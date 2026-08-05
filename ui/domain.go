package ui

import (
	"fmt"
	"strings"
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
	// set: "omemo-v1", "omemo-v2", "omemo" (protocol version unknown - legacy
	// stored message), or "gpg". Empty when Encrypted is false.
	EncMethod string

	// Retracted is set when the sender attempted a XEP-0424 retraction of
	// this message. Content is kept and still shown — we don't trust a
	// remote retraction to erase what was said on our side — but flagged so
	// the attempt is visible.
	Retracted bool

	// Delivered is set once the peer has acknowledged receipt of this message
	// (XEP-0184). Only meaningful when IsMe is true — an outgoing message
	// starts "sent" and flips to "delivered" when the receipt arrives.
	Delivered bool

	// Reactions is the aggregate (XEP-0444) reaction state on this message
	// across everyone who's reacted, one entry per distinct emoji.
	Reactions []Reaction

	// DecryptFailed is set when this message's Content is the
	// "[message could not be decrypted: ...]" placeholder (lost/rotated
	// OMEMO session, etc.) rather than the sender's actual content. Used to
	// exclude it from unread counting: opening the chat will never reveal
	// anything more, so it shouldn't inflate the badge.
	DecryptFailed bool
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

	// Alias is an optional display name from config, shown in place of Name
	// (the bare JID) in the account bar/list; empty means fall back to Name.
	Alias string

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

	// Status is this account's own configured presence: any Presence value
	// (PresenceOnline by default). Distinct from Connecting - an offline
	// account is never dialed at all, so it's never "connecting". Set by
	// AccountStatusSetter and persisted to config so it's restored on the
	// next startup.
	Status Presence
}

// DisplayName is the account's Alias if configured, else its bare JID.
func (a Account) DisplayName() string {
	if a.Alias != "" {
		return a.Alias
	}
	return a.Name
}

// StatusText is a plain-text description of the account's connection state,
// for the account bar/list — connecting/offline/syncing take priority over
// the configured presence since they describe why the account can't be used
// right now, not what presence it will show once it can.
func (a Account) StatusText() string {
	switch {
	case a.Connecting:
		return "connecting…"
	case a.ConnectError != "":
		return "offline"
	case a.SyncingHistory:
		return "syncing history…"
	default:
		return presenceLabel(a.Status)
	}
}

// Presence is a contact's (or, for Account.Status, our own) online status —
// the full RFC 6121 §4.7.2.1 <show/> vocabulary, plus Offline for
// type="unavailable" (a plain presence, absent <show/>, is PresenceOnline).
type Presence int

const (
	PresenceOffline Presence = iota // default: never seen online, or explicitly unavailable
	PresenceDND                     // <show>dnd</show>: do not disturb
	PresenceXA                      // <show>xa</show>: extended away
	PresenceAway                    // <show>away</show>
	PresenceOnline                  // no <show/>: plain available
	PresenceChat                    // <show>chat</show>: actively free to chat
)

type Chat struct {
	Name        string
	Address     string
	LastMessage string
	Presence    Presence
	Typing      bool // true while the peer has an active XEP-0085 "composing" state

	// EncryptionMode is this chat's outgoing message encryption:
	// "omemo-v1" (default), "omemo-v2", "gpg", or "none". Set by
	// ChatEncryptionSetter.
	EncryptionMode string

	// Unread is a local-only count of messages received while this chat
	// wasn't the actively-focused one, persisted via ChatReadTracker. Reset
	// to zero when the chat is opened.
	Unread int
}

func (c Chat) Title() string { return presenceGlyph(c.Presence) + " " + c.Name }
func (c Chat) Description() string {
	text := c.LastMessage
	if text == "" && c.Address != "" && c.Address != c.Name {
		text = c.Address
	}
	if c.Unread <= 0 {
		return text
	}
	prefix := fmt.Sprintf("(%d) ", c.Unread)
	if text == "" {
		return strings.TrimSpace(prefix)
	}
	return prefix + text
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
	confirmQuit
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
