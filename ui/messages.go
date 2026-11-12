package ui

import (
	"errors"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

// ErrQueued is the sentinel Send returns when the account is offline and the
// message was queued for later delivery instead of actually sent -
// distinguishing "not sent yet, but will be" from both success and a real
// failure, so the caller can mark the message Pending rather than either
// showing it as delivered (a lie) or discarding it/showing "send failed" (it
// wasn't a failure). See Message.Pending and MessageSendResolvedMsg, which
// reports what eventually happens to a queued send.
var ErrQueued = errors.New("account offline; message queued for delivery")

// uploadCanceledMsg is the exact text adapter.errUploadCanceled formats to
// (see kage's adapter.go) - errors cross the daemon/TUI IPC boundary as
// plain strings, so this is how FileSendResultMsg/FileUploadResultMsg.Err
// is recognized as "the user canceled this" rather than a real failure, to
// show a quieter notification for it. Matched by exact string rather than
// errors.Is since the RPC layer doesn't preserve error identity.
const uploadCanceledMsg = "upload canceled"

// HistoryAnchor pins a Daemon.LoadHistoryWindow request to a specific
// message — Delay/StoreID are exactly Message.SentAt.Unix()/Message.StoreID
// of whichever currently-loaded message triggered the fetch (the one at the
// edge of the window that was reached). A nil anchor means "no message to
// anchor on, give me the chat's most recent window" — the initial chat open,
// or jumping to the true latest message.
type HistoryAnchor struct {
	Delay   int64
	StoreID int64
}

// HistoryWindowMsg reports the result of Daemon.LoadHistoryWindow:
// Messages (chronological order) entirely replaces whatever was loaded for
// the chat matching AccountIdx/From — this is a fresh window, not a page to
// merge with the old one. HasOlder/HasNewer report whether more history
// exists in storage beyond either edge of Messages.
type HistoryWindowMsg struct {
	AccountIdx int
	From       string
	Messages   []Message
	HasOlder   bool
	HasNewer   bool

	// Err is set when the fetch failed, in which case every other field
	// carries no information. It must be distinguishable from a successful
	// empty window: a failure used to be reported as Messages=nil with
	// HasOlder/HasNewer false, which replaced the chat's loaded history with
	// nothing AND declared storage exhausted in both directions - so the
	// chat went blank and could never be paged again for the rest of the
	// session.
	Err error `json:"-"`
}

// HistorySearchResultMsg reports the result of Daemon.SearchHistory.
// Messages is the chat's entire persisted history, already decrypted and in
// chronological order, and Matches indexes into it for every message whose
// content contains Query (case-insensitive) — used only to list/preview
// matches in the results popup. Selecting one fires a fresh
// Daemon.LoadHistoryWindow anchored on that message rather than using
// Messages/Matches directly, so the loaded window stays consistent with how
// every other window load works (and paging up/down from it afterward is
// ordinary storage-backed paging, not a special in-memory case).
type HistorySearchResultMsg struct {
	AccountIdx int
	From       string
	Query      string
	Messages   []Message
	Matches    []int
	Err        error
}

// ContactAddedMsg reports the result of Daemon.AddContact.
type ContactAddedMsg struct {
	AccountIdx int
	Address    string
	Err        error
}

// ContactRemovedMsg reports the result of Daemon.RemoveContact.
type ContactRemovedMsg struct {
	AccountIdx int
	Address    string
	Err        error
}

// ChatAddedMsg is broadcast by the daemon when a chat comes into existence
// (or changes its roster nickname) while clients are already attached:
// someone not in the roster writes to us for the first time, another of our
// own clients adds a contact, or our server auto-creates a roster item
// because we approved an inbound subscription request. It is an upsert - a
// chat already in the list has its Name refreshed rather than being
// duplicated.
//
// Without this, such a chat is only ever discovered by the initial
// listAccounts snapshot, so it stays invisible until the app is restarted
// even though the daemon has already stored its messages.
type ChatAddedMsg struct {
	AccountIdx int
	Chat       Chat

	// Messages is the chat's history as the daemon has it, so a chat
	// created by an incoming message arrives with that message already in
	// it rather than empty.
	Messages []Message
}

// ChatRemovedMsg is broadcast by the daemon when a roster item is deleted
// somewhere else (another client, or the server) - the counterpart to
// ChatAddedMsg. Local history is untouched; only the list row goes away.
type ChatRemovedMsg struct {
	AccountIdx int
	Address    string
}

// ContactResubscribedMsg reports the result of Daemon.ResubscribeContact.
type ContactResubscribedMsg struct {
	AccountIdx int
	Address    string
	Err        error
}

// FileTransferProgressMsg reports incremental progress for one file upload
// or download, keyed by ID - an opaque per-transfer identifier chosen by the
// sender (the local path for an upload, the attachment URL for a download)
// so the UI can track several concurrent transfers separately. Label is the
// human-readable line prefix (e.g. "uploading photo.jpg"). Total is 0 when
// not yet known (e.g. a download before the response headers arrive).
type FileTransferProgressMsg struct {
	ID    string
	Label string
	Sent  int64
	Total int64
}

// FileTransferDoneMsg reports that the transfer keyed by ID (see
// FileTransferProgressMsg) reached a terminal outcome (success or failure)
// on whichever attached client actually drove it - broadcast to every
// attached client so one that only ever saw FileTransferProgressMsg
// broadcasts (i.e. every client except the one that initiated the transfer,
// which learns this instead from its own direct RPC return) still clears the
// progress line instead of leaving it stuck at its last reported percentage.
type FileTransferDoneMsg struct {
	ID string
}

// FileSendResultMsg reports completion of an asynchronous upload and send.
type FileSendResultMsg struct {
	AccountIdx int
	To         string
	Path       string
	URL        string
	ID         string
	ReplyToID  string // non-empty if this upload was sent in reply to a message
	Err        error
}

// FileUploadResultMsg reports completion of Daemon.UploadFile — a file
// staged as a pending attachment, not yet sent as a message. AttachID
// correlates it back to the pendingAttachment it was started for, since the
// same path can be staged more than once.
type FileUploadResultMsg struct {
	AttachID int
	Path     string
	URL      string
	Queued   bool // true: account was offline, upload+send was queued instead of attempted - URL/Err are both zero
	Err      error
}

// ComposedSendResultMsg reports completion of startAttachedSend: uploading
// every staged attachment and sending each as its own single-attachment
// message (see startAttachedSend for why - multi-attachment-per-message
// isn't reliably understood by other clients), all sharing ReplyToID.
// Messages holds every one that made it out before Err (if any) aborted the
// rest - a failure partway through still leaves whatever already sent
// reflected in the chat, matching what's actually on the wire. On total
// failure (nothing sent) nothing is added, same tradeoff FileSendResultMsg
// already makes rather than trying to restore compose state after an async
// round trip.
type ComposedSendResultMsg struct {
	AccountIdx int
	To         string
	ReplyToID  string   // non-empty if this was sent in reply to a message
	Paths      []string // every staged local path this batch attempted, for clearing their transfer-progress entries regardless of how far the batch got
	Messages   []SentMessage
	Queued     bool // true: account was offline, every not-yet-sent file in this batch was queued rather than attempted

	// QueuedLocalID/QueuedPath are set alongside Queued: the file that got
	// queued, so the handler can show a local Pending placeholder for it —
	// the same local-echo treatment a plain-text queued send already gets
	// in sendCurrentInput — instead of just a toast. QueuedLocalID also
	// carries through to the outbox row (Daemon.UploadFile passes it
	// on), so adapter.flushOutbox can resolve this exact placeholder by
	// LocalID once the queued upload+send actually happens.
	QueuedLocalID string
	QueuedPath    string

	// RetryOfLocalID is set when this batch is actionRetryMessage retrying an
	// existing Failed attachment placeholder rather than a fresh send: the
	// first outcome (success or a new failure) updates that placeholder by
	// LocalID in place instead of appending a new message; any further files
	// in the batch still append as usual.
	RetryOfLocalID string

	// FailedLocalID/FailedPaths mirror QueuedLocalID/QueuedPath for a
	// genuine failure (Err set, Queued false): the local file path(s) that
	// never made it out - including whichever one actually failed, plus any
	// after it in the batch that were never attempted - so the handler can
	// leave behind a Failed, retryable placeholder
	// (Message.PendingAttachmentPaths) instead of only a toast.
	FailedLocalID string
	FailedPaths   []string

	Err error
}

// SentMessage is one message startAttachedSend successfully sent.
type SentMessage struct {
	ID          string
	LocalID     string // set when this was startAttachedSend's first message in an actionRetryMessage retry - see ComposedSendResultMsg.RetryOfLocalID
	Content     string
	Attachments []string
}

// IncomingMessageMsg is sent into the Bubble Tea loop when a message arrives
// from the network for one of the configured accounts.
type IncomingMessageMsg struct {
	AccountIdx int
	From       string // bare/full JID the message came from
	ReplyToID  string // non-empty if this message is a XEP-0461 reply
	Message    Message
}

// MessageCorrectedMsg is sent into the Bubble Tea loop when a XEP-0308
// correction arrives for a message already shown for one of the configured
// accounts.
type MessageCorrectedMsg struct {
	AccountIdx int
	From       string // bare JID (chat)
	ReplaceID  string // ID of the message being corrected
	NewContent string
	Encrypted  bool   // whether the correction itself was e2e encrypted on the wire
	EncMethod  string // "omemo-v1", "omemo-v2", "omemo", or "gpg", matching Message.EncMethod; empty when Encrypted is false
}

// MessageRetractedMsg is sent into the Bubble Tea loop when the other party
// attempts to retract (XEP-0424) a message already shown for one of the
// configured accounts. The message is flagged, never removed — see
// Message.Retracted.
type MessageRetractedMsg struct {
	AccountIdx int
	From       string // bare JID (chat)
	RetractID  string // ID of the message being retracted
}

// MessageSendResolvedMsg is sent into the Bubble Tea loop when a queued
// (Message.Pending) send is actually attempted - i.e. adapter.flushOutbox
// replaying it after the account reconnects. LocalID finds the placeholder
// Message to patch in place (its ID was never known at compose time, so it
// can't be used for this lookup the way MessageCorrectedMsg's ReplaceID is).
// Exactly one of ID or Err is meaningful: a successful send clears Pending
// and fills in ID/Encrypted/EncMethod, matching what a normal successful
// Send() call would have set; a failed one clears Pending and sets Failed
// instead, with Err as the notification text - the same two outcomes
// sendCurrentInput handles for a live send, just arriving later.
type MessageSendResolvedMsg struct {
	AccountIdx int
	To         string // bare/full JID this was addressed to (chat key)
	LocalID    string
	ID         string // the real stanza ID, non-empty only on success
	Encrypted  bool
	EncMethod  string
	Err        string // non-empty means the queued send ultimately failed

	// Attachments/Content, when Attachments is non-nil, replace the
	// placeholder's — used only for a resolved staged-attachment send,
	// whose real URL wasn't known until the upload actually happened (a
	// plain-text placeholder already shows its final content from the
	// moment it was composed, so this is left nil there and the placeholder's
	// existing Content/Attachments are kept as-is).
	Content     string
	Attachments []string
}

// OutboxDeletedMsg is sent into the Bubble Tea loop when a still-Pending
// message is permanently discarded (Daemon.DeleteQueued) - by this
// client or, since more than one TUI can attach to the same daemon, another
// one. The Pending placeholder (found by LocalID, same as
// MessageSendResolvedMsg) is removed from the chat outright rather than
// flagged, since it was never actually sent.
type OutboxDeletedMsg struct {
	AccountIdx int
	To         string // bare/full JID this was addressed to (chat key)
	LocalID    string
}

// MessagePurgedMsg is sent into the Bubble Tea loop when an already-retracted
// message is erased from storage outright (Daemon.PurgeMessage) - by
// this client or another TUI attached to the same daemon. Unlike
// MessageRetractedMsg, the message is removed from the chat entirely; there
// is nothing left to show.
type MessagePurgedMsg struct {
	AccountIdx int
	Peer       string // bare JID (chat)
	MessageID  string // ID of the message that was erased
}

// MessageDeliveredMsg is sent into the Bubble Tea loop when a XEP-0184
// delivery receipt arrives for a message we sent.
type MessageDeliveredMsg struct {
	AccountIdx int
	From       string // bare JID (chat)
	MessageID  string // ID of the message the peer acknowledged
}

// MessageServerAckedMsg is sent into the Bubble Tea loop when a debounced
// server-confirmation ping (see account.go's
// trackForServerAck/confirmPendingAcks) succeeds for a message we sent -
// see Message.ServerAcked's doc comment.
type MessageServerAckedMsg struct {
	AccountIdx int
	To         string // bare/full JID this was addressed to (chat key)
	MessageID  string
}

// MessageSendFailedMsg is sent into the Bubble Tea loop when confirmPendingAcks's
// server-confirmation ping times out and the connection is declared dead (see
// account.go) - unlike MessageServerAckedMsg's happy path, this is a real,
// already-sent message (it has an ID, unlike a still-Pending queued send)
// that we now have positive evidence never reached the server. Rendered the
// same as Message.Failed - see its doc comment.
type MessageSendFailedMsg struct {
	AccountIdx int
	To         string // bare/full JID this was addressed to (chat key)
	MessageID  string
	// Reason is set when the recipient's side bounced the message (it was
	// never delivered, even if our server had acked it); empty for a send
	// lost to a dropped connection.
	Reason string
}

// MessageReactionsMsg is sent into the Bubble Tea loop when a XEP-0444
// reaction-set update arrives for a message already shown for one of the
// configured accounts. Reactions is the full recomputed aggregate to
// display, replacing whatever was there before.
type MessageReactionsMsg struct {
	AccountIdx int
	From       string // bare JID (chat)
	MessageID  string // ID of the message being reacted to
	Reactions  []Reaction
}

// PresenceMsg is sent into the Bubble Tea loop when a contact's presence
// changes for one of the configured accounts.
type PresenceMsg struct {
	AccountIdx int
	From       string // bare JID
	Presence   Presence
	Resource   string // resource part of the full JID the presence stanza came from; "" if it had none
}

// ChatSettingMsg reports one chat's setting changed - by any attached TUI,
// this one included (for which it's a no-op, already applied) - so every
// TUI shows the same thing. Setting is "encryption" (Mode carries it),
// "hidden", "pinned" or "muted" (On carries it).
type ChatSettingMsg struct {
	AccountJID  string
	ChatAddress string
	Setting     string
	Mode        string
	On          bool
}

// AccountStatusChangedMsg reports an account's own status changed, by any
// attached TUI - the state half of a status change, applied in every TUI
// alike (AccountStatusSetMsg is only the requesting TUI's result). NewChats
// etc. carry whatever coming back online loaded.
type AccountStatusChangedMsg struct {
	Index          int
	Status         Presence
	NewChats       []list.Item
	NewMessages    map[int][]Message
	NewHistoryMore map[int]bool
}

// AvatarPublishedMsg reports the result of an Daemon call.
// Removed distinguishes the two operations so the notice can say which one
// failed.
type AvatarPublishedMsg struct {
	AccountIdx int
	Removed    bool
	Err        error
}

// AvatarUpdatedMsg reports that a contact's avatar image changed and has
// been cached at Path by the daemon (see XEP-0084 fetching in the daemon's
// avatars.go). The image itself never crosses the socket — the TUI reads
// the file, which is why this carries a path rather than bytes.
//
// The TUI-side handler decodes it into the avatar store before this reaches
// Update, so Update's only job is to let the frame redraw.
type AvatarUpdatedMsg struct {
	AccountIdx int
	JID        string // bare JID
	Path       string
}

// DeviceNameMsg reports a contact resource's disco#info-resolved client
// name (see xmpp.Client.DeviceName), arriving asynchronously sometime after
// the PresenceMsg that first reported that resource online.
type DeviceNameMsg struct {
	AccountIdx int
	From       string // bare JID
	Resource   string
	Name       string
}

// TypingMsg is sent into the Bubble Tea loop when a contact's XEP-0085 chat
// state changes for one of the configured accounts.
type TypingMsg struct {
	AccountIdx int
	From       string // bare JID
	Typing     bool
}

// typingPauseTimeout is how long the compose input can sit idle (no
// keystrokes, but not cleared either) before we tell the peer we've stopped
// composing — matching XEP-0085's convention that "composing" shouldn't
// stick forever just because the draft is still in the box.
const typingPauseTimeout = 5 * time.Second

// typingPauseMsg fires typingPauseTimeout after a keystroke that left the
// compose input non-empty. Gen must still match Model.typingGen when it
// arrives — otherwise a later keystroke already rearmed the timer (or
// stopped composing outright) and this one is stale and ignored.
type typingPauseMsg struct {
	addr string
	gen  int
}

func typingPauseTimer(addr string, gen int) tea.Cmd {
	return tea.Tick(typingPauseTimeout, func(time.Time) tea.Msg {
		return typingPauseMsg{addr: addr, gen: gen}
	})
}

// peerTypingTimeout is how long a contact's "composing" is believed before
// the indicator clears itself. XEP-0085's own advice, and the only defense
// against an indicator that never goes away: the contradicting "active" is
// just another stanza, so a peer that quits mid-keystroke, crashes, or drops
// off the network never sends it and the row would otherwise claim they're
// still typing for the rest of the session. Generous compared to our own
// typingPauseTimeout — other clients pick their own pause interval, and a
// peer that really is still typing re-sends "composing".
const peerTypingTimeout = 30 * time.Second

// peerTypingExpiredMsg fires peerTypingTimeout after a contact's "composing"
// arrived. Gen must still match the chat's peerTypingGen when it lands —
// otherwise a newer "composing" rearmed the timer (or something already
// cleared the indicator) and this one is stale.
type peerTypingExpiredMsg struct {
	accountIdx int
	from       string
	gen        int
}

func peerTypingTimer(accountIdx int, from string, gen int) tea.Cmd {
	return tea.Tick(peerTypingTimeout, func(time.Time) tea.Msg {
		return peerTypingExpiredMsg{accountIdx: accountIdx, from: from, gen: gen}
	})
}

// flashDuration is how long a message stays highlighted after jumping to it
// (e.g. clicking a reply quote) before the highlight clears itself.
const flashDuration = 600 * time.Millisecond

// flashClearMsg fires flashDuration after a message was flashed. Gen must
// still match Model.flashGen when it arrives — otherwise a later flash
// already superseded this one and clearing here would cut it short.
type flashClearMsg struct {
	gen int
}

func flashTimer(gen int) tea.Cmd {
	return tea.Tick(flashDuration, func(time.Time) tea.Msg {
		return flashClearMsg{gen: gen}
	})
}

// chatSwitchDebounce is how long the chat-list selection must sit still
// before the message panel actually re-renders the newly selected chat.
// Rendering a chat's full message list (renderMessagesWithOffsets, up to
// maxMessagesPerChat messages of ansi-wrapping/styling) isn't free, and
// holding the up/down key repeats it once per intermediate row skipped
// through on the way to wherever the key repeat actually lands — visibly
// lagging the list cursor itself, since Update() is synchronous. Short
// enough that a single deliberate move still feels instant.
const chatSwitchDebounce = 40 * time.Millisecond

// chatSwitchSettledMsg fires chatSwitchDebounce after the chat-list
// selection last moved. gen must still match Model.chatSwitchGen when it
// arrives — otherwise the selection moved again since this timer was
// scheduled, and this message is stale.
type chatSwitchSettledMsg struct {
	gen int
}

func chatSwitchTimer(gen int) tea.Cmd {
	return tea.Tick(chatSwitchDebounce, func(time.Time) tea.Msg {
		return chatSwitchSettledMsg{gen: gen}
	})
}

// hoverDevicesDelay is how long the mouse must sit still over a chat-list
// row before that contact's online devices are shown in place of its
// normal description — long enough that a mouse just passing over the list
// on its way elsewhere doesn't flash the device list on every row it
// crosses.
const hoverDevicesDelay = time.Second

// hoverDevicesRevealMsg fires hoverDevicesDelay after a chat row became the
// hovered one. gen must still match Model.hoverGen when it arrives —
// otherwise the hover already moved to a different row (or off the list
// entirely) since this timer was scheduled, and this message is stale.
type hoverDevicesRevealMsg struct {
	id  string
	gen int
}

func hoverDevicesTimer(id string, gen int) tea.Cmd {
	return tea.Tick(hoverDevicesDelay, func(time.Time) tea.Msg {
		return hoverDevicesRevealMsg{id: id, gen: gen}
	})
}

// draftSaveDebounce is how long the compose input can sit idle (mid-typing,
// not yet sent or navigated away from) before the draft is persisted to
// storage — long enough that a fast typist doesn't trigger a write per
// keystroke, short enough that a crash/kill loses at most a few seconds of
// unsaved text. Chat-switch and send/clear also persist immediately,
// independent of this timer (see swapComposeDraft/saveChatDraft call sites);
// this only covers the case where the user just keeps typing in the same
// chat for a while.
const draftSaveDebounce = 3 * time.Second

// draftSaveMsg fires draftSaveDebounce after a keystroke that changed the
// compose input. gen must still match Model.draftSaveGen and addr must still
// be the chat currently loaded into the input when it arrives — otherwise a
// later keystroke already re-armed the timer, or the user switched/sent/
// cleared in the meantime (which already persisted the draft itself), and
// this one is stale and ignored.
type draftSaveMsg struct {
	accountIdx int
	addr       string
	gen        int
}

func draftSaveTimer(accountIdx int, addr string, gen int) tea.Cmd {
	return tea.Tick(draftSaveDebounce, func(time.Time) tea.Msg {
		return draftSaveMsg{accountIdx: accountIdx, addr: addr, gen: gen}
	})
}

// notifyIdleTimeout is how long the UI can sit with no keyboard/mouse
// activity before we tell the daemon the user probably isn't actually
// looking anymore, even if the terminal still has OS focus and a chat is
// still open — see Model.idle.
const notifyIdleTimeout = 10 * time.Minute

// idleMsg fires notifyIdleTimeout after the last detected activity. gen
// must still match Model.idleGen when it arrives — otherwise activity since
// then already rearmed the timer and this one is stale.
type idleMsg struct {
	gen int
}

func idleTimer(gen int) tea.Cmd {
	return tea.Tick(notifyIdleTimeout, func(time.Time) tea.Msg {
		return idleMsg{gen: gen}
	})
}

// AccountAddedMsg is sent into the Bubble Tea loop once Daemon.AddAccount
// has connected the new account and it's ready to show in the sidebar.
type AccountAddedMsg struct {
	Account Account
}

// AccountAppearedMsg is sent into the Bubble Tea loop when an account was
// added by someone else — another TUI attached to the same daemon. Index is
// the account's position daemon-side, which every client has to mirror
// exactly: indices address accounts on every subsequent RPC, so a client that
// ignored this would send that account's traffic from the wrong one as soon as
// it added an account of its own. Carries no add-account-form state, unlike
// AccountAddedMsg — the form belongs to the client that actually did the add.
type AccountAppearedMsg struct {
	Index   int
	Account Account
}

// AccountAddErrorMsg is sent into the Bubble Tea loop when Daemon.AddAccount
// fails; the add-account form stays open so the user can correct and retry.
type AccountAddErrorMsg struct {
	Err error
}

// AccountRemovedMsg is sent into the Bubble Tea loop once
// Daemon.RemoveAccount has disconnected accountIdx and dropped it
// from config.toml. The account stays in the sidebar for the rest of this
// run (indices must stay stable — see AccountConnectedMsg) but shows as
// offline/removed and is excluded from switching; it's gone for good only
// after a restart.
type AccountRemovedMsg struct {
	Index int
}

// AccountRemoveErrorMsg is sent into the Bubble Tea loop when
// Daemon.RemoveAccount fails.
type AccountRemoveErrorMsg struct {
	Index int
	Err   error
}

// AccountsSnapshotMsg delivers the daemon's initial account snapshot after
// the program is already running.
//
// Building that snapshot is a round trip to the daemon, which reads and
// decrypts a page of history per chat and can queue behind the daemon's own
// account connects - so waiting for it before the first frame meant the
// terminal sat blank for however long that took (seconds, against a slow
// server). The UI starts on whatever is known from config alone and takes
// this when it arrives.
type AccountsSnapshotMsg struct {
	Accounts []Account

	// StartAccount is the account to display, from config (default account,
	// or the one holding the last-opened chat).
	StartAccount int

	// CallState is the daemon's current call state, if any was in progress
	// when this client attached.
	CallState *CallStateMsg

	// Err is set instead of Accounts when the snapshot couldn't be fetched
	// at all. Reported rather than dropped: the TUI starts with no accounts
	// (see above), so a silently lost snapshot is indistinguishable from a
	// user with no configured accounts - an empty sidebar and no clue why.
	Err string
}

// AccountConnectedMsg is sent into the Bubble Tea loop once a configured
// account's *local* storage has been opened and its cached roster/history
// loaded from disk — no network involved, so this is fast and lets local
// chats/messages appear instantly instead of waiting on a live connection.
// Index addresses the placeholder Account passed to New() at the same
// position — it never changes, unlike AddAccount's accounts which are always
// appended. The account is still marked Connecting until AccountLiveMsg
// arrives.
type AccountConnectedMsg struct {
	Index   int
	Account Account
}

// AccountLiveMsg is sent into the Bubble Tea loop once a configured account
// has finished dialing and fetching its live roster (in the background,
// after AccountConnectedMsg already showed local data) — clearing
// Connecting. NewChats/NewMessages carry only contacts the local snapshot
// didn't already know about (freshly added on another device); existing
// chats/history are left untouched rather than re-fetched, since they're
// already showing.
type AccountLiveMsg struct {
	Index          int
	NewChats       []list.Item
	NewMessages    map[int][]Message // indices are relative to Chats *after* NewChats is appended
	NewHistoryMore map[int]bool      // same indexing as NewMessages; whether older history exists beyond what's loaded

	// SupportsInvisible carries the XEP-0186 disco result, resolved as part
	// of going live. It rides along here because the snapshot a TUI gets on
	// attach deliberately doesn't wait for that round trip (see
	// daemonServer.listAccounts), so a client that attached while this
	// account was still connecting learns the answer from this message.
	SupportsInvisible bool
}

// AccountConnectErrorMsg is sent into the Bubble Tea loop when a configured
// account's background connect (local load or live dial — see
// AccountConnectedMsg/AccountLiveMsg) fails. The account stays in the
// sidebar, marked with the error, rather than disappearing — any local data
// already shown is left in place.
type AccountConnectErrorMsg struct {
	Index int
	Err   error
}

// HistorySyncedMsg is sent into the Bubble Tea loop with a batch of XEP-0313
// (MAM) archive messages that were missed while offline, backfilled after an
// account finishes connecting. Handled identically to a batch of
// IncomingMessageMsg for the same chat.
type HistorySyncedMsg struct {
	AccountIdx int
	From       string
	Messages   []Message
}

// ChatUnreadMsg carries a chat's current unread count, pushed by the daemon
// whenever it changes: it counts incoming messages for chats no attached TUI
// is looking at, and zeroes a chat when any TUI opens it (ResetChatUnread).
type ChatUnreadMsg struct {
	AccountJID  string
	ChatAddress string
	Count       int
}

// HistorySyncStartedMsg marks an account entering the post-connect MAM
// backfill (see syncArchive) so the UI can show a "syncing…" indicator
// instead of leaving the user guessing why history keeps growing.
type HistorySyncStartedMsg struct {
	AccountIdx int
}

// HistorySyncFinishedMsg marks the end of that backfill, successful or not.
type HistorySyncFinishedMsg struct {
	AccountIdx int
}

// AccountStatusSetMsg reports the result of Daemon.SetAccountStatus.
// NewChats/NewMessages/NewHistoryMore carry any contacts discovered while
// bringing a previously-offline account online, indexed the same way as
// AccountLiveMsg's — empty when no (re)connect was needed.
//
// Through the daemon, the state change itself arrives separately as an
// AccountStatusChangedMsg to every attached TUI, and this only reports
// success or failure to the one that asked (StateApplied false). In process
// (tests, no daemon) it carries the state too, StateApplied true.
type AccountStatusSetMsg struct {
	Index          int
	Status         Presence
	NewChats       []list.Item
	NewMessages    map[int][]Message
	NewHistoryMore map[int]bool
	Err            error
	StateApplied   bool
}

// StoragePasswordChangedMsg reports the result of a Daemon
// call, produced by the "change storage password" popup's submit action.
type StoragePasswordChangedMsg struct {
	Err error
}

// AudioDevice is one microphone or speaker, by the audio backend's name.
type AudioDevice struct {
	Name    string
	Default bool // the system's current default
}

// AudioDevicesMsg answers Daemon.ListAudioDevices: every
// device available, and which input/output calls use ("" for the system
// default).
type AudioDevicesMsg struct {
	Inputs, Outputs []AudioDevice
	Input, Output   string
	Err             error
}

// CallActionResultMsg reports the result of a Daemon method call
// (StartCall/AnswerCall/HangupCall/RejectCall) failing to even reach or be
// accepted by the daemon — the call's actual state transitions arrive
// separately via CallStateMsg/IncomingCallMsg, broadcast from the daemon's
// own call state machine regardless of which side is driving it.
type CallActionResultMsg struct {
	Action     string // "start", "answer", "hangup", "reject" — which method produced this
	AccountIdx int
	Err        error
}

// IncomingCallMsg is sent into the Bubble Tea loop when a peer proposes a
// voice call (XEP-0353) to one of the configured accounts. The daemon has
// already answered <ringing/>; nothing further happens until the UI drives
// Daemon.AnswerCall or RejectCall.
type IncomingCallMsg struct {
	AccountIdx int
	From       string // bare JID
	SID        string
	Media      string // "audio"
}

// CallStateMsg is sent into the Bubble Tea loop on every transition of an
// account's current call (see callState.String in callsession.go for the
// State values: "proposing", "ringing-remote", "ringing-local",
// "negotiating", "connected", "ended", plus "failed" for an error teardown).
// Reason is free text to show alongside a terminal state. Muted/Quality ride
// along on every broadcast (not just ones that changed them), so they're
// always current regardless of what else transitioned.
type CallStateMsg struct {
	AccountIdx int
	Peer       string // bare JID
	SID        string
	State      string
	Reason     string
	Muted      bool
	Quality    string // "", "good", "fair", "poor"
	Sharing    bool   // true while we're actively sending our own video
	// Video is what we're sending ("camera"/"screen", "" for nothing) -
	// already set while VideoPending, i.e. requested but not flowing yet.
	Video        string
	VideoPending bool
	// RemoteVideo is true while the peer streams video to us; ViewerOpen is
	// whether its window is currently up (closed by hand until ReopenVideo).
	RemoteVideo bool
	ViewerOpen  bool
	// DesktopAudio is true while other applications' audio is mixed into
	// what we send; DesktopAudioEnabled is the setting that does it while
	// sharing the screen.
	DesktopAudio        bool
	DesktopAudioEnabled bool
	DesktopAudioVolume  int // percent
	// Notice is a one-off message to surface as a toast (e.g. a camera that
	// failed to open) - not call state.
	Notice string
	// StartedAt is normally left zero - handleCallStateMsg fills it in the
	// moment State first becomes "connected". A daemon-provided sync (a TUI
	// attaching to a call already in progress) sets it explicitly so the
	// duration displayed doesn't reset to 00:00.
	StartedAt time.Time
	// SAS is the short authentication string derived from both ends' DTLS
	// fingerprints - "" until negotiation completes. Meant to be read aloud
	// and compared over a trusted channel; matches iff neither fingerprint
	// was tampered with in transit.
	SAS string
	// FingerprintChanged is true if the peer's DTLS fingerprint differs from
	// the one pinned from a previous call with them (trust-on-first-use) -
	// worth a loud warning, since it can mean a signaling server is
	// swapping fingerprints to sit in the middle of the call.
	FingerprintChanged bool
}

// MissedCallMsg is sent when a peer proposed a call while this account
// already had one in progress — the daemon auto-rejected it (no call waiting
// in this slice) and this is the UI's only signal that it happened.
type MissedCallMsg struct {
	AccountIdx int
	From       string // bare JID
	SID        string
}

type noticeClearMsg struct {
	id int
}

// openPendingChatMsg triggers openPendingChat on startup, once, via Init's
// returned tea.Cmd — needed because the account owning the last-opened chat
// may already be fully connected when the TUI attaches to a running daemon,
// so no AccountConnectedMsg/AccountLiveMsg ever arrives to trigger it.
type openPendingChatMsg struct{}
