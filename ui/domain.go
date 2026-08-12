package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/list"
)

// Message is one chat message in an Account's timeline.
type Message struct {
	ID          string // stanza ID; enables XEP-0308 correction and XEP-0461 reply targeting
	Author      string
	Content     string
	SentAt      time.Time
	IsMe        bool
	ReplyTo     *int     // index into the message slice; nil = not a reply
	Attachments []string // file paths or URLs attached to the message

	// LocalID is a client-generated correlation key set on a message the
	// moment it's composed (sendCurrentInput), before the network even knows
	// about it - ID stays empty until (if ever) a real send actually
	// succeeds. Used to find and patch this exact placeholder row again once
	// MessageSendResolvedMsg reports what really happened, since ID can't be
	// used for that lookup yet. Empty for anything that wasn't optimistically
	// echoed this way (incoming messages, history loaded from storage).
	LocalID string

	// Pending is set on a message that's been queued for later delivery
	// (MessageSender.Send returned ErrQueued, e.g. the account is offline)
	// rather than actually sent yet - see MessageSendResolvedMsg, which
	// clears it once the queued send is actually attempted. Never true at
	// the same time as Failed.
	Pending bool

	// Failed is set on a message MessageSender.Send (or its later queued
	// retry) reported a real error for - kept visible rather than silently
	// dropped, so what the user typed is never lost, but never rendered
	// indistinguishably from an actually-delivered message. Never true at
	// the same time as Pending.
	Failed bool

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

	// Edited is set when this message's body was overwritten by a XEP-0308
	// correction (ours or the peer's), rather than being the originally sent
	// content.
	Edited bool

	// Delivered is set once the peer has acknowledged receipt of this message
	// (XEP-0184). Only meaningful when IsMe is true — an outgoing message
	// starts "sent" and flips to "delivered" when the receipt arrives.
	Delivered bool

	// ServerAcked is set once our own server has confirmed (via a debounced
	// XEP-0199 ping round trip - see account.go's
	// trackForServerAck/confirmPendingAcks) that it actually received this
	// message, as opposed to the local send call merely returning without an
	// error. Only meaningful when IsMe is true and ID is set. Rendered as a
	// single "✓" (vs. Delivered's "✓✓") - never shown until this is true, so
	// a message the server never actually got (e.g. written into a socket
	// buffer on a connection that was already silently dead) doesn't render
	// identically to one that really went out.
	ServerAcked bool

	// Reactions is the aggregate (XEP-0444) reaction state on this message
	// across everyone who's reacted, one entry per distinct emoji.
	Reactions []Reaction

	// DecryptFailed is set when this message's Content is the
	// "[message could not be decrypted: ...]" placeholder (lost/rotated
	// OMEMO session, etc.) rather than the sender's actual content. Used to
	// exclude it from unread counting: opening the chat will never reveal
	// anything more, so it shouldn't inflate the badge.
	DecryptFailed bool

	// CallLog is set when this row is a call-log entry (a finished voice
	// call recorded into the chat timeline, like a phone app's call
	// history) rather than an ordinary sent/received message. nil for
	// every normal message.
	CallLog *CallLogInfo
}

// CallLogInfo describes a finished voice call recorded into a chat's
// timeline as a Message with CallLog set.
type CallLogInfo struct {
	// Direction is "incoming" or "outgoing".
	Direction string
	// Outcome is "answered", "missed", "declined", or "failed".
	Outcome string
	// Duration is the call's connected duration. Zero unless Outcome is
	// "answered".
	Duration time.Duration
}

// Reaction is one distinct emoji's aggregate state on a message.
type Reaction struct {
	Emoji string
	Count int
	Mine  bool // true if our own account is one of the reactors for Emoji
}

// Account holds one configured account's chats, messages, and connection state.
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

	// PinnedHistory holds, per chat index, the full decrypted history behind
	// a window loaded via a search-result jump (see loadSearchResult) —
	// storage-backed paging only ever loads older messages from the live
	// tail, so a jump into the middle of a long chat's history has no way to
	// page further in either direction from storage alone. Since a search
	// already had to decrypt the whole chat to find matches, PinnedHistory
	// keeps that result around just long enough to slide the loaded window
	// across it locally (see growPinnedWindow); it's removed once the
	// window has grown to touch both of its edges, handing back off to
	// ordinary storage-backed older-history fetches / live-tail appending.
	PinnedHistory map[int][]Message
	// PinnedWindow is [start, end) — Messages[chatIdx]'s current position
	// within PinnedHistory[chatIdx], meaningful only while that entry
	// exists.
	PinnedWindow map[int][2]int

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

	// Removed is set once AccountRemover.RemoveAccount has disconnected this
	// account and dropped it from config.yaml. It stays in m.accounts (and
	// so this slot in the sidebar) for the rest of this run — see
	// AccountRemovedMsg for why indices can't shift — but is excluded from
	// switching and shows as offline/removed until the next restart drops
	// it from config entirely.
	Removed bool
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
	case a.Removed:
		return "removed"
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

// Presence values, in ascending "how available" order.
const (
	PresenceOffline Presence = iota // default: never seen online, or explicitly unavailable
	PresenceDND                     // <show>dnd</show>: do not disturb
	PresenceXA                      // <show>xa</show>: extended away
	PresenceAway                    // <show>away</show>
	PresenceOnline                  // no <show/>: plain available
	PresenceChat                    // <show>chat</show>: actively free to chat
)

// resourceDisplayName turns a raw XMPP resource string into a short,
// human-readable device label. Clients commonly suffix their resource with
// a random per-session ID after a separator — "." (e.g.
// "Conversations.aB3dE9kL"), or "-" (e.g. a server disambiguating a
// requested resource that collided with another session of the same name,
// producing "kage-aB3dE9kLm1n2"). That suffix is never meaningful to a
// human, so everything from the first non-letter character on is dropped,
// keeping just the client name. A resource that's nothing *but* an opaque
// generated ID (no letters-only prefix at all, e.g. a bare hex/random
// string) falls back to a generic label instead of dumping the raw ID in
// the UI.
func resourceDisplayName(resource string) string {
	end := len(resource)
	for i, r := range resource {
		if !unicode.IsLetter(r) {
			end = i
			break
		}
	}
	if end > 0 {
		return resource[:end]
	}
	if len(resource) > 16 {
		return "device"
	}
	return resource
}

// ResourcePresence is one online device (full-JID resource) of a contact,
// with that resource's own <show/> state — distinct from Chat.Presence,
// which is just the bare-JID roster's last-known presence.
type ResourcePresence struct {
	Resource string
	Presence Presence

	// Name is the human-readable client name this resource advertises via
	// XEP-0030 disco#info (e.g. "kage", "Conversations", "Gajim") - resolved
	// asynchronously after the resource is first seen online (see
	// DeviceNameMsg) since it takes a round trip, so this starts "" and the
	// UI falls back to resourceDisplayName(Resource) until it arrives.
	Name string
}

// Chat is one roster entry / conversation.
type Chat struct {
	Name        string
	Address     string
	LastMessage string
	Presence    Presence
	Typing      bool // true while the peer has an active XEP-0085 "composing" state

	// Resources lists this contact's currently online devices (full-JID
	// resources), sorted by resource name. Populated from live
	// xmpp.PresenceEvents (see PresenceMsg.Resource) — a resource is added
	// on becoming available and removed on an unavailable presence for that
	// same resource. Shown on chat-list row hover (see renderHoverChatRow).
	Resources []ResourcePresence

	// EncryptionMode is this chat's outgoing message encryption:
	// "omemo-v1" (default), "omemo-v2", "gpg", or "none". Set by
	// ChatEncryptionSetter.
	EncryptionMode string

	// Unread is a local-only count of messages received while this chat
	// wasn't the actively-focused one, persisted via ChatReadTracker. Reset
	// to zero when the chat is opened.
	Unread int

	// Draft is the compose box's unsent text last recorded for this chat —
	// loaded from storage when the account connects, kept in sync with
	// m.input as the compose box switches between chats, and persisted via
	// DraftSaver. Empty means no unsent draft.
	Draft string
}

// Title implements list.Item.
func (c Chat) Title() string { return presenceGlyph(c.Presence) + " " + c.Name }

// Description implements list.Item.
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

// FilterValue implements list.Item.
func (c Chat) FilterValue() string { return c.Name }

// withResource returns c with its Resources list updated for one contact
// device: added/updated (kept sorted by resource name) if presence is
// anything but offline, removed if the resource just went unavailable.
// resource == "" (a presence stanza with no resource part) is a no-op —
// nothing to key a device entry by.
func (c Chat) withResource(resource string, presence Presence) Chat {
	if resource == "" {
		return c
	}
	updated := make([]ResourcePresence, 0, len(c.Resources)+1)
	for _, r := range c.Resources {
		if r.Resource != resource {
			updated = append(updated, r)
		}
	}
	if presence != PresenceOffline {
		updated = append(updated, ResourcePresence{Resource: resource, Presence: presence})
		sort.Slice(updated, func(i, j int) bool { return updated[i].Resource < updated[j].Resource })
	}
	c.Resources = updated
	return c
}

// withResourceName sets the disco#info-resolved display name for one of
// c's already-known online resources (see DeviceNameMsg). A no-op if that
// resource isn't (or is no longer) in c.Resources - e.g. the resolution
// arrived after the resource already went offline.
func (c Chat) withResourceName(resource, name string) Chat {
	for i := range c.Resources {
		if c.Resources[i].Resource != resource {
			continue
		}
		updated := make([]ResourcePresence, len(c.Resources))
		copy(updated, c.Resources)
		updated[i].Name = name
		c.Resources = updated
		break
	}
	return c
}

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
	confirmRemoveAccount
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

	// OOBURLs marks each of these URLs (which must also appear in body, one
	// per line) as a file attachment via XEP-0066, so receivers can tell
	// "this is a file" from "the user pasted a link" instead of guessing
	// from the body text alone.
	OOBURLs []string

	// LocalID, for a plain new-message send, is the composing Message's own
	// LocalID - carried through MessageSender.Send (and, if the account is
	// offline, the outbox replay that eventually calls Send again) purely so
	// a later MessageSendResolvedMsg can report back which placeholder
	// message to reconcile. Meaningless (and unused) for
	// reaction/retraction/correction sends, which target an existing
	// message's real ID instead.
	LocalID string
}
