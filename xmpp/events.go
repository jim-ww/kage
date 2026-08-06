package xmpp

import (
	"encoding/xml"
	"log/slog"
	"time"

	omemolib "github.com/jim-ww/omemo-go"
	"mellium.im/xmlstream"
	"mellium.im/xmpp/stanza"
)

// Event is a value received from a Client's event stream.
type Event interface{ isEvent() }

// MessageEvent is an incoming chat message.
type MessageEvent struct {
	ID     string
	From   string
	Body   string // fallback-quote already stripped if this is a XEP-0461 reply
	SentAt time.Time

	// ReplaceID is non-empty if this is a XEP-0308 correction of an earlier
	// message with this ID.
	ReplaceID string

	// ReplyToID is non-empty if this is a XEP-0461 reply to the message with
	// this ID.
	ReplyToID string

	// RetractID is non-empty if this is a XEP-0424 retraction of an earlier
	// message with this ID. When set, the other fields besides ID/From/SentAt
	// carry no meaningful content (Body is just the compatibility fallback
	// text, if present at all).
	RetractID string

	// ReactionTargetID is non-empty if this is a XEP-0444 reaction-set update:
	// Reactions is the sender's complete, current reaction set on the
	// message with this ID (may be empty, meaning they cleared it). When
	// set, other fields besides ID/From/SentAt carry no meaningful content.
	ReactionTargetID string
	Reactions        []string

	// Encrypted is non-nil if this is a XEP-0384 OMEMO message; Body/other
	// fields carry no meaningful content and the caller must decrypt this
	// (crypto/omemo) to get the actual message.
	Encrypted *omemoEncryptedElem

	// EncryptedV1 is non-nil if this is a legacy
	// (eu.siacs.conversations.axolotl) OMEMO message; same shape as
	// Encrypted otherwise.
	EncryptedV1 *omemoEncryptedElemV1

	// OOBURLs are the XEP-0066 out-of-band URLs this message explicitly
	// marked as file attachments, if any. Only ever set on the plaintext
	// path (a sender that encrypts its body has no reason to also leak
	// these in the clear - see xmpp.SendOptions.OOBURLs).
	OOBURLs []string
}

func (MessageEvent) isEvent() {}

// PresenceEvent is an incoming presence update.
type PresenceEvent struct {
	From string
	// Available is false for unavailable/error/subscription-management
	// presence (RFC 6121 §4.7.1) — anything other than a plain "here I am"
	// broadcast.
	Available bool
	// Show is the optional <show/> value when Available is true: "" (plain
	// online), "chat", "away", "xa", or "dnd".
	Show string

	// Caps is the XEP-0115 entity capabilities element carried on this
	// presence, if any - nil if the sender didn't include one.
	Caps *capsElem
}

func (PresenceEvent) isEvent() {}

// SubscriptionRequestEvent is an inbound XEP-0084/RFC 6121 request from From
// asking to subscribe to our presence. The caller is expected to respond,
// typically by auto-approving via Client.ApproveSubscription.
type SubscriptionRequestEvent struct {
	From string
}

func (SubscriptionRequestEvent) isEvent() {}

// ChatStateEvent is an incoming XEP-0085 chat state notification, standalone
// or attached to a regular message.
type ChatStateEvent struct {
	From  string
	State ChatState
}

func (ChatStateEvent) isEvent() {}

// MessageDeliveredEvent is a XEP-0184 delivery receipt: the peer acknowledged
// receipt of the message we sent with this ID.
type MessageDeliveredEvent struct {
	ID   string
	From string
}

func (MessageDeliveredEvent) isEvent() {}

// DeviceListChangedEvent is a XEP-0163 PEP push notification that From's
// OMEMO device list (of the given Protocol) changed. The caller should
// re-fetch and update its cached copy of that peer's device list (e.g. via
// omemo-go's Manager.SyncDevices) - without this, a cached device list is
// only ever refreshed when it's completely empty (a brand new peer) or as
// a last-resort retry when every currently-known device fails, so a peer
// adding a new device (or dropping an old one after a wipe) is otherwise
// invisible until one of those rare conditions happens to occur.
type DeviceListChangedEvent struct {
	From     string
	Protocol omemolib.Protocol
}

func (DeviceListChangedEvent) isEvent() {}

// Events returns the channel of incoming events, populated for the lifetime
// of the connection (from Dial until Close). The channel closes once the
// session ends.
func (c *Client) Events() <-chan Event {
	return c.events
}

func (c *Client) handleStanza(t xmlstream.TokenReadEncoder, start *xml.StartElement) {
	events := c.events
	switch start.Name.Local {
	case "iq":
		// Incoming IQ requests - most importantly disco#info/disco#items
		// (see disco.go), without which contacts have no way to learn we
		// support OMEMO at all. Responses to our own outstanding SendIQ/
		// SendIQElement calls are matched and consumed by the session layer
		// before ever reaching this handler, so this only ever sees IQs
		// actually directed at us to answer.
		if err := c.discoMux.HandleXMPP(t, start); err != nil {
			slog.Warn("handling incoming iq", "err", err)
		}
	case "message":
		d := xml.NewTokenDecoder(t)
		var msg messageBody
		// DecodeElement legitimately returns a non-nil, non-io.EOF error here
		// (an "unexpected end element" for the message's own closing tag) even
		// on a fully successful decode, because the token stream handed to the
		// handler is already positioned inside the element start passed to us.
		// The decoded value is valid regardless; only bail if we got nothing.
		_ = d.DecodeElement(&msg, start)

		// XEP-0280: unwrap a carbon-copied message and process the original
		// as if it arrived directly - this is what lets another resource on
		// this account see messages the server delivered to a different one.
		switch {
		case msg.CarbonReceived != nil:
			msg = msg.CarbonReceived.Forwarded.Message
		case msg.CarbonSent != nil:
			msg = msg.CarbonSent.Forwarded.Message
		}

		if msg.MAMResult != nil {
			c.dispatchArchiveResult(msg.MAMResult)
			return
		}

		if msg.Received != nil {
			events <- MessageDeliveredEvent{ID: msg.Received.ID, From: msg.From.String()}
			return
		}

		if msg.Request != nil && msg.ID != "" {
			// Acknowledge receipt back to the sender. Written directly onto t
			// (rather than via a separate Send call) since t is the same
			// bidirectional stream handleStanza was handed - mirrors how
			// mellium's own receipts.Handler responds.
			reply := stanza.Message{To: msg.From, Type: msg.Type}
			_, err := xmlstream.Copy(t, reply.Wrap(xmlstream.Wrap(nil, xml.StartElement{
				Name: xml.Name{Space: "urn:xmpp:receipts", Local: "received"},
				Attr: []xml.Attr{{Name: xml.Name{Local: "id"}, Value: msg.ID}},
			})))
			if err != nil {
				slog.Warn("sending delivery receipt", "id", msg.ID, "err", err)
			}
		}

		if msg.PubsubEvent != nil && msg.PubsubEvent.Items != nil {
			switch msg.PubsubEvent.Items.Node {
			case omemoDevicesNode:
				events <- DeviceListChangedEvent{From: msg.From.String(), Protocol: omemolib.ProtocolV2}
				return
			case omemoV1DevicesNode:
				events <- DeviceListChangedEvent{From: msg.From.String(), Protocol: omemolib.ProtocolV1}
				return
			}
		}

		if state, ok := msg.chatState(); ok {
			events <- ChatStateEvent{From: msg.From.String(), State: state}
		}

		if msg.Retract != nil {
			events <- MessageEvent{
				ID:        msg.ID,
				From:      msg.From.String(),
				SentAt:    time.Now(),
				RetractID: msg.Retract.ID,
			}
			return
		}

		if msg.Reactions != nil {
			reactions := msg.Reactions.Reactions
			if reactions == nil {
				reactions = []string{} // distinguish "cleared" from "field absent" for callers
			}
			events <- MessageEvent{
				ID:               msg.ID,
				From:             msg.From.String(),
				SentAt:           time.Now(),
				ReactionTargetID: msg.Reactions.ID,
				Reactions:        reactions,
			}
			return
		}

		if msg.Encrypted != nil {
			// Unlike the plaintext body, an OMEMO ciphertext can't be sliced
			// by the <fallback/> element's start/end offsets - the reply
			// quote lives in-band inside the decrypted plaintext instead
			// (see adapter.go's send()/stripReplyQuote). But the <reply/>
			// and <replace/> elements themselves are sent unencrypted
			// alongside Encrypted (see xmpp/send.go), so they're still
			// available here and must be read - otherwise an encrypted
			// reply never gets linked to what it replied to, and an
			// encrypted XEP-0308 edit loses its ReplaceID and gets stored
			// as a brand-new message instead of correcting the original
			// (dispatchArchiveResult's MAM path already gets this right;
			// this mirrors it for the live path).
			var replyToID, replaceID string
			if msg.Reply != nil {
				replyToID = msg.Reply.ID
			}
			if msg.Replace != nil {
				replaceID = msg.Replace.ID
			}
			events <- MessageEvent{
				ID:        msg.ID,
				From:      msg.From.String(),
				SentAt:    time.Now(),
				Encrypted: msg.Encrypted,
				ReplyToID: replyToID,
				ReplaceID: replaceID,
			}
			return
		}

		if msg.EncryptedV1 != nil {
			var replyToID, replaceID string
			if msg.Reply != nil {
				replyToID = msg.Reply.ID
			}
			if msg.Replace != nil {
				replaceID = msg.Replace.ID
			}
			events <- MessageEvent{
				ID:          msg.ID,
				From:        msg.From.String(),
				SentAt:      time.Now(),
				EncryptedV1: msg.EncryptedV1,
				ReplyToID:   replyToID,
				ReplaceID:   replaceID,
			}
			return
		}

		if msg.Body == "" {
			return
		}

		body := msg.Body
		var replyToID string
		if msg.Reply != nil {
			replyToID = msg.Reply.ID
			if msg.Fallback != nil && msg.Fallback.For == "urn:xmpp:reply:0" && msg.Fallback.Body != nil {
				start := 0
				if msg.Fallback.Body.Start != nil {
					start = *msg.Fallback.Body.Start
				}
				end := len(body)
				if msg.Fallback.Body.End != nil {
					end = *msg.Fallback.Body.End
				}
				if start >= 0 && start <= end && end <= len(body) {
					body = body[:start] + body[end:]
				}
			}
		}
		var replaceID string
		if msg.Replace != nil {
			replaceID = msg.Replace.ID
		}

		var oobURLs []string
		for _, x := range msg.OOB {
			if x.URL != "" {
				oobURLs = append(oobURLs, x.URL)
			}
		}

		events <- MessageEvent{
			ID:        msg.ID,
			From:      msg.From.String(),
			Body:      body,
			SentAt:    time.Now(),
			ReplaceID: replaceID,
			ReplyToID: replyToID,
			OOBURLs:   oobURLs,
		}
	case "presence":
		var p presenceBody
		d := xml.NewTokenDecoder(t)
		_ = d.DecodeElement(&p, start)
		switch p.Type {
		case stanza.SubscribePresence:
			events <- SubscriptionRequestEvent{From: p.From.String()}
		case stanza.SubscribedPresence, stanza.UnsubscribePresence, stanza.UnsubscribedPresence, stanza.ErrorPresence, stanza.ProbePresence:
			// Subscription-management and probe/error presence carry no
			// availability information - reporting them as PresenceEvent
			// would misrepresent the contact as offline.
		default:
			events <- PresenceEvent{
				From:      p.From.String(),
				Available: p.Type == stanza.AvailablePresence,
				Show:      p.Show,
				Caps:      p.Caps,
			}
		}
	}
}
