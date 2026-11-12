package ui

import (
	"errors"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

// ErrQueued is Send's error when the account is offline and the message was
// queued: not sent, not failed (see Message.Pending and
// MessageSendResolvedMsg).
var ErrQueued = errors.New("account offline; message queued for delivery")

// uploadCanceledMsg is the daemon's errUploadCanceled text: errors cross the
// socket as strings, so a canceled upload is recognised by it.
const uploadCanceledMsg = "upload canceled"

// HistoryAnchor is the message a Daemon.LoadHistoryWindow window is loaded
// around: its SentAt.Unix() and StoreID.
type HistoryAnchor struct {
	Delay   int64
	StoreID int64
}

// HistoryWindowMsg reports Daemon.LoadHistoryWindow's result: Messages,
// oldest first, replaces what was loaded for the chat. HasOlder/HasNewer
// report whether storage has more beyond either end.
type HistoryWindowMsg struct {
	AccountIdx int
	From       string
	Messages   []Message
	HasOlder   bool
	HasNewer   bool

	// Err is set when the fetch failed, as opposed to an empty window, which
	// would replace the loaded history with nothing.
	Err error `json:"-"`
}

// HistorySearchResultMsg reports Daemon.SearchHistory's result: the chat's
// whole history, and Matches indexing the messages containing Query. Only
// for listing matches: picking one loads a window around it like any other.
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

// ChatAddedMsg reports a chat that came to be, or was renamed, while TUIs are
// attached: a stranger writing, a contact added elsewhere, a subscription
// approved. An upsert.
type ChatAddedMsg struct {
	AccountIdx int
	Chat       Chat

	// Messages is the chat's history.
	Messages []Message
}

// ChatRemovedMsg reports a contact removed elsewhere. History stays.
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

// FileTransferProgressMsg reports one transfer's progress. ID tells
// transfers apart (an upload's path, a download's URL). Total is 0 while
// unknown.
type FileTransferProgressMsg struct {
	ID    string
	Label string
	Sent  int64
	Total int64
}

// FileTransferDoneMsg reports a transfer finished, so TUIs that only saw its
// progress clear it.
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

// FileUploadResultMsg reports Daemon.UploadFile's result. AttachID is the
// pendingAttachment it's for: one path can be staged twice.
type FileUploadResultMsg struct {
	AttachID int
	Path     string
	URL      string
	Queued   bool // true: account was offline, upload+send was queued instead of attempted - URL/Err are both zero
	Err      error
}

// ComposedSendResultMsg reports startAttachedSend's result. Messages is every
// attachment sent before Err, if any, stopped the rest.
type ComposedSendResultMsg struct {
	AccountIdx int
	To         string
	ReplyToID  string   // non-empty if this was sent in reply to a message
	Paths      []string // every staged local path this batch attempted, for clearing their transfer-progress entries regardless of how far the batch got
	Messages   []SentMessage
	Queued     bool // true: account was offline, every not-yet-sent file in this batch was queued rather than attempted

	// QueuedLocalID/QueuedPath are the queued file, shown as a pending
	// placeholder that the outbox replay resolves by LocalID.
	QueuedLocalID string
	QueuedPath    string

	// RetryOfLocalID is the failed placeholder a retry updates in place.
	RetryOfLocalID string

	// FailedLocalID/FailedPaths are the files not sent after a failure, kept
	// as a retryable failed placeholder.
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

// MessageCorrectedMsg reports a XEP-0308 correction.
type MessageCorrectedMsg struct {
	AccountIdx int
	From       string // bare JID (chat)
	ReplaceID  string // ID of the message being corrected
	NewContent string
	Encrypted  bool   // whether the correction itself was e2e encrypted on the wire
	EncMethod  string // "omemo-v1", "omemo-v2", "omemo", or "gpg", matching Message.EncMethod; empty when Encrypted is false
}

// MessageRetractedMsg reports a XEP-0424 retraction; the message is only
// flagged (see Message.Retracted).
type MessageRetractedMsg struct {
	AccountIdx int
	From       string // bare JID (chat)
	RetractID  string // ID of the message being retracted
}

// MessageSendResolvedMsg reports a queued send's outcome, for its
// placeholder, found by LocalID: ID and encryption once sent, or Err.
type MessageSendResolvedMsg struct {
	AccountIdx int
	To         string // bare/full JID this was addressed to (chat key)
	LocalID    string
	ID         string // the real stanza ID, non-empty only on success
	Encrypted  bool
	EncMethod  string
	Err        string // non-empty means the queued send ultimately failed

	// Attachments/Content, when Attachments is set, replace the
	// placeholder's: a queued attachment's URL is only known now.
	Content     string
	Attachments []string
}

// OutboxDeletedMsg reports a queued message discarded (Daemon.DeleteQueued);
// its placeholder is removed.
type OutboxDeletedMsg struct {
	AccountIdx int
	To         string // bare/full JID this was addressed to (chat key)
	LocalID    string
}

// MessagePurgedMsg reports a retracted message erased (Daemon.PurgeMessage).
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

// MessageServerAckedMsg reports our server has a message we sent (see
// Message.ServerAcked).
type MessageServerAckedMsg struct {
	AccountIdx int
	To         string // bare/full JID this was addressed to (chat key)
	MessageID  string
}

// MessageSendFailedMsg reports a sent message that never arrived: the server
// didn't confirm it, or the recipient's side bounced it.
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
// TUI shows the same thing. Mode carries ChatEncryption, On the rest.
type ChatSettingMsg struct {
	AccountJID  string
	ChatAddress string
	Setting     ChatSetting
	Mode        string
	On          bool
}

// ChatSetting is one of a chat's settings.
type ChatSetting string

const (
	// ChatEncryption is the chat's outgoing encryption.
	ChatEncryption ChatSetting = "encryption"
	// ChatHidden keeps the chat out of the list, see Chat.Hidden.
	ChatHidden ChatSetting = "hidden"
	// ChatPinned sorts the chat to the top of the list.
	ChatPinned ChatSetting = "pinned"
	// ChatMuted stops the chat's messages raising notifications.
	ChatMuted ChatSetting = "muted"
)

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

// AvatarPublishedMsg reports setting (or, Removed, removing) our avatar.
type AvatarPublishedMsg struct {
	AccountIdx int
	Removed    bool
	Err        error
}

// AvatarUpdatedMsg reports a contact's avatar cached at Path ("" removed).
// The TUI reads the file, decoding it before this reaches Update.
type AvatarUpdatedMsg struct {
	AccountIdx int
	JID        string // bare JID
	Path       string
}

// DeviceNameMsg reports a contact resource's client name (see
// xmpp.Client.DeviceName), some time after its presence.
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

// typingPauseTimeout is how long after the last keystroke we stop
// "composing" (XEP-0085), even with text still in the box.
const typingPauseTimeout = 5 * time.Second

// typingPauseMsg fires typingPauseTimeout after a keystroke; stale unless Gen
// is still Model.typingGen.
type typingPauseMsg struct {
	addr string
	gen  int
}

func typingPauseTimer(addr string, gen int) tea.Cmd {
	return tea.Tick(typingPauseTimeout, func(time.Time) tea.Msg {
		return typingPauseMsg{addr: addr, gen: gen}
	})
}

// peerTypingTimeout is how long a contact's "composing" is believed, as
// XEP-0085 advises: a peer that drops off never sends "active". One still
// typing sends "composing" again.
const peerTypingTimeout = 30 * time.Second

// peerTypingExpiredMsg fires peerTypingTimeout after a "composing"; stale
// unless Gen is still the chat's peerTypingGen.
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

// flashClearMsg fires flashDuration after a flash; stale unless Gen is still
// Model.flashGen.
type flashClearMsg struct {
	gen int
}

func flashTimer(gen int) tea.Cmd {
	return tea.Tick(flashDuration, func(time.Time) tea.Msg {
		return flashClearMsg{gen: gen}
	})
}

// chatSwitchDebounce is how long the chat-list selection must sit still
// before the selected chat renders: rendering every chat a held key passes
// would lag the cursor.
const chatSwitchDebounce = 40 * time.Millisecond

// chatSwitchSettledMsg fires chatSwitchDebounce after the selection moved;
// stale unless gen is still Model.chatSwitchGen.
type chatSwitchSettledMsg struct {
	gen int
}

func chatSwitchTimer(gen int) tea.Cmd {
	return tea.Tick(chatSwitchDebounce, func(time.Time) tea.Msg {
		return chatSwitchSettledMsg{gen: gen}
	})
}

// hoverDevicesDelay is how long the mouse must rest on a chat row before its
// contact's devices show, so passing over rows doesn't flash them.
const hoverDevicesDelay = time.Second

// hoverDevicesRevealMsg fires hoverDevicesDelay after a row was hovered;
// stale unless gen is still Model.hoverGen.
type hoverDevicesRevealMsg struct {
	id  string
	gen int
}

func hoverDevicesTimer(id string, gen int) tea.Cmd {
	return tea.Tick(hoverDevicesDelay, func(time.Time) tea.Msg {
		return hoverDevicesRevealMsg{id: id, gen: gen}
	})
}

// draftSaveDebounce is how long typing must pause before the draft is saved.
// Switching chats and sending save it right away.
const draftSaveDebounce = 3 * time.Second

// draftSaveMsg fires draftSaveDebounce after a keystroke; stale unless gen is
// still Model.draftSaveGen and addr the chat in the input.
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

// notifyIdleTimeout is how long without input before the user is taken as
// not looking, focus or not (see Model.idle).
const notifyIdleTimeout = 10 * time.Minute

// idleMsg fires notifyIdleTimeout after the last input; stale unless gen is
// still Model.idleGen.
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

// AccountAppearedMsg reports an account added, by any TUI, at Index: every
// TUI must mirror the daemon's indices, which address accounts in calls.
type AccountAppearedMsg struct {
	Index   int
	Account Account
}

// AccountAddErrorMsg is sent into the Bubble Tea loop when Daemon.AddAccount
// fails; the add-account form stays open so the user can correct and retry.
type AccountAddErrorMsg struct {
	Err error
}

// AccountRemovedMsg reports an account removed (Daemon.RemoveAccount). It
// stays listed as removed until restart: indices must stay stable.
type AccountRemovedMsg struct {
	Index int
}

// AccountRemoveErrorMsg is sent into the Bubble Tea loop when
// Daemon.RemoveAccount fails.
type AccountRemoveErrorMsg struct {
	Index int
	Err   error
}

// NoticeMsg shows Text in the notice line, like the UI's own notices.
type NoticeMsg struct {
	Text string
}

// AccountsSnapshotMsg delivers the daemon's account snapshot once the TUI is
// running: it can take seconds, so the TUI doesn't wait for it to draw.
type AccountsSnapshotMsg struct {
	Accounts []Account

	// StartAccount is the account to display, from config (default account,
	// or the one holding the last-opened chat).
	StartAccount int

	// CallState is the daemon's current call state, if any was in progress
	// when this client attached.
	CallState *CallStateMsg

	// Err is set when the snapshot couldn't be fetched, which would otherwise
	// look like having no accounts.
	Err string
}

// AccountConnectedMsg delivers an account's stored chats, before it's
// online; it stays Connecting until AccountLiveMsg.
type AccountConnectedMsg struct {
	Index   int
	Account Account
}

// AccountLiveMsg reports an account online. NewChats are contacts the stored
// roster didn't have.
type AccountLiveMsg struct {
	Index          int
	NewChats       []list.Item
	NewMessages    map[int][]Message // indices are relative to Chats *after* NewChats is appended
	NewHistoryMore map[int]bool      // same indexing as NewMessages; whether older history exists beyond what's loaded

	// SupportsInvisible is whether the server offers XEP-0186, which the
	// snapshot doesn't wait for.
	SupportsInvisible bool
}

// AccountConnectErrorMsg reports an account failed to load or connect; it
// stays listed, with the error.
type AccountConnectErrorMsg struct {
	Index int
	Err   error
}

// HistorySyncedMsg delivers messages backfilled from the archive (XEP-0313),
// handled like incoming ones.
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

// HistorySyncStartedMsg marks an account's archive backfill starting (see
// syncArchive), for a "syncing" indicator.
type HistorySyncStartedMsg struct {
	AccountIdx int
}

// HistorySyncFinishedMsg marks the end of that backfill, successful or not.
type HistorySyncFinishedMsg struct {
	AccountIdx int
}

// AccountStatusSetMsg reports the result of Daemon.SetAccountStatus to the
// TUI that asked; the change itself reaches every attached TUI as an
// AccountStatusChangedMsg.
type AccountStatusSetMsg struct {
	Index  int
	Status Presence
	Err    error
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

// CallActionResultMsg reports whether the daemon took a Daemon.CallAction.
type CallActionResultMsg struct {
	Action CallAction
	Err    error
}

// IncomingCallMsg reports an incoming call (XEP-0353), already ringing.
type IncomingCallMsg struct {
	AccountIdx int
	From       string // bare JID
	SID        string
	Media      string // "audio"
}

// CallStateMsg reports an account's call, on every change. State is
// "proposing", "ringing-remote", "ringing-local", "negotiating",
// "connected", "ended" or "failed"; "" for no call. Reason goes with an
// ending.
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
	// whether its window is currently up (closed by hand until CallReopenVideo).
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
	// StartedAt is set for a TUI attaching mid-call; otherwise the TUI
	// stamps the first "connected".
	StartedAt time.Time
	// SAS is the short authentication string of both ends' DTLS fingerprints,
	// to compare aloud; "" until negotiated.
	SAS string
	// FingerprintChanged is set if the peer's DTLS fingerprint differs from
	// the one pinned at an earlier call: possibly an interception.
	FingerprintChanged bool
}

// MissedCallMsg reports a call declined because one was in progress.
type MissedCallMsg struct {
	AccountIdx int
	From       string // bare JID
	SID        string
}

type noticeClearMsg struct {
	id int
}

// openPendingChatMsg runs openPendingChat at startup: the last chat's account
// may be connected already, so no account message would.
type openPendingChatMsg struct{}
