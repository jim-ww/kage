package ui

import (
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

// MessageSender delivers an outgoing message for the given account to "to"
// (a bare/full JID), returning the ID it was sent with. Implemented outside
// ui — by an adapter that knows about the xmpp session and any per-peer
// encryption — so ui stays decoupled from both.
type MessageSender interface {
	Send(accountIdx int, to, body string, opts SendOptions) (id string, err error)

	// MarkRetracted flags a message as locally deleted without sending
	// anything over the network — used when deleting someone else's message
	// (XEP-0424 retraction can only be attempted on our own messages). The
	// message's content is never removed from storage, only flagged.
	MarkRetracted(accountIdx int, to, id string) error

	// SetTyping sends a XEP-0085 chat state notification: composing=true
	// while the user is actively typing to "to", false once they stop
	// (cleared the input or sent) or navigate away.
	SetTyping(accountIdx int, to string, composing bool) error
}

// FileSender uploads a local file and sends its download URL to a chat. It is
// separate from MessageSender so text-only senders remain compatible. opts
// carries the same reply/correction metadata as MessageSender.Send — only
// ReplyToID/QuotedAuthor/QuotedBody are meaningful here.
type FileSender interface {
	SendFile(accountIdx int, to, path string, opts SendOptions) tea.Msg

	// UploadFile uploads a local file (applying the same peer encryption
	// policy as SendFile) without sending it as a message — used to stage a
	// pending attachment above the compose box, so several files can be
	// attached to a single outgoing message. to is the peer JID the
	// eventual message will go to, needed to resolve the encryption policy.
	// text/opts carry what the follow-up message for this file would say if
	// the account were online (only meaningful for the offline case below):
	// if the account has no live connection, the upload+send is queued
	// whole (text, opts and all) instead of failing outright, and the
	// result comes back with Queued set rather than an error - the caller
	// must not also call MessageSender.Send for this file, since there's no
	// URL yet to send.
	UploadFile(accountIdx int, to, path, text string, opts SendOptions) tea.Msg
}

// HistoryLoader fetches the next older page of a chat's persisted history —
// implemented outside ui by an adapter that knows about local storage, kept
// off the main goroutine as a tea.Cmd since it's a disk read plus decrypt of
// up to a page's worth of messages. "to" is the chat's peer address (bare or
// full JID, whatever the chat is keyed by).
type HistoryLoader interface {
	LoadOlderHistory(accountIdx int, to string) tea.Cmd
}

// OlderHistoryMsg reports the result of HistoryLoader.LoadOlderHistory:
// Messages (already in chronological order) to prepend to the chat matching
// AccountIdx/From, and whether further older history remains beyond that.
type OlderHistoryMsg struct {
	AccountIdx int
	From       string
	Messages   []Message
	HasMore    bool
}

// HistorySearcher searches a chat's entire persisted history for messages
// whose content contains a substring, implemented outside ui (main.go's
// adapter, backed by storage) so ui stays decoupled from the storage/crypto
// layers. Runs as a tea.Cmd, off the Bubble Tea event loop's goroutine, since
// (unlike HistoryLoader's single page) it has to decrypt and scan the whole
// chat history.
type HistorySearcher interface {
	SearchHistory(accountIdx int, to, query string) tea.Cmd
}

// HistorySearchResultMsg reports the result of HistorySearcher.SearchHistory.
// Messages is the chat's entire persisted history, already decrypted and in
// chronological order — exactly what ui.Model would hold if the chat were
// fully loaded — and Matches indexes into it for every message whose content
// contains Query (case-insensitive). Selecting a match and pressing enter
// loads Messages wholesale as the chat's new in-memory window and jumps the
// cursor to Matches[i], so no further paging is needed for that chat.
type HistorySearchResultMsg struct {
	AccountIdx int
	From       string
	Query      string
	Messages   []Message
	Matches    []int
	Err        error
}

// ContactRenamer sets a custom display name for a contact — a roster set
// (RFC 6121), persisted server-side and mirrored to local storage — so ui
// stays decoupled from the XMPP/storage layers. Called synchronously from
// Update(), like MessageSender.Send/SetTyping: renaming isn't a bulk
// operation and doesn't need its own async result message.
type ContactRenamer interface {
	RenameContact(accountIdx int, address, name string) error
}

// ContactManager adds/removes roster contacts (RFC 6121 roster set/delete
// plus the matching presence subscribe/unsubscribe), implemented outside ui
// (main.go's adapter) so ui stays decoupled from the XMPP layer. Both run as
// a tea.Cmd like AccountAdder.AddAccount since they're network I/O.
type ContactManager interface {
	AddContact(accountIdx int, address string) tea.Msg
	RemoveContact(accountIdx int, address string) tea.Msg
	ResubscribeContact(accountIdx int, address string) tea.Msg
}

// ContactAddedMsg reports the result of ContactManager.AddContact.
type ContactAddedMsg struct {
	AccountIdx int
	Address    string
	Err        error
}

// ContactRemovedMsg reports the result of ContactManager.RemoveContact.
type ContactRemovedMsg struct {
	AccountIdx int
	Address    string
	Err        error
}

// ContactResubscribedMsg reports the result of ContactManager.ResubscribeContact.
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

// FileUploadResultMsg reports completion of FileSender.UploadFile — a file
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
	Err        error
}

// SentMessage is one message startAttachedSend successfully sent.
type SentMessage struct {
	ID          string
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

// MessageDeliveredMsg is sent into the Bubble Tea loop when a XEP-0184
// delivery receipt arrives for a message we sent.
type MessageDeliveredMsg struct {
	AccountIdx int
	From       string // bare JID (chat)
	MessageID  string // ID of the message the peer acknowledged
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

// AccountAdder connects and persists a new XMPP account, implemented outside
// ui (main.go's adapter) so ui stays decoupled from the network/config
// layers. Runs on the Bubble Tea event loop's goroutine via a tea.Cmd, so it
// may block on network I/O — ui always calls it that way, never inline.
type AccountAdder interface {
	AddAccount(jid, password, gpgKeyID string) tea.Msg
}

// AccountAddedMsg is sent into the Bubble Tea loop once AccountAdder.AddAccount
// has connected the new account and it's ready to show in the sidebar.
type AccountAddedMsg struct {
	Account Account
}

// AccountAddErrorMsg is sent into the Bubble Tea loop when AccountAdder.AddAccount
// fails; the add-account form stays open so the user can correct and retry.
type AccountAddErrorMsg struct {
	Err error
}

// AccountRemover disconnects an account and drops it from config.yaml —
// implemented outside ui (main.go's adapter) so ui stays decoupled from the
// network/config layers. Runs on the Bubble Tea event loop's goroutine via a
// tea.Cmd since it involves network I/O (purging the account's published
// OMEMO device list before disconnecting). Never touches local storage — no
// history, roster cache, or OMEMO identity/session state is deleted, only
// the live connection and the config.yaml entry.
type AccountRemover interface {
	RemoveAccount(accountIdx int) tea.Msg
}

// AccountRemovedMsg is sent into the Bubble Tea loop once
// AccountRemover.RemoveAccount has disconnected accountIdx and dropped it
// from config.yaml. The account stays in the sidebar for the rest of this
// run (indices must stay stable — see AccountConnectedMsg) but shows as
// offline/removed and is excluded from switching; it's gone for good only
// after a restart.
type AccountRemovedMsg struct {
	Index int
}

// AccountRemoveErrorMsg is sent into the Bubble Tea loop when
// AccountRemover.RemoveAccount fails.
type AccountRemoveErrorMsg struct {
	Index int
	Err   error
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

// AccountStatusSetter changes and persists a configured account's own
// presence status (online/away/offline), implemented outside ui (main.go's
// adapter) so ui stays decoupled from the network/config layers. Setting
// PresenceOffline disconnects the account entirely (no further traffic to
// its server until switched back); setting Online/Away on a currently
// offline account dials it. Runs on the Bubble Tea event loop's goroutine via
// a tea.Cmd, like AccountAdder.AddAccount, since it may block on network I/O.
type AccountStatusSetter interface {
	SetAccountStatus(accountIdx int, status Presence) tea.Msg
}

// AccountStatusSetMsg reports the result of AccountStatusSetter.SetAccountStatus.
// NewChats/NewMessages/NewHistoryMore carry any contacts discovered while
// bringing a previously-offline account online, indexed the same way as
// AccountLiveMsg's — empty when no (re)connect was needed.
type AccountStatusSetMsg struct {
	Index          int
	Status         Presence
	NewChats       []list.Item
	NewMessages    map[int][]Message
	NewHistoryMore map[int]bool
	Err            error
}

// DefaultAccountSetter persists which account should be selected on startup,
// implemented outside ui (main.go's adapter) so ui stays decoupled from the
// config layer. It's a local file write, not network I/O, so ui calls it
// inline like Send/SetTyping rather than through a tea.Cmd.
type DefaultAccountSetter interface {
	SetDefaultAccount(jid string) error
}

// ChatEncryptionSetter persists per-chat outgoing message encryption choice
// ("omemo-v1", "omemo-v2", "gpg", or "none" - see
// encryptionModes), implemented outside ui (main.go's adapter) so ui stays
// decoupled from the storage layer. A local database write, called inline
// like Send/SetTyping rather than through a tea.Cmd.
type ChatEncryptionSetter interface {
	SetChatEncryption(accountIdx int, peerJID, mode string) error
}

// SidebarWidthSetter persists the sidebar width the user last dragged it to,
// implemented outside ui (main.go's adapter) so ui stays decoupled from the
// config layer. A local file write, called inline like Send/SetTyping
// rather than through a tea.Cmd.
type SidebarWidthSetter interface {
	SetSidebarWidth(width int) error
}

// SidebarHiddenSetter persists whether the chat list sidebar is currently
// hidden, implemented outside ui (main.go's adapter) so ui stays decoupled
// from the config layer. A local file write, called inline like Send/
// SetTyping rather than through a tea.Cmd.
type SidebarHiddenSetter interface {
	SetSidebarHidden(hidden bool) error
}

// InputHeightSetter persists the compose box height the user last dragged
// it to, implemented outside ui (main.go's adapter) so ui stays decoupled
// from the config layer. A local file write, called inline like Send/
// SetTyping rather than through a tea.Cmd.
type InputHeightSetter interface {
	SetInputHeight(height int) error
}

// FilePickerSortSetter persists the attach-file picker's sort field
// ("created"/"updated") and direction the user last selected, implemented
// outside ui (main.go's adapter) so ui stays decoupled from the config
// layer. A local file write, called inline like Send/SetTyping rather than
// through a tea.Cmd.
type FilePickerSortSetter interface {
	SetFilePickerSort(field string, ascending bool) error
}

// StoragePasswordChanger re-encrypts every locally-encrypted message body
// and draft under a new password, implemented outside ui (main.go's
// adapter, backed by storage) so ui stays decoupled from the storage/crypto
// layers. Unlike most setters here this runs as a tea.Cmd rather than being
// called inline from Update — it walks and re-seals every encrypted row in
// one transaction, slow enough on a large history to visibly block the UI
// if done synchronously. See StoragePasswordChangedMsg for the result.
type StoragePasswordChanger interface {
	ChangeStoragePassword(newPassword string) error
}

// StoragePasswordChangedMsg reports the result of a StoragePasswordChanger
// call, produced by the "change storage password" popup's submit action.
type StoragePasswordChangedMsg struct {
	Err error
}

// LastChatSetter persists which chat was last opened, implemented outside
// ui (main.go's adapter) so ui stays decoupled from the config layer. A
// local file write, called inline like Send/SetTyping rather than through a
// tea.Cmd. Used to reopen the same chat on startup when configured.
type LastChatSetter interface {
	SetLastChat(accountJID, chatAddress string) error
}

// FocusReporter tells the daemon which chat (if any) is actively being
// viewed and whether the terminal itself currently has OS focus,
// implemented outside ui (main.go's adapter) so ui stays decoupled from the
// notification layer. In-memory only on the daemon side — used to suppress
// a desktop notification for a message that's already visible on screen.
// Called inline like Send/SetTyping rather than through a tea.Cmd. An empty
// chatAddress means no chat is currently open.
type FocusReporter interface {
	SetFocusState(accountJID, chatAddress string, focused bool) error
}

// ChatReadTracker persists local-only unread message counts per chat,
// implemented outside ui (main.go's adapter, backed by storage) so ui stays
// decoupled from the storage layer. Called inline like Send/SetTyping
// rather than through a tea.Cmd; a failure just means the in-memory count
// (still updated regardless) won't survive a restart.
type ChatReadTracker interface {
	IncrementChatUnread(accountJID, chatAddress string, delta int) error
	ResetChatUnread(accountJID, chatAddress string) error
	ChatUnreadCounts(accountJID string) (map[string]int, error)
}

// DraftSaver persists the compose box's unsent text per chat, implemented
// outside ui (main.go's adapter, backed by storage) so ui stays decoupled
// from the storage layer. Called inline like Send/SetTyping rather than
// through a tea.Cmd; a failure just means the in-memory draft (still updated
// regardless) won't survive a restart. An empty text means "no draft" and
// should clear any persisted row rather than storing an empty string.
type DraftSaver interface {
	SaveDraft(accountJID, chatAddress, text string) error
}

// CallController drives the daemon's voice call state machine
// (callsession.go) for an account: placing/accepting/ending a XEP-0166/0167
// call, implemented outside ui (main.go's adapter/ipcClient) so ui stays
// decoupled from the XMPP/Jingle layers. Each call is a network round trip
// through IPC to the daemon, so — like ContactManager.AddContact/RemoveContact
// — these return tea.Msg rather than blocking Update synchronously.
type CallController interface {
	StartCall(accountIdx int, to string) tea.Msg
	AnswerCall(accountIdx int) tea.Msg
	HangupCall(accountIdx int) tea.Msg
	RejectCall(accountIdx int) tea.Msg
	MuteCall(accountIdx int, muted bool) tea.Msg
	ScreenShare(accountIdx int, sharing bool) tea.Msg
}

// CallActionResultMsg reports the result of a CallController method call
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
// CallController.AnswerCall or RejectCall.
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
	Sharing    bool   // true while we're actively sending our own screen
	// StartedAt is normally left zero - handleCallStateMsg fills it in the
	// moment State first becomes "connected". A daemon-provided sync (a TUI
	// attaching to a call already in progress) sets it explicitly so the
	// duration displayed doesn't reset to 00:00.
	StartedAt time.Time
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
