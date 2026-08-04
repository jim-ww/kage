package ui

import (
	"time"

	"charm.land/bubbles/v2/list"
)

type Message struct {
	ID          string // stanza ID; enables XEP-0308 correction and XEP-0461 reply targeting
	Author      string
	Content     string
	SentAt      time.Time
	IsMe        bool
	ReplyTo     *int     // index into the message slice; nil = not a reply
	Attachments []string // file paths or URLs attached to the message

	// Encrypted is set when the message was end-to-end encrypted (OMEMO or
	// GPG) on the wire, rather than sent as plaintext.
	Encrypted bool

	// EncMethod names the mechanism that did the encrypting when Encrypted is
	// set: "omemo" or "gpg". Empty when Encrypted is false.
	EncMethod string

	// Retracted is set when the sender attempted a XEP-0424 retraction of
	// this message. Content is kept and still shown — we don't trust a
	// remote retraction to erase what was said on our side — but flagged so
	// the attempt is visible.
	Retracted bool

	// Reactions is the aggregate (XEP-0444) reaction state on this message
	// across everyone who's reacted, one entry per distinct emoji.
	Reactions []Reaction
}

// Reaction is one distinct emoji's aggregate state on a message.
type Reaction struct {
	Emoji string
	Count int
	Mine  bool // true if our own account is one of the reactors for Emoji
}

type Account struct {
	Name     string
	Chats    []list.Item
	Messages map[int][]Message

	// HistoryMore marks, per chat index, whether older messages exist in
	// storage beyond what's currently loaded in Messages — set from the
	// initial paginated load and updated after each "load older" fetch.
	// Absent (or false) means either the chat has no more history, or
	// nothing is known yet (treated the same: no further fetch attempted).
	HistoryMore map[int]bool

	// Connecting is true from New()/AddAccount until the account's dial,
	// roster fetch, and local history load complete asynchronously in the
	// background — see AccountConnectedMsg/AccountConnectErrorMsg.
	Connecting bool
	// ConnectError is set (and Connecting cleared) if the background connect
	// failed; the account stays in the sidebar so the user can see which one
	// is down rather than it silently vanishing.
	ConnectError string

	// SyncingHistory is true while syncArchive is paging through XEP-0313
	// MAM archives for this account's contacts, after Connecting clears.
	// Purely informational — chats and messages already stream in as pages
	// arrive, this just tells the user why more keep showing up.
	SyncingHistory bool
}

// Presence is a contact's coarse online status.
type Presence int

const (
	PresenceOffline Presence = iota // default: never seen online, or explicitly unavailable
	PresenceAway
	PresenceOnline
)

type Chat struct {
	Name        string
	Address     string
	LastMessage string
	Presence    Presence
	Typing      bool // true while the peer has an active XEP-0085 "composing" state

	// EncryptionMode is this chat's outgoing message encryption: "omemo"
	// (default), "gpg", or "none". Set by ChatEncryptionSetter.
	EncryptionMode string
}

func (c Chat) Title() string { return presenceGlyph(c.Presence) + " " + c.Name }
func (c Chat) Description() string {
	switch {
	case c.LastMessage != "":
		return c.LastMessage
	case c.Address != "" && c.Address != c.Name:
		return c.Address
	default:
		return ""
	}
}
func (c Chat) FilterValue() string { return c.Name }

// ── Focus state ───────────────────────────────────────────────────────────────

type selectedView int

const (
	viewAccounts selectedView = iota
	viewChats
	viewChat // viewport + input
)

type confirmTarget int

const (
	confirmNone confirmTarget = iota
	confirmDeleteMessage
	confirmDeleteChat
)

// SendOptions carries optional wire metadata for MessageSender.Send — kept as
// plain fields here (rather than importing the xmpp package's own type) so
// ui stays decoupled from the network layer; the adapter implementing
// MessageSender translates these into XEP-0308/XEP-0461 elements.
type SendOptions struct {
	// ReplaceID, if set, marks this send as a correction of the earlier
	// message with this ID (XEP-0308).
	ReplaceID string

	// ReplyToID, if set, marks this send as a reply to the message with this
	// ID (XEP-0461). QuotedAuthor/QuotedBody let the sender build the
	// quoted-text fallback for reply-unaware clients.
	ReplyToID    string
	QuotedAuthor string
	QuotedBody   string

	// RetractID, if set, sends a XEP-0424 retraction of the earlier message
	// with this ID instead of a normal message. Mutually exclusive with the
	// other options above.
	RetractID string

	// ReactionTargetID, if set, sends a XEP-0444 reaction-set update instead
	// of a normal message: Reactions becomes our complete, current reaction
	// set on the message with this ID (an empty-but-non-nil slice clears
	// it). Mutually exclusive with the other options above.
	ReactionTargetID string
	Reactions        []string
}
