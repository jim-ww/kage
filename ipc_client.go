package main

import (
	"fmt"
	"log/slog"

	tea "charm.land/bubbletea/v2"
	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/ui"
)

// ipcClient is the TUI's ui.Daemon: every method is a call to the daemon
// over conn (see ipc_rpc.go).
type ipcClient struct {
	conn *ipc.Conn
	// program is only ever read/written from dispatchLoop's own goroutine
	// (set via programReady, never assigned directly) - the daemon can start
	// streaming events the moment ipc.Dial returns, well before main has
	// finished the synchronous listAccounts() call and tea.NewProgram, so
	// events routinely need to be dispatched before program exists.
	program *tea.Program

	// events decouples the Conn's read loop from delivery to the program.
	// program.Send blocks until bubbletea's own Update loop is free to
	// receive, but Update can itself be blocked inside a synchronous
	// conn.Call (e.g. Send/SetTyping) waiting on that same read loop to
	// deliver its Response - calling program.Send directly from the read
	// loop (as handleEvent used to) deadlocks in that window. The
	// dispatcher goroutine is the only thing allowed to call program.Send.
	events chan any

	// programReady hands off the freshly constructed *tea.Program from main
	// once it exists. Buffered 1: main sends exactly once and never blocks
	// on dispatchLoop's goroutine scheduling.
	programReady chan *tea.Program

	// pending buffers events that arrived before program was set - e.g. a
	// contact's PresenceMsg pushed the instant another account finishes
	// connecting in the daemon, which can easily land before this process
	// gets through listAccounts() + ui.New(). Previously these were just
	// dropped (dispatch no-oped on a nil program), which permanently lost
	// state a client attaching moments later would otherwise have seen:
	// a contact could show offline indefinitely, until its presence
	// happened to change again. Only ever touched by dispatchLoop's
	// goroutine.
	pending []any
}

// newIPCClient creates a client with its event-dispatch goroutine already
// running, so it's ready to be passed to ipc.Dial as the onEvent callback.
func newIPCClient() *ipcClient {
	c := &ipcClient{events: make(chan any, 256), programReady: make(chan *tea.Program, 1)}
	go c.dispatchLoop()
	return c
}

func (c *ipcClient) dispatchLoop() {
	for {
		select {
		case ev, ok := <-c.events:
			if !ok {
				return
			}
			if c.program == nil {
				c.pending = append(c.pending, ev)
				continue
			}
			c.dispatch(ev)
		case p, ok := <-c.programReady:
			if !ok {
				continue
			}
			c.program = p
			pending := c.pending
			c.pending = nil
			for _, ev := range pending {
				c.dispatch(ev)
			}
		}
	}
}

// setProgram hands the running program to dispatchLoop, which flushes any
// events buffered while program didn't exist yet before processing anything
// new - see programReady and pending.
func (c *ipcClient) setProgram(p *tea.Program) {
	c.programReady <- p
}

func (c *ipcClient) Send(accountIdx int, to, body string, opts ui.SendOptions) (string, error) {
	res, err := do(c.conn, sendReq{AccountIdx: accountIdx, To: to, Body: body, Opts: opts})
	if res.Queued {
		return "", ui.ErrQueued
	}
	return res.ID, err
}

func (c *ipcClient) MarkRetracted(accountIdx int, to, id string) error {
	_, err := do(c.conn, markRetractedReq{AccountIdx: accountIdx, To: to, ID: id})
	return err
}

func (c *ipcClient) PurgeMessage(accountIdx int, to, id string) error {
	_, err := do(c.conn, purgeMessageReq{AccountIdx: accountIdx, To: to, ID: id})
	return err
}

func (c *ipcClient) DeleteQueued(accountIdx int, localID string) error {
	_, err := do(c.conn, deleteQueuedReq{AccountIdx: accountIdx, LocalID: localID})
	return err
}

func (c *ipcClient) SetTyping(accountIdx int, to string, composing bool) error {
	_, err := do(c.conn, setTypingReq{AccountIdx: accountIdx, To: to, Composing: composing})
	return err
}

func (c *ipcClient) SendFile(accountIdx int, to, path string, opts ui.SendOptions) tea.Msg {
	msg, err := do(c.conn, sendFileReq{AccountIdx: accountIdx, To: to, Path: path, Opts: opts})
	if err != nil {
		return ui.FileSendResultMsg{AccountIdx: accountIdx, To: to, Path: path, ReplyToID: opts.ReplyToID, Err: err}
	}
	return msg
}

func (c *ipcClient) UploadFile(accountIdx int, to, path, text string, opts ui.SendOptions) tea.Msg {
	msg, err := do(c.conn, uploadFileReq{AccountIdx: accountIdx, To: to, Path: path, Text: text, Opts: opts})
	if err != nil {
		return ui.FileUploadResultMsg{Path: path, Err: err}
	}
	return msg
}

func (c *ipcClient) CancelUpload(path string) {
	if _, err := do(c.conn, cancelUploadReq{Path: path}); err != nil {
		slog.Warn("canceling upload", "path", path, "err", err)
	}
}

func (c *ipcClient) LoadHistoryWindow(accountIdx int, to string, anchor *ui.HistoryAnchor) tea.Cmd {
	return func() tea.Msg {
		msg, err := do(c.conn, loadHistoryWindowReq{AccountIdx: accountIdx, To: to, Anchor: anchor})
		if err != nil {
			slog.Warn("loading history window", "err", err)
			// Err, not a zero-valued window: the latter reads as "this chat
			// has no messages and no more history in either direction",
			// which blanks the chat and permanently stops its paging.
			return ui.HistoryWindowMsg{AccountIdx: accountIdx, From: to, Err: err}
		}
		return msg
	}
}

func (c *ipcClient) SearchHistory(accountIdx int, to, query string) tea.Cmd {
	return func() tea.Msg {
		msg, err := do(c.conn, searchHistoryReq{AccountIdx: accountIdx, To: to, Query: query})
		if err != nil {
			return ui.HistorySearchResultMsg{AccountIdx: accountIdx, From: to, Query: query, Err: err}
		}
		return msg
	}
}

func (c *ipcClient) RenameContact(accountIdx int, address, name string) error {
	_, err := do(c.conn, renameContactReq{AccountIdx: accountIdx, Address: address, Name: name})
	return err
}

func (c *ipcClient) AddContact(accountIdx int, address string) tea.Msg {
	_, err := do(c.conn, addContactReq{AccountIdx: accountIdx, Address: address})
	return ui.ContactAddedMsg{AccountIdx: accountIdx, Address: address, Err: err}
}

func (c *ipcClient) RemoveContact(accountIdx int, address string) tea.Msg {
	_, err := do(c.conn, removeContactReq{AccountIdx: accountIdx, Address: address})
	return ui.ContactRemovedMsg{AccountIdx: accountIdx, Address: address, Err: err}
}

func (c *ipcClient) ResubscribeContact(accountIdx int, address string) tea.Msg {
	_, err := do(c.conn, resubscribeContactReq{AccountIdx: accountIdx, Address: address})
	return ui.ContactResubscribedMsg{AccountIdx: accountIdx, Address: address, Err: err}
}

func (c *ipcClient) SetChatFlag(accountJID, chatAddress string, flag ui.ChatSetting, on bool) error {
	_, err := do(c.conn, setChatFlagReq{AccountJID: accountJID, ChatAddress: chatAddress, Flag: flag, On: on})
	return err
}

func (c *ipcClient) SetChatEncryption(accountIdx int, peerJID, mode string) error {
	_, err := do(c.conn, setChatEncryptionReq{AccountIdx: accountIdx, PeerJID: peerJID, Mode: mode})
	return err
}

func (c *ipcClient) ResetChatUnread(accountJID, chatAddress string) error {
	_, err := do(c.conn, resetChatUnreadReq{AccountJID: accountJID, ChatAddress: chatAddress})
	return err
}

func (c *ipcClient) ChatUnreadCounts(accountJID string) (map[string]int, error) {
	return do(c.conn, chatUnreadCountsReq{AccountJID: accountJID})
}

func (c *ipcClient) SaveDraft(accountJID, chatAddress, text string) error {
	_, err := do(c.conn, saveDraftReq{AccountJID: accountJID, ChatAddress: chatAddress, Text: text})
	return err
}

// listAccounts is the snapshot a TUI starts from (not part of ui.Daemon -
// main calls it directly).
func (c *ipcClient) listAccounts() ([]ui.Account, error) {
	return do(c.conn, listAccountsReq{})
}

func (c *ipcClient) AddAccount(jid, password, gpgKeyID string, register bool) tea.Msg {
	acct, err := do(c.conn, addAccountReq{JID: jid, Password: password, GPGKeyID: gpgKeyID, Register: register})
	if err != nil {
		return ui.AccountAddErrorMsg{Err: err}
	}
	return ui.AccountAddedMsg{Account: acct}
}

func (c *ipcClient) RemoveAccount(accountIdx int) tea.Msg {
	if _, err := do(c.conn, removeAccountReq{AccountIdx: accountIdx}); err != nil {
		return ui.AccountRemoveErrorMsg{Index: accountIdx, Err: err}
	}
	return ui.AccountRemovedMsg{Index: accountIdx}
}

// SetAccountStatus only reports success or failure: the change itself
// reaches every attached TUI as an AccountStatusChangedMsg.
func (c *ipcClient) SetAccountStatus(accountIdx int, status ui.Presence) tea.Msg {
	_, err := do(c.conn, setAccountStatusReq{AccountIdx: accountIdx, Status: status})
	return ui.AccountStatusSetMsg{Index: accountIdx, Status: status, Err: err}
}

func (c *ipcClient) SetOwnAvatar(accountIdx int, path string) error {
	_, err := do(c.conn, setOwnAvatarReq{AccountIdx: accountIdx, Path: path})
	return err
}

func (c *ipcClient) RemoveOwnAvatar(accountIdx int) error {
	_, err := do(c.conn, removeOwnAvatarReq{AccountIdx: accountIdx})
	return err
}

func (c *ipcClient) FetchOwnDeviceList(accountIdx int) tea.Msg {
	msg, err := do(c.conn, fetchOwnDeviceListReq{AccountIdx: accountIdx})
	if err != nil {
		return ui.OmemoDeviceListMsg{AccountIdx: accountIdx, Err: err}
	}
	return msg
}

func (c *ipcClient) PurgeOwnDeviceList(accountIdx int, keep []ui.OmemoDevice) tea.Msg {
	msg, err := do(c.conn, purgeOwnDeviceListReq{AccountIdx: accountIdx, Keep: keep})
	if err != nil {
		return ui.OmemoDevicePurgedMsg{AccountIdx: accountIdx, Err: err}
	}
	return msg
}

func (c *ipcClient) ChangeStoragePassword(newPassword string) error {
	_, err := do(c.conn, changeStoragePasswordReq{NewPassword: newPassword})
	return err
}

func (c *ipcClient) SaveState(change ui.StateChange) error {
	_, err := do(c.conn, saveStateReq{Change: change})
	return err
}

func (c *ipcClient) SetFocusState(accountJID, chatAddress string, focused bool) error {
	_, err := do(c.conn, setFocusStateReq{AccountJID: accountJID, ChatAddress: chatAddress, Focused: focused})
	return err
}

// callState is the call in progress when this TUI attached, if any, so the
// call bar shows right away instead of at the next transition.
func (c *ipcClient) callState() (*ui.CallStateMsg, error) {
	msg, err := do(c.conn, callStateReq{})
	if err != nil || msg.State == "" {
		return nil, err
	}
	return &msg, nil
}

func (c *ipcClient) CallAction(accountIdx int, action ui.CallAction) tea.Msg {
	_, err := do(c.conn, callActionReq{AccountIdx: accountIdx, Action: action})
	return ui.CallActionResultMsg{Action: action, Err: err}
}

func (c *ipcClient) ListAudioDevices() tea.Msg {
	msg, err := do(c.conn, listAudioDevicesReq{})
	if err != nil {
		return ui.AudioDevicesMsg{Err: err}
	}
	return msg
}

// handleEvent is passed to ipc.Dial as the onEvent callback: it runs on the
// Conn's own read loop, so it must never block - it only enqueues onto
// events, which dispatchLoop drains on its own goroutine.
func (c *ipcClient) handleEvent(ev any) {
	select {
	case c.events <- ev:
	default:
		slog.Warn("ipc event queue full, dropping event", "event", fmt.Sprintf("%T", ev))
	}
}

// dispatch forwards one event to the program. Runs only on dispatchLoop's
// goroutine, and only once c.program is set - see pending.
func (c *ipcClient) dispatch(ev any) {
	if msg, ok := ev.(ui.AvatarUpdatedMsg); ok {
		// Decoded here rather than inside Update on the UI's goroutine.
		if msg.Path == "" {
			ui.RemoveAvatarImage(msg.JID)
		} else if err := ui.LoadAvatarFile(msg.JID, msg.Path); err != nil {
			slog.Debug("loading cached avatar", "jid", msg.JID, "path", msg.Path, "err", err)
			return
		}
	}
	c.program.Send(ev)
}
