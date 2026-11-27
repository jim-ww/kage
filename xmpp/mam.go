package xmpp

import (
	"context"
	"encoding/xml"
	"fmt"
	"time"

	"mellium.im/xmlstream"
	"mellium.im/xmpp/form"
	"mellium.im/xmpp/jid"
	"mellium.im/xmpp/paging"
	"mellium.im/xmpp/stanza"
)

// mamNS is the XEP-0313 (Message Archive Management) namespace.
const mamNS = "urn:xmpp:mam:2"

// ArchivedMessage is one archived message (XEP-0313) and its archive ID.
type ArchivedMessage struct {
	ArchiveID string // server-assigned MAM id, used as the RSM "after" cursor to resume
	SentAt    time.Time
	From      string
	To        string
	Body      string
	ID        string // stanza id of the original message, if any (for XEP-0461/0308/0424 correlation)

	// ReplyToID is the id of the message this one replies to (XEP-0461),
	// if any.
	ReplyToID string

	// Encrypted is non-nil if the archived message is a XEP-0384 OMEMO
	// message; Body carries no meaningful content and the caller must
	// decrypt this (crypto/omemo, via DecodeOmemoMessage) to get the text.
	Encrypted *OmemoEncryptedElem

	// EncryptedV1 is non-nil if the archived message is a legacy
	// (eu.siacs.conversations.axolotl) OMEMO message; same shape as
	// Encrypted otherwise.
	EncryptedV1 *OmemoEncryptedElemV1

	// RetractID is non-empty if this archived item is a XEP-0424 retraction
	// of an earlier message with this ID. Other fields besides
	// ArchiveID/From/SentAt carry no meaningful content.
	RetractID string

	// ReactionTargetID is set for a XEP-0444 update, see
	// MessageEvent.ReactionTargetID.
	ReactionTargetID string
	Reactions        []string

	// ReplaceID is non-empty if this archived item is a XEP-0308 correction
	// of an earlier message with this ID; Body (or Encrypted) carries the
	// corrected content, same as a normal message.
	ReplaceID string

	// OOBURLs are the XEP-0066 out-of-band URLs this archived item explicitly
	// marked as file attachments, if any.
	OOBURLs []string

	// From a room's archive only: OccupantID is the room's id for the sender
	// (XEP-0421), RealJID their bare JID where the room shows it.
	OccupantID string
	RealJID    string
}

// mamResultElem is the <result/> wrapper XEP-0313 attaches to a <message/>
// carrying one archived item.
type mamResultElem struct {
	QueryID   string       `xml:"queryid,attr"`
	ID        string       `xml:"id,attr"`
	Forwarded mamForwarded `xml:"urn:xmpp:forward:0 forwarded"`
}

// mamForwarded is XEP-0297: the original delay-stamped message, forwarded
// verbatim inside a MAM result.
type mamForwarded struct {
	Delay   delayElem   `xml:"urn:xmpp:delay delay"`
	Message messageBody `xml:"message"`
}

// mamWaiter is a FetchArchive waiting for its results, which only archive
// may send: "" for our own account's.
type mamWaiter struct {
	ch      chan ArchivedMessage
	archive string
}

// dispatchArchiveResult hands a MAM <result/> from from to the FetchArchive
// waiting on its queryid, if any and if from is the archive it asked.
func (c *Client) dispatchArchiveResult(r *mamResultElem, from jid.JID) {
	c.mamMu.Lock()
	w, ok := c.mamWaiters[r.QueryID]
	c.mamMu.Unlock()
	if !ok {
		return
	}
	if w.archive == "" && !c.fromOwnAccount(from) || w.archive != "" && from.String() != w.archive {
		return
	}
	ch := w.ch

	msg := r.Forwarded.Message
	// A chat state alone falls to the default case below and is dropped; one
	// riding along a body (Conversations sends <active/> with every message)
	// must not drop the message.

	am := ArchivedMessage{
		ArchiveID: r.ID,
		From:      msg.From.String(),
		To:        msg.To.String(),
		ID:        msg.selfID(),
	}
	if w.archive != "" {
		if msg.OccupantID != nil {
			am.OccupantID = msg.OccupantID.ID
		}
		if msg.MUCUser != nil && msg.MUCUser.Item != nil {
			if real, err := jid.Parse(msg.MUCUser.Item.JID); err == nil && msg.MUCUser.Item.JID != "" {
				am.RealJID = real.Bare().String()
			}
		}
	}
	// Left zero without a stamp: the caller decides what an unknown time
	// means (see syncArchiveForContact's cursor).
	if stamp, ok := parseStamp(r.Forwarded.Delay.Stamp); ok {
		am.SentAt = notAfter(stamp, c.Now())
	}

	switch {
	case msg.Retract != nil:
		am.RetractID = msg.Retract.ID
	case msg.Reactions != nil:
		reactions := msg.Reactions.Reactions
		if reactions == nil {
			reactions = []string{} // distinguish "cleared" from "field absent" for callers
		}
		am.ReactionTargetID = msg.Reactions.ID
		am.Reactions = reactions
	case msg.Replace != nil:
		am.ReplaceID = msg.Replace.ID
		am.Body = msg.Body
		am.Encrypted = msg.Encrypted
		am.EncryptedV1 = msg.EncryptedV1
		for _, x := range msg.OOB {
			if x.URL != "" {
				am.OOBURLs = append(am.OOBURLs, x.URL)
			}
		}
	case msg.Body != "" || msg.Encrypted != nil || msg.EncryptedV1 != nil:
		am.Body = msg.Body
		am.Encrypted = msg.Encrypted
		am.EncryptedV1 = msg.EncryptedV1
		if msg.Reply != nil {
			am.ReplyToID = msg.Reply.ID
			am.Body = stripReplyFallback(am.Body, msg.Fallback)
		}
		for _, x := range msg.OOB {
			if x.URL != "" {
				am.OOBURLs = append(am.OOBURLs, x.URL)
			}
		}
	default:
		// Nothing worth archiving/showing - see the belt-and-suspenders
		// check on the caller side too (syncArchiveForContact).
		return
	}

	select {
	case ch <- am:
	default: // buffer sized to the page's Max; a full buffer means a misbehaving server sent more than requested
	}
}

// ArchivePage is one page of XEP-0313 results.
type ArchivePage struct {
	// Items are the archived messages worth showing — the page's raw results
	// minus everything dispatchArchiveResult filters out.
	Items []ArchivedMessage

	// Complete is the <fin/> completeness flag: the archive holds nothing
	// after this page.
	Complete bool

	// Last is the page's RSM <last> id, to resume after - not the last
	// item's, as filtered chat states and receipts occupy the archive too.
	// "" for an empty page.
	Last string
}

// FetchArchive fetches a page of the archive with peerJID (XEP-0313) after
// afterArchiveID ("" for the start), oldest first. Page on with Last until
// Complete.
func (c *Client) FetchArchive(ctx context.Context, peerJID, afterArchiveID string, max uint64) (ArchivePage, error) {
	return c.fetchArchive(ctx, archiveQuery{with: peerJID, after: afterArchiveID, max: max})
}

// FetchRoomArchive fetches a page of room's own archive (XEP-0313 §5.1.2)
// after afterArchiveID, oldest first, or for "" its latest page.
func (c *Client) FetchRoomArchive(ctx context.Context, room, afterArchiveID string, max uint64) (ArchivePage, error) {
	return c.fetchArchive(ctx, archiveQuery{archive: room, after: afterArchiveID, latest: afterArchiveID == "", max: max})
}

// FetchArchiveSince is FetchArchive from the time since (MAM <start>) rather
// than an id, for a cursor id the server stopped resolving (see
// recoverMAMCursor); zero since for the start.
func (c *Client) FetchArchiveSince(ctx context.Context, peerJID string, since time.Time, max uint64) (ArchivePage, error) {
	return c.fetchArchive(ctx, archiveQuery{with: peerJID, since: since, max: max})
}

// archiveQuery is one MAM query: of archive ("" for our own account's),
// with with ("" for everyone), after an id or since a time, or its latest
// page.
type archiveQuery struct {
	archive, with string
	after         string
	since         time.Time
	latest        bool
	max           uint64
}

func (c *Client) fetchArchive(ctx context.Context, q archiveQuery) (ArchivePage, error) {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	queryID := randomID()
	max := q.max

	var to jid.JID
	if q.archive != "" {
		var err error
		if to, err = jid.Parse(q.archive); err != nil {
			return ArchivePage{}, fmt.Errorf("parsing archive %q: %w", q.archive, err)
		}
		to = to.Bare()
	}

	ch := make(chan ArchivedMessage, max+8)
	c.mamMu.Lock()
	if c.mamWaiters == nil {
		c.mamWaiters = make(map[string]mamWaiter)
	}
	c.mamWaiters[queryID] = mamWaiter{ch: ch, archive: to.String()}
	c.mamMu.Unlock()
	defer func() {
		c.mamMu.Lock()
		delete(c.mamWaiters, queryID)
		c.mamMu.Unlock()
	}()

	fields := []form.Field{form.Hidden("FORM_TYPE", form.Value(mamNS))}
	if q.with != "" {
		fields = append(fields, form.JID("with", form.Value(q.with)))
	}
	if !q.since.IsZero() {
		fields = append(fields, form.Text("start", form.Value(q.since.UTC().Format(time.RFC3339))))
	}
	d := form.New(fields...)
	submission, ok := d.Submit()
	if !ok {
		return ArchivePage{}, fmt.Errorf("building mam query form")
	}

	var rsm xml.TokenReader
	if q.latest {
		rsm = (&paging.RequestPrev{Max: max}).TokenReader()
	} else {
		rsm = (&paging.RequestNext{Max: max, After: q.after}).TokenReader()
	}
	query := xmlstream.Wrap(
		xmlstream.MultiReader(submission, rsm),
		xml.StartElement{
			Name: xml.Name{Space: mamNS, Local: "query"},
			Attr: []xml.Attr{{Name: xml.Name{Local: "queryid"}, Value: queryID}},
		},
	)

	iq := stanza.IQ{Type: stanza.SetIQ, ID: randomID(), To: to}
	rc, err := c.session.SendIQElement(ctx, query, iq)
	if err != nil {
		return ArchivePage{}, fmt.Errorf("sending mam query: %w", err)
	}
	defer rc.Close()

	page, err := decodeMAMFin(rc)
	if err != nil {
		return ArchivePage{}, err
	}

	// Every result was dispatched before the <fin> by the same stream loop.
drain:
	for {
		select {
		case msg := <-ch:
			page.Items = append(page.Items, msg)
		default:
			break drain
		}
	}

	return page, nil
}

// decodeMAMFin reads a MAM response's <fin/>: whether it's complete, and the
// RSM <last>. r is the whole <iq>, so the <fin> is nested in it.
func decodeMAMFin(r xml.TokenReader) (ArchivePage, error) {
	var resp struct {
		Fin struct {
			Complete bool   `xml:"complete,attr"`
			Last     string `xml:"http://jabber.org/protocol/rsm set>last"`
		} `xml:"urn:xmpp:mam:2 fin"`
	}
	if err := xml.NewTokenDecoder(r).Decode(&resp); err != nil {
		return ArchivePage{}, fmt.Errorf("decoding mam fin: %w", err)
	}
	return ArchivePage{Complete: resp.Fin.Complete, Last: resp.Fin.Last}, nil
}
