package xmpp

import (
	"encoding/xml"
	"time"

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
}

func (PresenceEvent) isEvent() {}

// ChatStateEvent is an incoming XEP-0085 chat state notification, standalone
// or attached to a regular message.
type ChatStateEvent struct {
	From  string
	State ChatState
}

func (ChatStateEvent) isEvent() {}

// Events returns the channel of incoming events, populated for the lifetime
// of the connection (from Dial until Close). The channel closes once the
// session ends.
func (c *Client) Events() <-chan Event {
	return c.events
}

func (c *Client) handleStanza(t xmlstream.TokenReadEncoder, start *xml.StartElement) {
	events := c.events
	switch start.Name.Local {
	case "message":
		d := xml.NewTokenDecoder(t)
		var msg messageBody
		// DecodeElement legitimately returns a non-nil, non-io.EOF error here
		// (an "unexpected end element" for the message's own closing tag) even
		// on a fully successful decode, because the token stream handed to the
		// handler is already positioned inside the element start passed to us.
		// The decoded value is valid regardless; only bail if we got nothing.
		_ = d.DecodeElement(&msg, start)

		if msg.MAMResult != nil {
			c.dispatchArchiveResult(msg.MAMResult)
			return
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
			events <- MessageEvent{
				ID:        msg.ID,
				From:      msg.From.String(),
				SentAt:    time.Now(),
				Encrypted: msg.Encrypted,
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

		events <- MessageEvent{
			ID:        msg.ID,
			From:      msg.From.String(),
			Body:      body,
			SentAt:    time.Now(),
			ReplaceID: replaceID,
			ReplyToID: replyToID,
		}
	case "presence":
		var p presenceBody
		d := xml.NewTokenDecoder(t)
		_ = d.DecodeElement(&p, start)
		events <- PresenceEvent{
			From:      p.From.String(),
			Available: p.Type == stanza.AvailablePresence,
			Show:      p.Show,
		}
	}
}
