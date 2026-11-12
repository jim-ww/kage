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
	ID     string // stanza ID; enables XEP-0308 correction and XEP-0461 reply targeting
	Author string
	// StoreID is the storage rowid, 0 if not loaded from storage; a paging
	// anchor (see HistoryAnchor).
	StoreID int64
	Content string
	SentAt  time.Time
	IsMe    bool
	// ReplyToID is the ID of the message this replies to (XEP-0461). An ID,
	// not an index: the loaded slice is rebuilt and trimmed.
	ReplyToID   string
	Attachments []string // file paths or URLs attached to the message

	// QuotedAuthor/QuotedPreview are the replied-to message's author and
	// preview as stored with the reply, shown when that message isn't
	// loaded.
	QuotedAuthor  string
	QuotedPreview string

	// LocalID identifies a message we composed before it has an ID, for
	// MessageSendResolvedMsg to find it by.
	LocalID string

	// Pending is set on a message queued to send (ErrQueued), until
	// MessageSendResolvedMsg. Never with Failed.
	Pending bool

	// Failed is set on a message that failed to send; it's kept, marked.
	// Never with Pending.
	Failed bool

	// PendingAttachmentPaths are a Failed attachment send's files, for
	// actionRetryMessage.
	PendingAttachmentPaths []string

	// Encrypted is set when the message was end-to-end encrypted (OMEMO or
	// GPG) on the wire, rather than sent as plaintext.
	Encrypted bool

	// EncMethod names the mechanism that did the encrypting when Encrypted is
	// set: "omemo-v1", "omemo-v2", "omemo" (protocol version unknown - legacy
	// stored message), or "gpg". Empty when Encrypted is false.
	EncMethod string

	// Retracted is set when the sender retracted it (XEP-0424). Content is
	// kept and shown, flagged.
	Retracted bool

	// Edited is set when a XEP-0308 correction replaced the body.
	Edited bool

	// Delivered is set when the peer acknowledged our message (XEP-0184).
	Delivered bool

	// ServerAcked is set once our server confirmed it has our message (see
	// the daemon's trackForServerAck): a send without error only reached the
	// local socket. Shown as "✓", Delivered as "✓✓".
	ServerAcked bool

	// Reactions is the aggregate (XEP-0444) reaction state on this message
	// across everyone who's reacted, one entry per distinct emoji.
	Reactions []Reaction

	// DecryptFailed is set when Content is a "could not be decrypted"
	// placeholder; such messages don't count as unread.
	DecryptFailed bool

	// CallLog is set when this is a finished call's log entry.
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

	// HistoryMore marks, per chat index, whether storage has older messages
	// than Messages, HistoryNewer newer ones. Only history window loads set
	// them.
	HistoryMore  map[int]bool
	HistoryNewer map[int]bool

	// Connecting is set until the account is online or failed to connect.
	Connecting bool
	// ConnectError is why the account failed to connect.
	ConnectError string

	// SyncingHistory is set while the archive is backfilled (XEP-0313).
	SyncingHistory bool

	// Status is the account's own chosen presence. An offline one is never
	// dialed, so never Connecting.
	Status Presence

	// SupportsInvisible is set once the server is known to offer invisible
	// presence (XEP-0186).
	SupportsInvisible bool

	// Removed is set once the account is removed; it stays listed until
	// restart, as indices can't shift (see AccountRemovedMsg).
	Removed bool
}

// DisplayName is the account's Alias if configured, else its bare JID.
func (a Account) DisplayName() string {
	if a.Alias != "" {
		return a.Alias
	}
	return a.Name
}

// NameWithJID is DisplayName plus the account's JID when an alias hides it -
// for the places there's room to say which account an alias stands for
// (the accounts list, the account menu's title).
func (a Account) NameWithJID() string {
	if a.Alias == "" || a.Alias == a.Name {
		return a.Name
	}
	return a.Alias + " · " + a.Name
}

// StatusText describes the account's state; connecting, offline and syncing
// before its presence.
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

// Presence values, in ascending "how available" order, but for
// PresenceInvisible.
const (
	PresenceOffline Presence = iota // default: never seen online, or explicitly unavailable
	PresenceDND                     // <show>dnd</show>: do not disturb
	PresenceXA                      // <show>xa</show>: extended away
	PresenceAway                    // <show>away</show>
	PresenceOnline                  // no <show/>: plain available
	PresenceChat                    // <show>chat</show>: actively free to chat

	// PresenceInvisible is our own status only (XEP-0186): we stay fully
	// connected but the server withholds our presence from contacts, who see
	// us as offline. Only offered when Account.SupportsInvisible is true.
	PresenceInvisible
)

// resourceDisplayName is a resource's leading letters, the client name
// ("Conversations.aB3dE9kL", "kage-aB3d"), or a generic label for a resource
// that's all generated ID.
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

// ResourcePresence is one online device (resource) of a contact.
type ResourcePresence struct {
	Resource string
	Presence Presence

	// Name is the client name it advertises (XEP-0030), "" until known
	// (see DeviceNameMsg).
	Name string
}

// Chat is one roster entry / conversation.
type Chat struct {
	Name        string
	Address     string
	LastMessage string

	// LastActivity is the last message's time; the chat list sorts by it.
	LastActivity time.Time

	Presence Presence
	Typing   bool // true while the peer has an active XEP-0085 "composing" state

	// Subscription is the roster's RFC 6121 presence subscription with this
	// contact: "both", "to" (we see their presence, they don't see ours),
	// "from" (the reverse) or "none"; "" when unknown (no roster entry).
	// SubscriptionPending is true while a request we sent awaits their
	// approval.
	Subscription        string
	SubscriptionPending bool

	// Muted marks a chat whose messages raise no notification. They still
	// count as unread.
	Muted bool

	// Resources are the contact's online devices, sorted by resource.
	Resources []ResourcePresence

	// EncryptionMode is the chat's outgoing encryption: "omemo-v1",
	// "omemo-v2", "gpg" or "none"; "" for the default.
	EncryptionMode string

	// Unread counts messages that arrived while nobody looked at the chat.
	Unread int

	// Hidden marks a chat hidden from the list, locally: nothing else
	// changes. The TUI keeps hidden chats aside (see Model.hiddenChats).
	Hidden bool

	// Pinned marks a chat pinned to the top of the list.
	Pinned bool

	// Draft is the chat's unsent text.
	Draft string
}

// Title implements list.Item. The presence dot and the contact-tinted name
// are both pre-styled strings ending in their own ANSI reset, so nothing
// the delegate layers on top survives into them — see renderTintedName.
func (c Chat) Title() string {
	title := presenceGlyph(c.Presence) + " " + renderTintedName(c.Name, c.Address)
	if c.Muted {
		title += " " + mutedChatGlyph
	}
	if c.Pinned {
		title = pinnedChatGlyph + " " + title
	}
	return title
}

// Description implements list.Item.
func (c Chat) Description() string {
	text := c.LastMessage
	if nl := strings.IndexByte(text, '\n'); nl >= 0 {
		text = text[:nl]
	}
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

// withResource is c with resource set to presence in Resources, removed if
// offline. Must match the daemon's withResource.
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

// aggregatePresence is a contact's most available resource's presence, so
// one device going offline doesn't hide another that's online.
func aggregatePresence(resources []ResourcePresence) Presence {
	best := PresenceOffline
	for _, r := range resources {
		if r.Presence > best {
			best = r.Presence
		}
	}
	return best
}

// withResourceName names one of c's online resources (see DeviceNameMsg).
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

// SendOptions is what Daemon.Send sends besides a plain message.
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

	// RetractID, if set, retracts that message instead (XEP-0424).
	RetractID string

	// ReactionTargetID, if set, sends Reactions as our whole reaction set on
	// that message instead (XEP-0444); empty clears it.
	ReactionTargetID string
	Reactions        []string

	// OOBURLs marks these URLs, also in the body, as files (XEP-0066), not
	// links.
	OOBURLs []string

	// LocalID is a new message's Message.LocalID, carried through a queued
	// send for MessageSendResolvedMsg.
	LocalID string

	// SupersedesID is the ID of the stored, failed message a retry resends;
	// once the retry is sent, that message is deleted.
	SupersedesID string
}
