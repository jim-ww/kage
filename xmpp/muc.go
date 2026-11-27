package xmpp

import (
	"context"
	"encoding/xml"
	"fmt"
	"slices"
	"time"

	"mellium.im/xmlstream"
	"mellium.im/xmpp/disco"
	"mellium.im/xmpp/jid"
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
)

// mucUserElem is a room's <x xmlns='...muc#user'/> on an occupant's
// presence: who they are and why the presence was sent.
type mucUserElem struct {
	Item *struct {
		JID         string `xml:"jid,attr"`
		Nick        string `xml:"nick,attr"`
		Affiliation string `xml:"affiliation,attr"`
		Role        string `xml:"role,attr"`
	} `xml:"http://jabber.org/protocol/muc#user item"`
	Status []struct {
		Code int `xml:"code,attr"`
	} `xml:"http://jabber.org/protocol/muc#user status"`
}

// RoomPresenceEvent is an occupant's presence in a room (XEP-0045 §7.2).
type RoomPresenceEvent struct {
	Room string // the room's bare JID
	Nick string // the occupant's nick in it

	// RealJID is the occupant's bare JID, "" where the room hides it from
	// us (a semi-anonymous room, we not being a moderator).
	RealJID string

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
	Password string `xml:"http://jabber.org/protocol/muc#user password"`
}

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

// JoinRoom enters room as nick (XEP-0045 §7.2), asking for the history
// since since, or the room's default amount of it for a zero since. The
// outcome arrives as our own RoomPresenceEvent, or a PresenceErrorEvent.
func (c *Client) JoinRoom(ctx context.Context, room, nick string, since time.Time) error {
	occupant, err := occupantJID(room, nick)
	if err != nil {
		return err
	}
	var history xml.TokenReader
	if !since.IsZero() {
		history = xmlstream.Wrap(nil, xml.StartElement{
			Name: xml.Name{Local: "history"},
			Attr: []xml.Attr{{Name: xml.Name{Local: "since"}, Value: since.UTC().Format(time.RFC3339)}},
		})
	}
	x := xmlstream.Wrap(history, xml.StartElement{Name: xml.Name{Space: mucNS, Local: "x"}})
	return c.session.Send(ctx, stanza.Presence{To: occupant, Type: stanza.AvailablePresence}.Wrap(
		xmlstream.MultiReader(x, discoCaps().TokenReader()),
	))
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

// RoomName is the name room gives itself (XEP-0045 §6.4), "" if none.
func (c *Client) RoomName(ctx context.Context, room string) (string, error) {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	j, err := jid.Parse(room)
	if err != nil {
		return "", fmt.Errorf("parsing room %q: %w", room, err)
	}
	info, err := disco.GetInfo(ctx, "", j.Bare(), c.session)
	if err != nil {
		return "", err
	}
	for _, id := range info.Identity {
		if id.Category == "conference" {
			return id.Name, nil
		}
	}
	return "", nil
}
