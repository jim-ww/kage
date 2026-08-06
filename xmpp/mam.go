package xmpp

import (
	"context"
	"encoding/xml"
	"fmt"
	"time"

	"mellium.im/xmlstream"
	"mellium.im/xmpp/form"
	"mellium.im/xmpp/paging"
	"mellium.im/xmpp/stanza"
)

// mamNS is the XEP-0313 (Message Archive Management) namespace.
const mamNS = "urn:xmpp:mam:2"

// ArchivedMessage is one XEP-0313 archive result: a single forwarded message
// plus the server-assigned archive ID it can be paged/resumed from. Exactly
// one of {Body/Encrypted, RetractID, ReactionTargetID, ReplaceID} is set per
// message per XEP-0424/0444/0308 - see dispatchArchiveResult.
type ArchivedMessage struct {
	ArchiveID string // server-assigned MAM id, used as the RSM "after" cursor to resume
	SentAt    time.Time
	From      string
	To        string
	Body      string
	ID        string // stanza id of the original message, if any (for XEP-0461/0308/0424 correlation)

	// Encrypted is non-nil if the archived message is a XEP-0384 OMEMO
	// message; Body carries no meaningful content and the caller must
	// decrypt this (crypto/omemo, via DecodeOmemoMessage) to get the text.
	Encrypted *omemoEncryptedElem

	// EncryptedV1 is non-nil if the archived message is a legacy
	// (eu.siacs.conversations.axolotl) OMEMO message; same shape as
	// Encrypted otherwise.
	EncryptedV1 *omemoEncryptedElemV1

	// RetractID is non-empty if this archived item is a XEP-0424 retraction
	// of an earlier message with this ID. Other fields besides
	// ArchiveID/From/SentAt carry no meaningful content.
	RetractID string

	// ReactionTargetID is non-empty if this archived item is a XEP-0444
	// reaction-set update: Reactions is the sender's complete, current
	// reaction set on the message with this ID (may be empty, meaning they
	// cleared it). Other fields besides ArchiveID/From/SentAt carry no
	// meaningful content.
	ReactionTargetID string
	Reactions        []string

	// ReplaceID is non-empty if this archived item is a XEP-0308 correction
	// of an earlier message with this ID; Body (or Encrypted) carries the
	// corrected content, same as a normal message.
	ReplaceID string

	// OOBURLs are the XEP-0066 out-of-band URLs this archived item explicitly
	// marked as file attachments, if any.
	OOBURLs []string
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
	Delay struct {
		Stamp string `xml:"stamp,attr"`
	} `xml:"urn:xmpp:delay delay"`
	Message messageBody `xml:"message"`
}

// dispatchArchiveResult routes a decoded MAM <result/> to the FetchArchive
// call waiting on its queryid, if any is still in flight (a result for a
// queryid nobody's waiting on — e.g. arriving after the caller's context was
// canceled — is silently dropped).
func (c *Client) dispatchArchiveResult(r *mamResultElem) {
	c.mamMu.Lock()
	ch, ok := c.mamWaiters[r.QueryID]
	c.mamMu.Unlock()
	if !ok {
		return
	}

	msg := r.Forwarded.Message
	// MAM archives every <message/> a server saw, including chat states -
	// which carry nothing worth persisting even live (see handleStanza) -
	// and, unlike the live path, XEP-0424 retractions/XEP-0444 reactions/
	// XEP-0308 corrections need to survive being forwarded here so the
	// caller can apply them to already-synced history, same as the live
	// path does for ones that arrive while connected.
	if _, isChatState := msg.chatState(); isChatState {
		return
	}

	am := ArchivedMessage{
		ArchiveID: r.ID,
		From:      msg.From.String(),
		To:        msg.To.String(),
		ID:        msg.ID,
	}
	if stamp, err := time.Parse(time.RFC3339, r.Forwarded.Delay.Stamp); err == nil {
		am.SentAt = stamp
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

// FetchArchive retrieves one page of XEP-0313 history for the 1:1
// conversation with peerJID, strictly newer than afterArchiveID (empty
// fetches from the beginning of the archive), in chronological order.
// Callers should page by re-calling with afterArchiveID set to the last
// returned ArchiveID until complete is true.
func (c *Client) FetchArchive(ctx context.Context, peerJID, afterArchiveID string, max uint64) (results []ArchivedMessage, complete bool, err error) {
	queryID := randomID()

	ch := make(chan ArchivedMessage, max+8)
	c.mamMu.Lock()
	if c.mamWaiters == nil {
		c.mamWaiters = make(map[string]chan ArchivedMessage)
	}
	c.mamWaiters[queryID] = ch
	c.mamMu.Unlock()
	defer func() {
		c.mamMu.Lock()
		delete(c.mamWaiters, queryID)
		c.mamMu.Unlock()
	}()

	d := form.New(
		form.Hidden("FORM_TYPE", form.Value(mamNS)),
		form.JID("with", form.Value(peerJID)),
	)
	submission, ok := d.Submit()
	if !ok {
		return nil, false, fmt.Errorf("building mam query form")
	}

	rsm := paging.RequestNext{Max: max, After: afterArchiveID}
	query := xmlstream.Wrap(
		xmlstream.MultiReader(submission, rsm.TokenReader()),
		xml.StartElement{
			Name: xml.Name{Space: mamNS, Local: "query"},
			Attr: []xml.Attr{{Name: xml.Name{Local: "queryid"}, Value: queryID}},
		},
	)

	iq := stanza.IQ{Type: stanza.SetIQ, ID: randomID()}
	rc, err := c.session.SendIQElement(ctx, query, iq)
	if err != nil {
		return nil, false, fmt.Errorf("sending mam query: %w", err)
	}
	defer rc.Close()

	var fin struct {
		Complete bool `xml:"complete,attr"`
	}
	if err := xml.NewTokenDecoder(rc).Decode(&fin); err != nil {
		return nil, false, fmt.Errorf("decoding mam fin: %w", err)
	}

	// By the time SendIQElement's response (the <iq> fin) is delivered, every
	// <message> result for this queryid has already been read and dispatched
	// by the same sequential stream loop (see (*Client).handleStanza) — so
	// draining non-blockingly here is safe and complete.
drain:
	for {
		select {
		case msg := <-ch:
			results = append(results, msg)
		default:
			break drain
		}
	}

	return results, fin.Complete, nil
}
