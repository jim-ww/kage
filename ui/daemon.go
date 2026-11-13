package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// Daemon is everything the UI asks of the kage daemon, which owns the XMPP
// connections, storage and config - ui knows nothing of either. The
// methods returning tea.Msg or tea.Cmd may block on the network or on a
// slow scan of storage, so ui runs them as commands; the rest are quick
// local writes, called straight from Update.
type Daemon interface {
	// ── Messages ────────────────────────────────────────────────────────

	// Send delivers an outgoing message from account accountIdx to "to" (a
	// bare/full JID), returning the ID it was sent with. A non-nil err is
	// either ErrQueued (see its doc comment) or a real failure - the
	// caller must not treat the two the same way.
	Send(accountIdx int, to, body string, opts SendOptions) (id string, err error)
	// MarkRetracted flags a message as locally deleted without sending
	// anything - deleting someone else's message, which XEP-0424 can't
	// retract. Its content stays in storage, only flagged.
	MarkRetracted(accountIdx int, to, id string) error
	// SetTyping sends a XEP-0085 chat state: composing while the user types
	// to "to", not once they stop, send or leave.
	SetTyping(accountIdx int, to string, composing bool) error
	// DeleteQueued discards a still-pending message (Message.LocalID) for
	// good, without ever sending it. Not an error if nothing's queued under
	// localID any more.
	DeleteQueued(accountIdx int, localID string) error
	// PurgeMessage erases an already-retracted message and its reactions
	// from local storage - the second step of the two-step delete. Local
	// only; not an error if nothing matches id any more.
	PurgeMessage(accountIdx int, to, id string) error

	// ── Files ───────────────────────────────────────────────────────────

	// SendFile uploads a file (XEP-0363) and sends its URL to a chat. Of
	// opts only the reply fields mean anything here.
	SendFile(accountIdx int, to, path string, opts SendOptions) tea.Msg
	// UploadFile uploads a file, applying the chat's encryption, without
	// sending it - an attachment staged for the next message. text and opts
	// are that message: offline, the upload and send are queued whole, and
	// the result comes back Queued rather than failed, so the caller must
	// not Send for it too.
	UploadFile(accountIdx int, to, path, text string, opts SendOptions) tea.Msg
	// CancelUpload aborts the upload of path, if it's still running.
	CancelUpload(path string)

	// ── History ─────────────────────────────────────────────────────────

	// LoadHistoryWindow loads a window of a chat's stored history around
	// anchor - half older than it, half newer or equal - or its latest
	// messages for a nil anchor: paging, jumping to the latest message and
	// jumping to a search result are all this.
	LoadHistoryWindow(accountIdx int, to string, anchor *HistoryAnchor) tea.Cmd
	// SearchHistory finds the messages of a chat's whole stored history
	// containing query.
	SearchHistory(accountIdx int, to, query string) tea.Cmd
	// MessageDays lists the local days a chat has stored messages on,
	// oldest first.
	MessageDays(accountIdx int, to string) ([]time.Time, error)

	// ── Contacts and chats ──────────────────────────────────────────────

	// RenameContact sets a contact's display name, a roster set (RFC 6121).
	RenameContact(accountIdx int, address, name string) error
	AddContact(accountIdx int, address string) tea.Msg
	RemoveContact(accountIdx int, address string) tea.Msg
	ResubscribeContact(accountIdx int, address string) tea.Msg
	// SetChatFlag turns a chat's ChatHidden, ChatPinned or ChatMuted on or
	// off - all local and reversible.
	SetChatFlag(accountJID, chatAddress string, flag ChatSetting, on bool) error
	// SetChatEncryption sets a chat's outgoing encryption: "omemo-v1",
	// "omemo-v2", "gpg" or "none" (see encryptionModes).
	SetChatEncryption(accountIdx int, peerJID, mode string) error
	// ResetChatUnread clears a chat's unread count; counting is the
	// daemon's (ChatUnreadMsg), which alone knows whether any attached TUI
	// shows the chat.
	ResetChatUnread(accountJID, chatAddress string) error
	ChatUnreadCounts(accountJID string) (map[string]int, error)
	// SaveDraft stores a chat's unsent text; "" clears it.
	SaveDraft(accountJID, chatAddress, text string) error

	// ── Accounts ────────────────────────────────────────────────────────

	// AddAccount connects and saves a new account - registering it on the
	// server first (XEP-0077) when register is set.
	AddAccount(jid, password, gpgKeyID string, register bool) tea.Msg
	// RemoveAccount disconnects an account and drops it from config; its
	// local history and keys are kept.
	RemoveAccount(accountIdx int) tea.Msg
	// SetAccountStatus sets and saves an account's own presence:
	// PresenceOffline disconnects it, anything else connects it if needed.
	SetAccountStatus(accountIdx int, status Presence) tea.Msg
	// SetOwnAvatar publishes the account's avatar (XEP-0084);
	// RemoveOwnAvatar removes it.
	SetOwnAvatar(accountIdx int, path string) error
	RemoveOwnAvatar(accountIdx int) error
	// FetchOwnDeviceList and PurgeOwnDeviceList read and republish the
	// account's own OMEMO device lists (v1 and v2).
	FetchOwnDeviceList(accountIdx int) tea.Msg
	PurgeOwnDeviceList(accountIdx int, keep []OmemoDevice) tea.Msg
	// ChangeStoragePassword re-encrypts every locally encrypted message and
	// draft under a new password ("" turns local encryption off).
	ChangeStoragePassword(newPassword string) error

	// ── UI state, saved for the next start ──────────────────────────────

	SaveState(change StateChange) error
	// SetFocusState tells the daemon which chat ("" for none) is on screen
	// and whether the terminal has focus, so it doesn't notify about what's
	// already being read.
	SetFocusState(accountJID, chatAddress string, focused bool) error

	// ── Calls ───────────────────────────────────────────────────────────

	// CallAction does something to accountIdx's call. The result only says
	// whether the daemon took it (CallActionResultMsg); the call's state
	// transitions arrive as CallStateMsg/IncomingCallMsg events.
	CallAction(accountIdx int, action CallAction) tea.Msg
	// ListAudioDevices lists the microphones and speakers (AudioDevicesMsg).
	ListAudioDevices() tea.Msg
}

// StateChange is one piece of UI state the daemon saves for the next start.
type StateChange interface{ stateChange() }

type (
	SidebarWidth   int
	SidebarHidden  bool
	InputHeight    int
	FilePickerSort struct {
		Field     string
		Ascending bool
	}
	// LastChat is the chat open last, reopened on startup.
	LastChat struct{ AccountJID, ChatAddress string }
	// DefaultAccount is the account selected on startup.
	DefaultAccount string
	// ReactionsSent counts reactions sent, which rank the emoji picker's
	// suggestions (see rankReactionUsage).
	ReactionsSent []string
	// DesktopAudio is whether a screen share also sends what other
	// applications play.
	DesktopAudio bool
	// DesktopAudioVolume is the desktop audio's volume, in percent.
	DesktopAudioVolume int
	// AudioDeviceChoice picks the microphone or speaker (Output) calls use,
	// moving a call in progress over to it.
	AudioDeviceChoice struct {
		Output bool
		Name   string
	}
)

func (SidebarWidth) stateChange()       {}
func (SidebarHidden) stateChange()      {}
func (InputHeight) stateChange()        {}
func (FilePickerSort) stateChange()     {}
func (LastChat) stateChange()           {}
func (DefaultAccount) stateChange()     {}
func (ReactionsSent) stateChange()      {}
func (DesktopAudio) stateChange()       {}
func (DesktopAudioVolume) stateChange() {}
func (AudioDeviceChoice) stateChange()  {}

// saveStateCmd saves change off the UI goroutine - applying it can take a
// while, like moving a call over to another audio device - reporting a
// failure to save what as a notice.
func saveStateCmd(d Daemon, change StateChange, what string) tea.Cmd {
	return func() tea.Msg {
		if err := d.SaveState(change); err != nil {
			return NoticeMsg{Text: "saving " + what + ": " + err.Error()}
		}
		return nil
	}
}

// CallAction is one thing done to a call.
type CallAction interface{ callAction() string }

type (
	// CallStart places a call to To - sending our video from the moment it
	// connects with Video, the camera's with UseCamera, else the screen's.
	CallStart struct {
		To               string
		Video, UseCamera bool
	}
	CallAnswer struct{}
	CallReject struct{}
	CallHangup struct{}
	CallMute   struct{ Muted bool }
	// CallShareVideo starts or stops sending our camera (UseCamera) or
	// screen.
	CallShareVideo struct{ Sharing, UseCamera bool }
	// CallReopenVideo asks for a fresh keyframe of the call's incoming
	// video, reopening its viewer if it was closed.
	CallReopenVideo struct{}
)

func (CallStart) callAction() string       { return "start" }
func (CallAnswer) callAction() string      { return "answer" }
func (CallReject) callAction() string      { return "reject" }
func (CallHangup) callAction() string      { return "hangup" }
func (CallMute) callAction() string        { return "mute" }
func (CallShareVideo) callAction() string  { return "video" }
func (CallReopenVideo) callAction() string { return "reopen video" }
