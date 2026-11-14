package xmpp

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"mellium.im/xmpp/stanza"
)

// messageBody is a <message/> stanza carrying a <body/> and the optional
// XEP-0308 (correction) / XEP-0461 (reply) elements, used both for sending
// and for decoding incoming stanzas.
type messageBody struct {
	stanza.Message
	Error       *stanza.Error         `xml:"jabber:client error"`
	Body        string                `xml:"body,omitempty"`
	Replace     *replaceElem          `xml:"urn:xmpp:message-correct:0 replace"`
	Reply       *replyElem            `xml:"urn:xmpp:reply:0 reply"`
	Retract     *retractElem          `xml:"urn:xmpp:message-retract:1 retract"`
	Reactions   *reactionsElem        `xml:"urn:xmpp:reactions:0 reactions"`
	Fallback    *fallbackElem         `xml:"urn:xmpp:fallback:0 fallback"`
	Encrypted   *OmemoEncryptedElem   `xml:"urn:xmpp:omemo:2 encrypted"`
	EncryptedV1 *OmemoEncryptedElemV1 `xml:"eu.siacs.conversations.axolotl encrypted"`
	MAMResult   *mamResultElem        `xml:"urn:xmpp:mam:2 result"`
	PubsubEvent *pubsubEventElem      `xml:"http://jabber.org/protocol/pubsub#event event"`

	// OriginID/StanzaID are XEP-0359 - see their type docs.
	OriginID *originIDElem `xml:"urn:xmpp:sid:0 origin-id"`
	StanzaID *stanzaIDElem `xml:"urn:xmpp:sid:0 stanza-id"`

	// OOB marks URLs as files rather than links (XEP-0066).
	OOB []oobElem `xml:"jabber:x:oob x"`

	// Store asks servers to archive a message they might not otherwise,
	// having no <body/> or only a fallback one (XEP-0334).
	Store *struct{} `xml:"urn:xmpp:hints store"`

	// XEP-0353 call ringing, received; at most one set. Sent with jmiMessage.
	JMIPropose *jmiProposeElem `xml:"urn:xmpp:jingle-message:0 propose"`
	JMIRinging *jmiIDElem      `xml:"urn:xmpp:jingle-message:0 ringing"`
	JMIProceed *jmiIDElem      `xml:"urn:xmpp:jingle-message:0 proceed"`
	JMIReject  *jmiIDElem      `xml:"urn:xmpp:jingle-message:0 reject"`
	JMIAccept  *jmiIDElem      `xml:"urn:xmpp:jingle-message:0 accept"`
	JMIRetract *jmiIDElem      `xml:"urn:xmpp:jingle-message:0 retract"`

	// XEP-0184 message delivery receipts: Request marks an outgoing message
	// as wanting a receipt; Received is the receipt itself, naming the id of
	// the message it acknowledges.
	Request  *struct{}    `xml:"urn:xmpp:receipts request"`
	Received *receiptElem `xml:"urn:xmpp:receipts received"`

	// Carbons (XEP-0280), at most one set; handleStanza decodes the message
	// inside.
	CarbonReceived *carbonElem `xml:"urn:xmpp:carbons:2 received"`
	CarbonSent     *carbonElem `xml:"urn:xmpp:carbons:2 sent"`

	// XEP-0085 chat state, at most one set: encoding/xml matches fixed names.
	Active    *struct{} `xml:"http://jabber.org/protocol/chatstates active"`
	Composing *struct{} `xml:"http://jabber.org/protocol/chatstates composing"`
	Paused    *struct{} `xml:"http://jabber.org/protocol/chatstates paused"`
	Inactive  *struct{} `xml:"http://jabber.org/protocol/chatstates inactive"`
	Gone      *struct{} `xml:"http://jabber.org/protocol/chatstates gone"`
}

// ChatState is a XEP-0085 chat state notification value.
type ChatState int

// ChatState values, per XEP-0085.
const (
	ChatStateActive ChatState = iota
	ChatStateComposing
	ChatStatePaused
	ChatStateInactive
	ChatStateGone
)

// selfID is the ID replies and corrections refer to the message by: its
// origin-id (XEP-0359), stable across carbons and archives, else its id.
func (m messageBody) selfID() string {
	if m.OriginID != nil && m.OriginID.ID != "" {
		return m.OriginID.ID
	}
	return m.ID
}

// chatState reports the chat state carried by msg, if any.
func (m messageBody) chatState() (ChatState, bool) {
	switch {
	case m.Composing != nil:
		return ChatStateComposing, true
	case m.Paused != nil:
		return ChatStatePaused, true
	case m.Inactive != nil:
		return ChatStateInactive, true
	case m.Gone != nil:
		return ChatStateGone, true
	case m.Active != nil:
		return ChatStateActive, true
	default:
		return ChatStateActive, false
	}
}

// bounceReason renders a bounced message's error for a person: its
// condition, and the server's text explaining it when there is one.
func bounceReason(e *stanza.Error) string {
	if e == nil || e.Condition == "" {
		return "undeliverable"
	}
	reason := strings.ReplaceAll(string(e.Condition), "-", " ")
	for _, text := range e.Text {
		if text != "" {
			return reason + ": " + text
		}
	}
	return reason
}

// setChatState sets the one pointer field on m corresponding to state.
func (m *messageBody) setChatState(state ChatState) {
	switch state {
	case ChatStateComposing:
		m.Composing = &struct{}{}
	case ChatStatePaused:
		m.Paused = &struct{}{}
	case ChatStateInactive:
		m.Inactive = &struct{}{}
	case ChatStateGone:
		m.Gone = &struct{}{}
	default:
		m.Active = &struct{}{}
	}
}

// carbonElem is XEP-0280's <received/>/<sent/> wrapper: a <forwarded/>
// (XEP-0297) holding the original <message/> another of our resources
// sent or received.
type carbonElem struct {
	Forwarded struct {
		Message messageBody `xml:"jabber:client message"`
	} `xml:"urn:xmpp:forward:0 forwarded"`
}

// pubsubEventElem is a PEP push (XEP-0060) for the nodes our caps subscribe
// to (see disco.go). Only the node is decoded: it's a cue to re-fetch.
type pubsubEventElem struct {
	Items *struct {
		Node string `xml:"node,attr"`
	} `xml:"http://jabber.org/protocol/pubsub#event items"`
}

// reactionsElem is the sender's whole reaction set on message ID
// (XEP-0444), replacing the previous; empty clears it.
type reactionsElem struct {
	ID        string   `xml:"id,attr"`
	Reactions []string `xml:"urn:xmpp:reactions:0 reaction"`
}

// retractElem is XEP-0424: this message retracts an earlier one with this ID.
type retractElem struct {
	ID string `xml:"id,attr"`
}

// oobElem is one XEP-0066 <x xmlns='jabber:x:oob'> element.
type oobElem struct {
	URL string `xml:"url"`
}

// receiptElem is XEP-0184: acknowledges receipt of the message with this ID.
type receiptElem struct {
	ID string `xml:"id,attr"`
}

// originIDElem is XEP-0359's origin-id, which unlike id survives relaying.
// Ours equals the stanza id (see Send).
type originIDElem struct {
	ID string `xml:"id,attr"`
}

// stanzaIDElem is XEP-0359's stanza-id, assigned by By. Unused.
type stanzaIDElem struct {
	ID string `xml:"id,attr"`
	By string `xml:"by,attr"`
}

// replaceElem is XEP-0308: this message corrects an earlier one with this ID.
type replaceElem struct {
	ID string `xml:"id,attr"`
}

// replyElem is XEP-0461: this message is a reply to the message with this ID,
// sent to/from To (mirrors the enclosing message's "to", the only JID that
// makes sense in a 1:1 conversation).
type replyElem struct {
	To string `xml:"to,attr"`
	ID string `xml:"id,attr"`
}

// fallbackElem marks the body's fallback text, for replies (XEP-0461) and
// retractions (XEP-0424). Body is a pointer: an empty <body/> (all of it)
// differs from none.
type fallbackElem struct {
	For  string            `xml:"for,attr"`
	Body *fallbackBodyElem `xml:"urn:xmpp:fallback:0 body"`
}

// Start/End are pointers because XEP-0461 treats them as optional, each
// defaulting to "start of body"/"end of body" when absent — not to zero.
type fallbackBodyElem struct {
	Start *int `xml:"start,attr,omitempty"`
	End   *int `xml:"end,attr,omitempty"`
}

// presenceBody is a <presence/> stanza carrying an optional <show/> and
// XEP-0115 entity capabilities.
type presenceBody struct {
	stanza.Presence
	Show string    `xml:"show"`
	Caps *capsElem `xml:"http://jabber.org/protocol/caps c"`
}

// capsElem is XEP-0115's <c/> entity capabilities element.
type capsElem struct {
	Hash string `xml:"hash,attr"`
	Node string `xml:"node,attr"`
	Ver  string `xml:"ver,attr"`
}

// SendOptions carries optional XEP-0308/XEP-0461 wire metadata for Send.
type SendOptions struct {
	// ReplaceID, if set, marks this message as a correction (XEP-0308) of
	// the earlier message with this ID.
	ReplaceID string

	// ReplyToID, if set, marks this message as a reply (XEP-0461) to the
	// message with this ID. QuotedAuthor/QuotedBody build the quoted-text
	// fallback for clients that don't understand XEP-0461.
	ReplyToID    string
	QuotedAuthor string
	QuotedBody   string

	// RetractID, if set, sends a XEP-0424 retraction of the earlier message
	// with this ID instead of a normal message. Mutually exclusive with the
	// other options above.
	RetractID string

	// ReactionTargetID, if set, sends Reactions as our whole set on that
	// message instead (XEP-0444).
	ReactionTargetID string
	Reactions        []string

	// Encrypted, if set, sends a XEP-0384 <encrypted/> element instead of a
	// plaintext body. Mutually exclusive with the other options above and
	// with EncryptedV1.
	Encrypted *OmemoEncryptedElem

	// EncryptedV1, if set, sends a legacy (eu.siacs.conversations.axolotl)
	// <encrypted/> element instead of a plaintext body. Mutually exclusive
	// with the other options above and with Encrypted.
	EncryptedV1 *OmemoEncryptedElemV1

	// OOBURLs marks URLs as files (XEP-0066). Never with an encrypted body,
	// beside which they'd leak.
	OOBURLs []string
}

const retractFallbackBody = "This person attempted to retract a previous message, but it's unsupported by your client."

// BuildFallbackQuote renders a reply's fallback quote (XEP-0461): "> "
// lines, the first naming the author.
func BuildFallbackQuote(author, body string) string {
	lines := strings.Split(body, "\n")
	if len(lines) > 0 {
		lines[0] = author + ": " + lines[0]
	}
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n") + "\n"
}

// stripReplyFallback removes the reply quote fb marks from body. Offsets
// count code points, not bytes (XEP-0426). body is returned unchanged if fb
// marks no valid range.
func stripReplyFallback(body string, fb *fallbackElem) string {
	if fb == nil || fb.For != "urn:xmpp:reply:0" || fb.Body == nil {
		return body
	}
	start := 0
	if fb.Body.Start != nil {
		start = *fb.Body.Start
	}
	end := utf8.RuneCountInString(body)
	if fb.Body.End != nil {
		end = *fb.Body.End
	}
	if end < start {
		return body
	}
	startByte, endByte := runeOffsetToByte(body, start), runeOffsetToByte(body, end)
	if startByte < 0 || endByte < 0 {
		return body
	}
	return body[:startByte] + body[endByte:]
}

// runeOffsetToByte converts a Unicode code point offset into the byte index
// it refers to in s, or -1 if off is negative or points past s's end.
func runeOffsetToByte(s string, off int) int {
	if off < 0 {
		return -1
	}
	n := 0
	for i := range s {
		if n == off {
			return i
		}
		n++
	}
	if n == off {
		return len(s)
	}
	return -1
}

// randomID generates a random stanza ID (128 bits, hex-encoded).
func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing means the system RNG is broken; a predictable
		// fallback is still better than crashing message sends over it.
		return fmt.Sprintf("kage-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
