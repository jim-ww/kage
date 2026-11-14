package xmpp

import (
	"encoding/xml"
	"log/slog"
	"time"

	omemolib "github.com/jim-ww/omemo-go"
	"mellium.im/xmlstream"
	"mellium.im/xmpp/jid"
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

	// RetractID is set for a XEP-0424 retraction of that message; then only
	// ID, From and SentAt mean anything.
	RetractID string

	// ReactionTargetID is set for a XEP-0444 update: Reactions is the
	// sender's whole set on that message, maybe empty.
	ReactionTargetID string
	Reactions        []string

	// Encrypted is non-nil if this is a XEP-0384 OMEMO message; Body/other
	// fields carry no meaningful content and the caller must decrypt this
	// (crypto/omemo) to get the actual message.
	Encrypted *OmemoEncryptedElem

	// EncryptedV1 is non-nil if this is a legacy
	// (eu.siacs.conversations.axolotl) OMEMO message; same shape as
	// Encrypted otherwise.
	EncryptedV1 *OmemoEncryptedElemV1

	// OOBURLs are the URLs marked as files (XEP-0066); plaintext only.
	OOBURLs []string

	// Outgoing is set for a "sent" carbon (XEP-0280), our other device's
	// message: the chat is with To.
	Outgoing bool

	// ChatStates is set if the message carried a chat state (XEP-0085):
	// one without, from a contact, says they don't do chat states.
	ChatStates bool

	// To is the stanza's original recipient. Only meaningful (and only set)
	// when Outgoing is true; a normal inbound message is always addressed
	// to us, so callers key ordinary messages off From instead.
	To string
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

// SubscriptionRequestEvent is From asking for our presence (RFC 6121 §3.1).
type SubscriptionRequestEvent struct {
	From string
}

func (SubscriptionRequestEvent) isEvent() {}

// RosterPushEvent is a roster item changed (RFC 6121 §2.1.6).
type RosterPushEvent struct {
	JID string

	// Name is the roster item's display nickname, empty if it has none.
	Name string

	// Subscription is RFC 6121's subscription state: "none", "to", "from",
	// "both", or "remove" (the item was deleted - Removed reports that more
	// directly).
	Subscription string

	// Ask is true when a subscription request we sent is still pending the
	// contact's approval (the item's ask="subscribe" attribute).
	Ask bool

	// Removed is true when this push deletes the item
	// (subscription="remove"), rather than changing it.
	Removed bool

	// Groups are the roster groups the item belongs to, if any.
	Groups []string
}

func (RosterPushEvent) isEvent() {}

// MessageBouncedEvent is our message ID come back undelivered from From
// (RFC 6120 §8.3), with the error as Reason.
type MessageBouncedEvent struct {
	ID     string
	From   string
	Reason string
}

func (MessageBouncedEvent) isEvent() {}

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

// DeviceListChangedEvent is a PEP push (XEP-0163): From's OMEMO device list
// changed, to re-fetch.
type DeviceListChangedEvent struct {
	From     string
	Protocol omemolib.Protocol
}

func (DeviceListChangedEvent) isEvent() {}

// AvatarChangedEvent is a PEP push: From's avatar changed, to re-fetch (see
// FetchAvatarMetadata).
type AvatarChangedEvent struct {
	From string
}

func (AvatarChangedEvent) isEvent() {}

// Events returns the channel of incoming events, populated for the lifetime
// of the connection (from Dial until Close). The channel closes once the
// session ends.
func (c *Client) Events() <-chan Event {
	return c.events
}

// fromOwnAccount reports whether a stanza from from was sent by our own
// account (our bare JID), as a carbon or an archive result must be. No from
// at all is the server speaking for our account (RFC 6120 §8.1.2.1).
func (c *Client) fromOwnAccount(from jid.JID) bool {
	return from.Equal(jid.JID{}) || from.Equal(c.JID.Bare())
}

func (c *Client) handleStanza(t xmlstream.TokenReadEncoder, start *xml.StartElement) {
	switch start.Name.Local {
	case "iq":
		// IQs to us, like disco#info (disco.go); responses to ours don't
		// get here.
		if err := c.discoMux.HandleXMPP(t, start); err != nil {
			slog.Warn("handling incoming iq", "err", err)
		}
	case "message":
		d := xml.NewTokenDecoder(t)
		var msg messageBody
		// Errors on the closing tag even when it decoded, the stream being
		// inside the element already.
		_ = d.DecodeElement(&msg, start)
		logXML("IN", c.JID.String(), msg)

		// An error is our own message bounced, often with our payload in it,
		// which isn't the recipient's: only its ID and error count.
		if msg.Type == stanza.ErrorMessage {
			c.enqueue(MessageBouncedEvent{ID: msg.ID, From: msg.From.String(), Reason: bounceReason(msg.Error)})
			return
		}

		// Carbons (XEP-0280) are unwrapped. A "sent" one is from our own JID,
		// its chat being To. Only our own account forwards them: from anyone
		// else, a carbon is a forgery passing someone's message off as
		// another's, or as one of ours (XEP-0280 §11), and is dropped.
		outgoing := false
		carbon := msg.CarbonReceived != nil || msg.CarbonSent != nil
		if carbon {
			if !c.fromOwnAccount(msg.From) {
				slog.Warn("dropping a carbon not from our own account", "from", msg.From.String())
				return
			}
		}
		switch {
		case msg.CarbonReceived != nil:
			msg = msg.CarbonReceived.Forwarded.Message
		case msg.CarbonSent != nil:
			msg = msg.CarbonSent.Forwarded.Message
			outgoing = true
		}

		if msg.MAMResult != nil {
			c.dispatchArchiveResult(msg.MAMResult)
			return
		}

		if msg.Received != nil {
			c.enqueue(MessageDeliveredEvent{ID: msg.Received.ID, From: msg.From.String()})
			return
		}

		// Not for a carbon: the device it was sent to answers for a received
		// one, and a sent one is ours (XEP-0280 §10.4).
		if msg.Request != nil && msg.ID != "" && !carbon && c.receiptAllowed(msg.From) {
			// The receipt, written onto t as mellium's receipts.Handler does.
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
				c.enqueue(DeviceListChangedEvent{From: msg.From.String(), Protocol: omemolib.ProtocolV2})
				return
			case omemoV1DevicesNode:
				c.enqueue(DeviceListChangedEvent{From: msg.From.String(), Protocol: omemolib.ProtocolV1})
				return
			case avatarMetadataNode:
				c.enqueue(AvatarChangedEvent{From: msg.From.String()})
				return
			}
		}

		if ev, ok := msg.jmiEvent(); ok {
			c.enqueue(ev)
			return
		}

		// A sent carbon's chat state is our own, typed on another of our
		// devices - not anything the contact is doing.
		state, chatStates := msg.chatState()
		if chatStates && !outgoing {
			c.enqueue(ChatStateEvent{From: msg.From.String(), State: state})
		}

		if msg.Retract != nil {
			c.enqueue(MessageEvent{
				ID:        msg.selfID(),
				From:      msg.From.String(),
				SentAt:    time.Now(),
				RetractID: msg.Retract.ID,
				Outgoing:  outgoing,
				To:        msg.To.String(),
			})
			return
		}

		if msg.Reactions != nil {
			reactions := msg.Reactions.Reactions
			if reactions == nil {
				reactions = []string{} // distinguish "cleared" from "field absent" for callers
			}
			c.enqueue(MessageEvent{
				ID:               msg.selfID(),
				From:             msg.From.String(),
				SentAt:           time.Now(),
				ReactionTargetID: msg.Reactions.ID,
				Reactions:        reactions,
				Outgoing:         outgoing,
				To:               msg.To.String(),
			})
			return
		}

		if msg.Encrypted != nil {
			// <reply/> and <replace/> are unencrypted beside the ciphertext;
			// the quote is inside it (see stripReplyQuote).
			var replyToID, replaceID string
			if msg.Reply != nil {
				replyToID = msg.Reply.ID
			}
			if msg.Replace != nil {
				replaceID = msg.Replace.ID
			}
			c.enqueue(MessageEvent{
				ID:         msg.selfID(),
				From:       msg.From.String(),
				SentAt:     time.Now(),
				Encrypted:  msg.Encrypted,
				ReplyToID:  replyToID,
				ReplaceID:  replaceID,
				Outgoing:   outgoing,
				To:         msg.To.String(),
				ChatStates: chatStates,
			})
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
			c.enqueue(MessageEvent{
				ID:          msg.selfID(),
				From:        msg.From.String(),
				SentAt:      time.Now(),
				EncryptedV1: msg.EncryptedV1,
				ReplyToID:   replyToID,
				ReplaceID:   replaceID,
				Outgoing:    outgoing,
				To:          msg.To.String(),
				ChatStates:  chatStates,
			})
			return
		}

		if msg.Body == "" {
			return
		}

		body := msg.Body
		var replyToID string
		if msg.Reply != nil {
			replyToID = msg.Reply.ID
			body = stripReplyFallback(body, msg.Fallback)
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

		c.enqueue(MessageEvent{
			ID:         msg.selfID(),
			From:       msg.From.String(),
			Body:       body,
			SentAt:     time.Now(),
			ReplaceID:  replaceID,
			ReplyToID:  replyToID,
			OOBURLs:    oobURLs,
			Outgoing:   outgoing,
			To:         msg.To.String(),
			ChatStates: chatStates,
		})
	case "presence":
		var p presenceBody
		d := xml.NewTokenDecoder(t)
		_ = d.DecodeElement(&p, start)
		switch p.Type {
		case stanza.SubscribePresence:
			c.enqueue(SubscriptionRequestEvent{From: p.From.String()})
		case stanza.SubscribedPresence, stanza.UnsubscribePresence, stanza.UnsubscribedPresence, stanza.ErrorPresence, stanza.ProbePresence:
			// Subscription-management and probe/error presence carry no
			// availability information - reporting them as PresenceEvent
			// would misrepresent the contact as offline.
		default:
			c.enqueue(PresenceEvent{
				From:      p.From.String(),
				Available: p.Type == stanza.AvailablePresence,
				Show:      p.Show,
				Caps:      p.Caps,
			})
		}
	}
}
