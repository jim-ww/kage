package xmpp

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"mellium.im/xmlstream"
	"mellium.im/xmpp/disco"
	"mellium.im/xmpp/jid"
	"mellium.im/xmpp/ping"
	"mellium.im/xmpp/stanza"
)

// Multi-user chat (XEP-0045) namespaces.
const (
	mucNS      = "http://jabber.org/protocol/muc"
	mucUserNS  = "http://jabber.org/protocol/muc#user"
	mucOwnerNS = "http://jabber.org/protocol/muc#owner"
)

// XEP-0045 §15.6 status codes kage acts on.
const (
	MUCStatusSelf        = 110 // the presence is our own
	MUCStatusCreated     = 201 // our join created the room, locked until configured
	MUCStatusNickChanged = 303 // the occupant changed nick; Nick is the new one
	MUCStatusBanned      = 301
	MUCStatusKicked      = 307
	MUCStatusNickByRoom  = 210 // the room changed the nick we joined with
	MUCStatusRemoved     = 321 // removed on an affiliation change
	MUCStatusMembersOnly = 322 // removed as the room became members-only
	MUCStatusShutdown    = 332 // removed as the service is shutting down
	MUCStatusConfig      = 104 // the room's configuration changed
)

// occupantIDNS is XEP-0421's: a room stamps an id per occupant on what it
// relays, stable across nick changes.
const occupantIDNS = "urn:xmpp:occupant-id:0"

// occupantIDElem is a room's <occupant-id/>.
type occupantIDElem struct {
	ID string `xml:"id,attr"`
}

// mucUserElem is a room's <x xmlns='...muc#user'/> on an occupant's
// presence: who they are and why the presence was sent.
type mucUserElem struct {
	Item *struct {
		JID         string `xml:"jid,attr"`
		Nick        string `xml:"nick,attr"`
		Affiliation string `xml:"affiliation,attr"`
		Role        string `xml:"role,attr"`
	} `xml:"http://jabber.org/protocol/muc#user item"`
	Status []mucStatus `xml:"http://jabber.org/protocol/muc#user status"`
}

// mucStatus is one of a room's status codes (XEP-0045 §15.6).
type mucStatus struct {
	Code int `xml:"code,attr"`
}

// hasStatus reports whether code is among statuses.
func hasStatus(statuses []mucStatus, code int) bool {
	return slices.ContainsFunc(statuses, func(s mucStatus) bool { return s.Code == code })
}

// RoomPresenceEvent is an occupant's presence in a room (XEP-0045 §7.2).
type RoomPresenceEvent struct {
	Room string // the room's bare JID
	Nick string // the occupant's nick in it

	// RealJID is the occupant's bare JID, "" where the room hides it from
	// us (a semi-anonymous room, we not being a moderator).
	RealJID string

	// OccupantID is the room's id for the occupant (XEP-0421), meaningful
	// only if the room says it stamps them (RoomInfo.OccupantID).
	OccupantID string

	Affiliation string // owner, admin, member, outcast or none
	Role        string // moderator, participant, visitor or none

	// Available is false once the occupant left (or was made to).
	Available bool
	Show      string

	// NewNick is the nick an occupant leaving with MUCStatusNickChanged
	// comes back as.
	NewNick string

	StatusCodes []int
}

func (RoomPresenceEvent) isEvent() {}

// Has reports whether the room sent code with the presence.
func (e RoomPresenceEvent) Has(code int) bool { return slices.Contains(e.StatusCodes, code) }

// RoomSubjectEvent is a room's subject (XEP-0045 §8.1), sent on joining and
// whenever it changes; "" clears it.
type RoomSubjectEvent struct {
	Room    string
	Nick    string // who set it, "" for the room itself
	Subject string
}

func (RoomSubjectEvent) isEvent() {}

// PresenceErrorEvent is an error presence from From: for a room, the join
// failed (XEP-0045 §7.2.6 onwards).
type PresenceErrorEvent struct {
	From      string
	Condition string
	Reason    string
}

func (PresenceErrorEvent) isEvent() {}

// RoomInviteEvent is an invitation to a room, mediated by it (XEP-0045
// §7.8.2) or direct (XEP-0249).
type RoomInviteEvent struct {
	Room     string
	From     string // the bare JID of who invited us
	Reason   string
	Password string
}

func (RoomInviteEvent) isEvent() {}

// mucInviteElem is a mediated invitation, inside the room's muc#user <x/>
// on a message from it.
type mucInviteElem struct {
	Invite *struct {
		From   string `xml:"from,attr"`
		Reason string `xml:"http://jabber.org/protocol/muc#user reason"`
	} `xml:"http://jabber.org/protocol/muc#user invite"`
	// Item is the sender's real JID on a message from a room's archive.
	Item *struct {
		JID string `xml:"jid,attr"`
	} `xml:"http://jabber.org/protocol/muc#user item,omitempty"`
	Password string      `xml:"http://jabber.org/protocol/muc#user password,omitempty"`
	Status   []mucStatus `xml:"http://jabber.org/protocol/muc#user status"`
}

// RoomConfigChangedEvent is a room saying its configuration changed
// (XEP-0045 §10.2.1): what it is is to be asked again.
type RoomConfigChangedEvent struct {
	Room string
}

func (RoomConfigChangedEvent) isEvent() {}

// directInviteElem is XEP-0249's direct invitation.
type directInviteElem struct {
	JID      string `xml:"jid,attr"`
	Reason   string `xml:"reason,attr"`
	Password string `xml:"password,attr"`
}

// roomPresence makes p, with its muc#user element, a RoomPresenceEvent.
func roomPresence(p presenceBody) RoomPresenceEvent {
	ev := RoomPresenceEvent{
		Room:      p.From.Bare().String(),
		Nick:      p.From.Resourcepart(),
		Available: p.Type == stanza.AvailablePresence,
		Show:      p.Show,
	}
	if p.OccupantID != nil {
		ev.OccupantID = p.OccupantID.ID
	}
	if item := p.MUCUser.Item; item != nil {
		ev.Affiliation, ev.Role, ev.NewNick = item.Affiliation, item.Role, item.Nick
		if j, err := jid.Parse(item.JID); err == nil && item.JID != "" {
			ev.RealJID = j.Bare().String()
		}
	}
	for _, s := range p.MUCUser.Status {
		ev.StatusCodes = append(ev.StatusCodes, s.Code)
	}
	return ev
}

// JoinOptions are how JoinRoom enters a room.
type JoinOptions struct {
	Password string
	// Show is our <show/> in the room, "" for plain online.
	Show string
	// NoHistory asks for no history at all, for a room whose archive
	// (XEP-0313) is read instead; else Since asks for the history since
	// then, a zero Since for the room's default amount of it.
	NoHistory bool
	Since     time.Time
}

// JoinRoom enters room as nick (XEP-0045 §7.2). The outcome arrives as our
// own RoomPresenceEvent, or a PresenceErrorEvent.
func (c *Client) JoinRoom(ctx context.Context, room, nick string, opts JoinOptions) error {
	occupant, err := occupantJID(room, nick)
	if err != nil {
		return err
	}
	var children []xml.TokenReader
	switch {
	case opts.NoHistory:
		children = append(children, xmlstream.Wrap(nil, xml.StartElement{
			Name: xml.Name{Local: "history"},
			Attr: []xml.Attr{{Name: xml.Name{Local: "maxstanzas"}, Value: "0"}},
		}))
	case !opts.Since.IsZero():
		children = append(children, xmlstream.Wrap(nil, xml.StartElement{
			Name: xml.Name{Local: "history"},
			Attr: []xml.Attr{{Name: xml.Name{Local: "since"}, Value: opts.Since.UTC().Format(time.RFC3339)}},
		}))
	}
	if opts.Password != "" {
		children = append(children, xmlstream.Wrap(xmlstream.Token(xml.CharData(opts.Password)), xml.StartElement{Name: xml.Name{Local: "password"}}))
	}
	return c.session.Send(ctx, stanza.Presence{To: occupant, Type: stanza.AvailablePresence}.Wrap(
		xmlstream.MultiReader(
			xmlstream.Wrap(xmlstream.MultiReader(children...), xml.StartElement{Name: xml.Name{Space: mucNS, Local: "x"}}),
			showElem(opts.Show),
			discoCaps().TokenReader(),
		),
	))
}

// SetRoomPresence sends our <show/> to the room we're in as nick, or with a
// new nick changes it (XEP-0045 §7.6).
func (c *Client) SetRoomPresence(ctx context.Context, room, nick, show string) error {
	occupant, err := occupantJID(room, nick)
	if err != nil {
		return err
	}
	return c.session.Send(ctx, stanza.Presence{To: occupant, Type: stanza.AvailablePresence}.Wrap(
		xmlstream.MultiReader(showElem(show), discoCaps().TokenReader()),
	))
}

// showElem is <show>show</show>, nothing for "".
func showElem(show string) xml.TokenReader {
	if show == "" {
		return nil
	}
	return xmlstream.Wrap(xmlstream.Token(xml.CharData(show)), xml.StartElement{Name: xml.Name{Local: "show"}})
}

// LeaveRoom exits room, which we're in as nick (XEP-0045 §7.14).
func (c *Client) LeaveRoom(ctx context.Context, room, nick string) error {
	occupant, err := occupantJID(room, nick)
	if err != nil {
		return err
	}
	return c.session.Send(ctx, stanza.Presence{To: occupant, Type: stanza.UnavailablePresence}.Wrap(nil))
}

// ConfigureInstantRoom accepts the default configuration of a room our join
// just created, unlocking it for others (XEP-0045 §10.1.2).
func (c *Client) ConfigureInstantRoom(ctx context.Context, room string) error {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	to, err := jid.Parse(room)
	if err != nil {
		return fmt.Errorf("parsing room %q: %w", room, err)
	}
	submit := xmlstream.Wrap(nil, xml.StartElement{
		Name: xml.Name{Space: "jabber:x:data", Local: "x"},
		Attr: []xml.Attr{{Name: xml.Name{Local: "type"}, Value: "submit"}},
	})
	query := xmlstream.Wrap(submit, xml.StartElement{Name: xml.Name{Space: mucOwnerNS, Local: "query"}})
	return c.session.UnmarshalIQElement(ctx, query, stanza.IQ{Type: stanza.SetIQ, To: to.Bare()}, nil)
}

// occupantJID is room/nick.
func occupantJID(room, nick string) (jid.JID, error) {
	r, err := jid.Parse(room)
	if err != nil {
		return jid.JID{}, fmt.Errorf("parsing room %q: %w", room, err)
	}
	j, err := r.Bare().WithResource(nick)
	if err != nil {
		return jid.JID{}, fmt.Errorf("nick %q: %w", nick, err)
	}
	return j, nil
}

// ValidRoomNick reports whether nick can be used in room's occupant JID.
func ValidRoomNick(room, nick string) error {
	if nick == "" {
		return fmt.Errorf("nick is empty")
	}
	_, err := occupantJID(room, nick)
	return err
}

// RoomInfo is what a room says of itself (XEP-0045 §6.4).
type RoomInfo struct {
	Name string
	// MembersOnly and NonAnonymous together make a room OMEMO can be used
	// in: every member's real JID is known to everyone.
	MembersOnly  bool
	NonAnonymous bool
	// OccupantID is set if the room stamps occupant-ids (XEP-0421), which
	// are then to be trusted.
	OccupantID bool
	// MAM is set if the room keeps an archive (XEP-0313).
	MAM bool
}

// Private reports whether OMEMO can be used in the room.
func (i RoomInfo) Private() bool { return i.MembersOnly && i.NonAnonymous }

// RoomInfo asks room what it is.
func (c *Client) RoomInfo(ctx context.Context, room string) (RoomInfo, error) {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	j, err := jid.Parse(room)
	if err != nil {
		return RoomInfo{}, fmt.Errorf("parsing room %q: %w", room, err)
	}
	info, err := disco.GetInfo(ctx, "", j.Bare(), c.session)
	if err != nil {
		return RoomInfo{}, err
	}
	var out RoomInfo
	for _, id := range info.Identity {
		if id.Category == "conference" && out.Name == "" {
			out.Name = id.Name
		}
	}
	for _, f := range info.Features {
		switch f.Var {
		case "muc_membersonly":
			out.MembersOnly = true
		case "muc_nonanonymous":
			out.NonAnonymous = true
		case occupantIDNS:
			out.OccupantID = true
		case mamNS:
			out.MAM = true
		}
	}
	return out, nil
}

// SelfPingResult is what a self-ping (XEP-0410) says of our place in a
// room.
type SelfPingResult int

const (
	// StillJoined: the room still has us as nick.
	StillJoined SelfPingResult = iota
	// NotJoined: the room doesn't have us (any more), to rejoin.
	NotJoined
	// PingUnknown: no telling, the room's service didn't answer.
	PingUnknown
)

// SelfPing asks room whether we're still in it as nick (XEP-0410 §3): a
// room may drop us without a word, on a restart say.
func (c *Client) SelfPing(ctx context.Context, room, nick string) SelfPingResult {
	occupant, err := occupantJID(room, nick)
	if err != nil {
		return PingUnknown
	}
	ctx, cancel := c.bound(ctx)
	defer cancel()
	return selfPingResult(ping.Send(ctx, c.session, occupant))
}

// selfPingResult reads a self-ping's answer (XEP-0410 §3.2).
func selfPingResult(err error) SelfPingResult {
	if err == nil {
		return StillJoined
	}
	var se stanza.Error
	if !errors.As(err, &se) {
		return PingUnknown
	}
	switch se.Condition {
	// Our own client answered, through the room: we're in it.
	case stanza.ServiceUnavailable, stanza.FeatureNotImplemented:
		return StillJoined
	case stanza.NotAcceptable, stanza.ItemNotFound:
		return NotJoined
	}
	return PingUnknown
}

// mucAdminNS is XEP-0045's for affiliation lists.
const mucAdminNS = "http://jabber.org/protocol/muc#admin"

// RoomMembers is the bare JIDs affiliated with room as owner, admin or
// member (XEP-0045 §9.5): everyone a message in a private room is
// encrypted for, online or not. A list the room won't show us is left out;
// only all of them refused is an error.
func (c *Client) RoomMembers(ctx context.Context, room string) ([]string, error) {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	to, err := jid.Parse(room)
	if err != nil {
		return nil, fmt.Errorf("parsing room %q: %w", room, err)
	}
	var members []string
	var lastErr error
	got := 0
	for _, affiliation := range []string{"owner", "admin", "member"} {
		query := xmlstream.Wrap(
			xmlstream.Wrap(nil, xml.StartElement{
				Name: xml.Name{Local: "item"},
				Attr: []xml.Attr{{Name: xml.Name{Local: "affiliation"}, Value: affiliation}},
			}),
			xml.StartElement{Name: xml.Name{Space: mucAdminNS, Local: "query"}},
		)
		var resp struct {
			Items []struct {
				JID string `xml:"jid,attr"`
			} `xml:"http://jabber.org/protocol/muc#admin item"`
		}
		if err := c.session.UnmarshalIQElement(ctx, query, stanza.IQ{Type: stanza.GetIQ, To: to.Bare()}, &resp); err != nil {
			lastErr = err
			continue
		}
		got++
		for _, item := range resp.Items {
			if j, err := jid.Parse(item.JID); err == nil && item.JID != "" {
				members = append(members, j.Bare().String())
			}
		}
	}
	if got == 0 {
		return nil, fmt.Errorf("reading room members: %w", lastErr)
	}
	slices.Sort(members)
	return slices.Compact(members), nil
}

// SetRoomConfig submits values for fields of room's configuration form
// (XEP-0045 §10.2), e.g. "muc#roomconfig_membersonly": "1"; the rest keep
// what they are.
func (c *Client) SetRoomConfig(ctx context.Context, room string, values map[string]string) error {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	to, err := jid.Parse(room)
	if err != nil {
		return fmt.Errorf("parsing room %q: %w", room, err)
	}
	fields := []xml.TokenReader{formField("FORM_TYPE", "http://jabber.org/protocol/muc#roomconfig", "hidden")}
	for _, name := range slices.Sorted(maps.Keys(values)) {
		fields = append(fields, formField(name, values[name], ""))
	}
	submit := xmlstream.Wrap(xmlstream.MultiReader(fields...), xml.StartElement{
		Name: xml.Name{Space: "jabber:x:data", Local: "x"},
		Attr: []xml.Attr{{Name: xml.Name{Local: "type"}, Value: "submit"}},
	})
	query := xmlstream.Wrap(submit, xml.StartElement{Name: xml.Name{Space: mucOwnerNS, Local: "query"}})
	return c.session.UnmarshalIQElement(ctx, query, stanza.IQ{Type: stanza.SetIQ, To: to.Bare()}, nil)
}

// SetAffiliation makes who (a bare JID) owner, admin, member, outcast or
// none in room (XEP-0045 §9, §10).
func (c *Client) SetAffiliation(ctx context.Context, room, who, affiliation string) error {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	to, err := jid.Parse(room)
	if err != nil {
		return fmt.Errorf("parsing room %q: %w", room, err)
	}
	item := xmlstream.Wrap(nil, xml.StartElement{
		Name: xml.Name{Local: "item"},
		Attr: []xml.Attr{{Name: xml.Name{Local: "affiliation"}, Value: affiliation}, {Name: xml.Name{Local: "jid"}, Value: who}},
	})
	query := xmlstream.Wrap(item, xml.StartElement{Name: xml.Name{Space: mucAdminNS, Local: "query"}})
	return c.session.UnmarshalIQElement(ctx, query, stanza.IQ{Type: stanza.SetIQ, To: to.Bare()}, nil)
}

// SetRoomSubject asks room to change its subject, "" clearing it
// (XEP-0045 §8.1), in a message with id: the room bounces it from an
// occupant not allowed to, and sends everyone the new subject otherwise.
func (c *Client) SetRoomSubject(ctx context.Context, room, id, subject string) error {
	to, err := jid.Parse(room)
	if err != nil {
		return fmt.Errorf("parsing room %q: %w", room, err)
	}
	payload := xmlstream.Wrap(xmlstream.Token(xml.CharData(subject)), xml.StartElement{Name: xml.Name{Local: "subject"}})
	return c.session.Send(ctx, stanza.Message{To: to.Bare(), Type: stanza.GroupChatMessage, ID: id}.Wrap(payload))
}
