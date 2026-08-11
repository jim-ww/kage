package xmpp

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"mellium.im/xmpp/stanza"
)

// messageBody is a <message/> stanza carrying a <body/> and the optional
// XEP-0308 (correction) / XEP-0461 (reply) elements, used both for sending
// and for decoding incoming stanzas.
type messageBody struct {
	stanza.Message
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

	// OOB is XEP-0066 (Out of Band Data): each element explicitly marks a URL
	// in/alongside the body as a file attachment (e.g. from XEP-0363 HTTP
	// Upload) rather than an ordinary link the user pasted in. A message can
	// carry more than one when several files are shared together.
	OOB []oobElem `xml:"jabber:x:oob x"`

	// XEP-0353 Jingle Message Initiation: the pre-session call signaling that
	// rides on <message/> rather than IQs. At most one is set, same
	// one-field-per-element convention as the chat states below. Sending
	// goes through jmiMessage in jingle.go instead; these exist so incoming
	// ones are decoded by the single pass handleStanza already makes.
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

	// XEP-0280 message carbons: the server wraps a copy of a message another
	// of our resources sent/received in one of these, forwarding it to us.
	// At most one is set. The inner message is decoded raw (not as a nested
	// messageBody) because encoding/xml can't recurse into an unbounded
	// wrapper depth declaratively - handleStanza re-decodes CarbonMessage
	// itself once it knows which of the two fired.
	CarbonReceived *carbonElem `xml:"urn:xmpp:carbons:2 received"`
	CarbonSent     *carbonElem `xml:"urn:xmpp:carbons:2 sent"`

	// XEP-0085 chat state notification: at most one of these is set, on
	// send or receive. Modeled as five separate pointer fields (rather than
	// a single element with a variant name) because encoding/xml matches
	// struct fields to specific, fixed element names — there's no
	// "one-of-these-five-names" tag.
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

// selfID reports the id other clients should use to refer back to this
// message (XEP-0461 reply, XEP-0308 correction, XEP-0424 retraction
// targets) - the XEP-0359 origin-id when present, since it's guaranteed
// stable across carbons/MAM/relays, falling back to the bare stanza id
// attribute otherwise.
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

// pubsubEventElem is a XEP-0060 PEP push notification, delivered as a
// <message/> when a subscribed-to node's items change (e.g. via the
// <feature var="NODE+notify"/> auto-subscribe mechanism our own caps
// advertise - see disco.go). Only the node name is decoded: callers that
// care about a specific node (device-list changes, see events.go) treat
// this purely as a "go re-fetch" trigger rather than trying to parse the
// pushed item payload itself, which keeps this one shape usable for any
// node without per-node payload structs.
type pubsubEventElem struct {
	Items *struct {
		Node string `xml:"node,attr"`
	} `xml:"http://jabber.org/protocol/pubsub#event items"`
}

// reactionsElem is XEP-0444: the complete, current set of reaction emoji
// this sender has applied to the message with this ID. A new <reactions/>
// stanza always fully replaces the sender's previous set — it is never a
// delta — including an empty Reactions slice to mean "I've cleared mine".
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

// originIDElem is XEP-0359: a client-assigned id for this stanza that
// SHOULD survive being forwarded/archived/carbon-copied unmodified, unlike
// the bare stanza 'id' attribute which some intermediaries are free to
// rewrite. This is the value other well-behaved clients prefer when
// correlating a XEP-0461 <reply>/XEP-0308 <replace>/XEP-0424 <retract>
// back to this message, so we set it equal to our own outgoing stanza id
// (see Send) and prefer it over the bare id attribute on receipt (see
// messageBody.selfID).
type originIDElem struct {
	ID string `xml:"id,attr"`
}

// stanzaIDElem is XEP-0359: the id an archiving/relaying entity (e.g. a
// MAM server or MUC), identified by By, assigned this stanza. Decoded for
// completeness but not currently used - our own origin-id already gives
// us a stable id, and MAM's own <result id> (see mam.go's ArchiveID) is
// what we use for archive paging.
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

// fallbackElem declares which part of the body is fallback-only compatibility
// text (XEP-0461 for replies, XEP-0424 for retractions), so fallback-aware
// clients know what to strip/ignore. Body is a pointer so a bodyless
// <body/> (Start/End both nil — the whole message body is fallback, used for
// retractions) round-trips distinctly from omitting the element entirely.
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

	// ReactionTargetID, if set, sends a XEP-0444 reaction-set update instead
	// of a normal message: Reactions becomes the sender's complete, current
	// reaction set on the message with this ID (an empty slice clears it).
	// Mutually exclusive with the other options above.
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

	// OOBURLs marks each of these URLs as a XEP-0066 out-of-band attachment,
	// so receivers can tell "this is a file" from "the user pasted a link"
	// without guessing from the body text. Only meaningful on a plaintext
	// send: adding it alongside Encrypted/EncryptedV1 would leak the URL in
	// the clear next to the ciphertext, so callers must not combine them.
	OOBURLs []string
}

const retractFallbackBody = "This person attempted to retract a previous message, but it's unsupported by your client."

// BuildFallbackQuote renders XEP-0461's suggested quoted-text fallback:
// each line of the quoted message prefixed with "> ", first line labeled
// with the author, ending in a newline so it reads naturally before the
// real reply text that follows it in the body.
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
