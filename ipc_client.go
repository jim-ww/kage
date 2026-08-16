package main

import (
	"encoding/json"
	"errors"
	"log/slog"

	tea "charm.land/bubbletea/v2"
	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/ui"
)

// ipcClient implements every ui interface the daemon's adapter used to, by
// making an RPC over conn instead of touching xmpp/storage/crypto directly
// - the TUI process never does either anymore, the daemon does.
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
	events chan ipc.Event

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
	pending []ipc.Event
}

// newIPCClient creates a client with its event-dispatch goroutine already
// running, so it's ready to be passed to ipc.Dial as the onEvent callback.
func newIPCClient() *ipcClient {
	c := &ipcClient{events: make(chan ipc.Event, 256), programReady: make(chan *tea.Program, 1)}
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
	var res sendResult
	if err := c.conn.Call(rpcSend, sendParams{AccountIdx: accountIdx, To: to, Body: body, Opts: opts}, &res); err != nil {
		return "", err
	}
	if res.Queued {
		return "", ui.ErrQueued
	}
	return res.ID, nil
}

func (c *ipcClient) MarkRetracted(accountIdx int, to, id string) error {
	return c.conn.Call(rpcMarkRetracted, markRetractedParams{AccountIdx: accountIdx, To: to, ID: id}, nil)
}

func (c *ipcClient) DeleteQueued(accountIdx int, localID string) error {
	return c.conn.Call(rpcDeleteQueued, deleteQueuedParams{AccountIdx: accountIdx, LocalID: localID}, nil)
}

func (c *ipcClient) SetTyping(accountIdx int, to string, composing bool) error {
	return c.conn.Call(rpcSetTyping, setTypingParams{AccountIdx: accountIdx, To: to, Composing: composing}, nil)
}

func (c *ipcClient) RenameContact(accountIdx int, address, name string) error {
	return c.conn.Call(rpcRenameContact, renameContactParams{AccountIdx: accountIdx, Address: address, Name: name}, nil)
}

func (c *ipcClient) SetDefaultAccount(jid string) error {
	return c.conn.Call(rpcSetDefaultAccount, setDefaultAccountParams{JID: jid}, nil)
}

func (c *ipcClient) SetChatEncryption(accountIdx int, peerJID, mode string) error {
	return c.conn.Call(rpcSetChatEncryption, setChatEncryptionParams{AccountIdx: accountIdx, PeerJID: peerJID, Mode: mode}, nil)
}

func (c *ipcClient) SetSidebarWidth(width int) error {
	return c.conn.Call(rpcSetSidebarWidth, widthParams{Width: width}, nil)
}

func (c *ipcClient) SetSidebarHidden(hidden bool) error {
	return c.conn.Call(rpcSetSidebarHidden, hiddenParams{Hidden: hidden}, nil)
}

func (c *ipcClient) SetInputHeight(height int) error {
	return c.conn.Call(rpcSetInputHeight, heightParams{Height: height}, nil)
}

func (c *ipcClient) SetFilePickerSort(field string, ascending bool) error {
	return c.conn.Call(rpcSetFilePickerSort, filePickerSortParams{Field: field, Ascending: ascending}, nil)
}

func (c *ipcClient) SetLastChat(accountJID, chatAddress string) error {
	return c.conn.Call(rpcSetLastChat, setLastChatParams{AccountJID: accountJID, ChatAddress: chatAddress}, nil)
}

func (c *ipcClient) IncrementChatUnread(accountJID, chatAddress string, delta int) error {
	return c.conn.Call(rpcIncrementChatUnread, chatUnreadDeltaParams{AccountJID: accountJID, ChatAddress: chatAddress, Delta: delta}, nil)
}

func (c *ipcClient) ResetChatUnread(accountJID, chatAddress string) error {
	return c.conn.Call(rpcResetChatUnread, setLastChatParams{AccountJID: accountJID, ChatAddress: chatAddress}, nil)
}

func (c *ipcClient) SetFocusState(accountJID, chatAddress string, focused bool) error {
	return c.conn.Call(rpcSetFocusState, setFocusStateParams{AccountJID: accountJID, ChatAddress: chatAddress, Focused: focused}, nil)
}

func (c *ipcClient) ChatUnreadCounts(accountJID string) (map[string]int, error) {
	var res chatUnreadCountsResult
	if err := c.conn.Call(rpcChatUnreadCounts, accountJIDParams{AccountJID: accountJID}, &res); err != nil {
		return nil, err
	}
	return res.Counts, nil
}

func (c *ipcClient) SaveDraft(accountJID, chatAddress, text string) error {
	return c.conn.Call(rpcSaveDraft, saveDraftParams{AccountJID: accountJID, ChatAddress: chatAddress, Text: text}, nil)
}

func (c *ipcClient) ChangeStoragePassword(newPassword string) error {
	return c.conn.Call(rpcChangeStoragePassword, changeStoragePasswordParams{NewPassword: newPassword}, nil)
}

func (c *ipcClient) SendFile(accountIdx int, to, path string, opts ui.SendOptions) tea.Msg {
	var msg ui.FileSendResultMsg
	if err := c.conn.Call(rpcSendFile, sendFileParams{AccountIdx: accountIdx, To: to, Path: path, Opts: opts}, &msg); err != nil {
		return ui.FileSendResultMsg{AccountIdx: accountIdx, To: to, Path: path, ReplyToID: opts.ReplyToID, Err: err}
	}
	return msg
}

func (c *ipcClient) UploadFile(accountIdx int, to, path, text string, opts ui.SendOptions) tea.Msg {
	var msg ui.FileUploadResultMsg
	if err := c.conn.Call(rpcUploadFile, uploadFileParams{AccountIdx: accountIdx, To: to, Path: path, Text: text, Opts: opts}, &msg); err != nil {
		return ui.FileUploadResultMsg{Path: path, Err: err}
	}
	return msg
}

func (c *ipcClient) LoadHistoryWindow(accountIdx int, to string, anchor *ui.HistoryAnchor) tea.Cmd {
	return func() tea.Msg {
		var msg ui.HistoryWindowMsg
		if err := c.conn.Call(rpcLoadHistoryWindow, loadHistoryWindowParams{AccountIdx: accountIdx, To: to, Anchor: anchor}, &msg); err != nil {
			slog.Warn("loading history window", "err", err)
			return ui.HistoryWindowMsg{AccountIdx: accountIdx, From: to}
		}
		return msg
	}
}

func (c *ipcClient) SearchHistory(accountIdx int, to, query string) tea.Cmd {
	return func() tea.Msg {
		var msg ui.HistorySearchResultMsg
		if err := c.conn.Call(rpcSearchHistory, searchHistoryParams{AccountIdx: accountIdx, To: to, Query: query}, &msg); err != nil {
			return ui.HistorySearchResultMsg{AccountIdx: accountIdx, From: to, Query: query, Err: err}
		}
		return msg
	}
}

func (c *ipcClient) AddContact(accountIdx int, address string) tea.Msg {
	var msg ui.ContactAddedMsg
	if err := c.conn.Call(rpcAddContact, contactParams{AccountIdx: accountIdx, Address: address}, &msg); err != nil {
		return ui.ContactAddedMsg{AccountIdx: accountIdx, Address: address, Err: err}
	}
	return msg
}

func (c *ipcClient) RemoveContact(accountIdx int, address string) tea.Msg {
	var msg ui.ContactRemovedMsg
	if err := c.conn.Call(rpcRemoveContact, contactParams{AccountIdx: accountIdx, Address: address}, &msg); err != nil {
		return ui.ContactRemovedMsg{AccountIdx: accountIdx, Address: address, Err: err}
	}
	return msg
}

func (c *ipcClient) ResubscribeContact(accountIdx int, address string) tea.Msg {
	var msg ui.ContactResubscribedMsg
	if err := c.conn.Call(rpcResubscribeContact, contactParams{AccountIdx: accountIdx, Address: address}, &msg); err != nil {
		return ui.ContactResubscribedMsg{AccountIdx: accountIdx, Address: address, Err: err}
	}
	return msg
}

func (c *ipcClient) AddAccount(jid, password, gpgKeyID string, register bool) tea.Msg {
	var w wireAccount
	if err := c.conn.Call(rpcAddAccount, addAccountParams{JID: jid, Password: password, GPGKeyID: gpgKeyID, Register: register}, &w); err != nil {
		return ui.AccountAddErrorMsg{Err: err}
	}
	return ui.AccountAddedMsg{Account: w.toAccount()}
}

func (c *ipcClient) RemoveAccount(accountIdx int) tea.Msg {
	if err := c.conn.Call(rpcRemoveAccount, accountIdxParams{AccountIdx: accountIdx}, nil); err != nil {
		return ui.AccountRemoveErrorMsg{Index: accountIdx, Err: err}
	}
	return ui.AccountRemovedMsg{Index: accountIdx}
}

func (c *ipcClient) SetAccountStatus(accountIdx int, status ui.Presence) tea.Msg {
	var res setAccountStatusResult
	if err := c.conn.Call(rpcSetAccountStatus, setAccountStatusParams{AccountIdx: accountIdx, Status: status}, &res); err != nil {
		return ui.AccountStatusSetMsg{Index: accountIdx, Status: status, Err: err}
	}
	return ui.AccountStatusSetMsg{
		Index: accountIdx, Status: res.Status,
		NewChats: chatsFromWire(res.NewChats), NewMessages: res.NewMessages, NewHistoryMore: res.NewHistoryMore,
	}
}

func (c *ipcClient) FetchOwnDeviceList(accountIdx int) tea.Msg {
	var msg ui.OmemoDeviceListMsg
	if err := c.conn.Call(rpcFetchOwnDeviceList, accountIdxParams{AccountIdx: accountIdx}, &msg); err != nil {
		return ui.OmemoDeviceListMsg{AccountIdx: accountIdx, Err: err}
	}
	return msg
}

func (c *ipcClient) PurgeOwnDeviceList(accountIdx int, keep []ui.OmemoDevice) tea.Msg {
	var msg ui.OmemoDevicePurgedMsg
	if err := c.conn.Call(rpcPurgeOwnDeviceList, purgeOwnDeviceListParams{AccountIdx: accountIdx, Keep: keep}, &msg); err != nil {
		return ui.OmemoDevicePurgedMsg{AccountIdx: accountIdx, Err: err}
	}
	return msg
}

// StartCall/AnswerCall/HangupCall/RejectCall drive the daemon's voice call
// state machine (callsession.go), implementing ui.CallController. The RPC
// only reports whether the request reached/was accepted by the daemon - the
// call's actual state transitions arrive separately via evCallState/
// evIncomingCall broadcasts, handled in dispatch below.
func (c *ipcClient) StartCall(accountIdx int, to string) tea.Msg {
	err := c.conn.Call(rpcStartCall, startCallParams{AccountIdx: accountIdx, To: to}, nil)
	return ui.CallActionResultMsg{Action: "start", AccountIdx: accountIdx, Err: err}
}

func (c *ipcClient) StartVideoCall(accountIdx int, to string, useCamera bool) tea.Msg {
	err := c.conn.Call(rpcStartVideoCall, startVideoCallParams{AccountIdx: accountIdx, To: to, UseCamera: useCamera}, nil)
	return ui.CallActionResultMsg{Action: "start", AccountIdx: accountIdx, Err: err}
}

func (c *ipcClient) AnswerCall(accountIdx int) tea.Msg {
	err := c.conn.Call(rpcAnswerCall, accountIdxParams{AccountIdx: accountIdx}, nil)
	return ui.CallActionResultMsg{Action: "answer", AccountIdx: accountIdx, Err: err}
}

func (c *ipcClient) HangupCall(accountIdx int) tea.Msg {
	err := c.conn.Call(rpcHangupCall, accountIdxParams{AccountIdx: accountIdx}, nil)
	return ui.CallActionResultMsg{Action: "hangup", AccountIdx: accountIdx, Err: err}
}

func (c *ipcClient) RejectCall(accountIdx int) tea.Msg {
	err := c.conn.Call(rpcRejectCall, accountIdxParams{AccountIdx: accountIdx}, nil)
	return ui.CallActionResultMsg{Action: "reject", AccountIdx: accountIdx, Err: err}
}

func (c *ipcClient) MuteCall(accountIdx int, muted bool) tea.Msg {
	err := c.conn.Call(rpcMuteCall, muteCallParams{AccountIdx: accountIdx, Muted: muted}, nil)
	return ui.CallActionResultMsg{Action: "mute", AccountIdx: accountIdx, Err: err}
}

func (c *ipcClient) ScreenShare(accountIdx int, sharing, useCamera bool) tea.Msg {
	err := c.conn.Call(rpcScreenShare, screenShareParams{AccountIdx: accountIdx, Sharing: sharing, UseCamera: useCamera}, nil)
	return ui.CallActionResultMsg{Action: "screenshare", AccountIdx: accountIdx, Err: err}
}

func (c *ipcClient) ReopenVideo(accountIdx int) tea.Msg {
	err := c.conn.Call(rpcReopenVideo, reopenVideoParams{AccountIdx: accountIdx}, nil)
	return ui.CallActionResultMsg{Action: "reopenvideo", AccountIdx: accountIdx, Err: err}
}

// listAccounts is the bootstrap call used once at startup, before ui.New,
// to get every configured account's current state (not part of any ui
// interface - main calls it directly).
func (c *ipcClient) listAccounts() ([]ui.Account, error) {
	var wires []wireAccount
	if err := c.conn.Call(rpcListAccounts, nil, &wires); err != nil {
		return nil, err
	}
	out := make([]ui.Account, len(wires))
	for i, w := range wires {
		out[i] = w.toAccount()
	}
	return out, nil
}

// getCallState queries whatever call is currently in progress on any
// account, if any - called once right after connecting so a (re)launched TUI
// can show the persistent call bar immediately instead of waiting for the
// next live transition. Returns nil, nil when no call is up.
func (c *ipcClient) getCallState() (*ui.CallStateMsg, error) {
	var res getCallStateResult
	if err := c.conn.Call(rpcGetCallState, nil, &res); err != nil {
		return nil, err
	}
	if !res.Active {
		return nil, nil
	}
	return &ui.CallStateMsg{
		AccountIdx: res.AccountIdx, Peer: res.Peer, SID: res.SID, State: res.State,
		Reason: res.Reason, Muted: res.Muted, Quality: res.Quality, Sharing: res.Sharing, StartedAt: res.StartedAt,
		SAS: res.SAS, FingerprintChanged: res.FingerprintChanged,
	}, nil
}

// sendEvent unmarshals ev's payload as T and forwards it to the program
// unchanged - the direct replacement for every p.Send(ui.SomeMsg{...}) call
// that used to live in this process when it dialed xmpp itself.
func sendEvent[T any](c *ipcClient, data json.RawMessage) {
	var m T
	if err := json.Unmarshal(data, &m); err != nil {
		slog.Warn("unmarshaling event", "err", err)
		return
	}
	c.program.Send(m)
}

// handleEvent is passed to ipc.Dial as the onEvent callback: it runs on the
// Conn's own read loop, so it must never block - it only enqueues onto
// events, which dispatchLoop drains on its own goroutine.
func (c *ipcClient) handleEvent(ev ipc.Event) {
	select {
	case c.events <- ev:
	default:
		slog.Warn("ipc event queue full, dropping event", "kind", ev.Kind)
	}
}

// dispatch unmarshals and forwards one event to the program. Runs only on
// dispatchLoop's goroutine, never on the read loop, and only ever once
// c.program is known to be set - see pending.
func (c *ipcClient) dispatch(ev ipc.Event) {
	switch ev.Kind {
	case evIncomingMessage:
		sendEvent[ui.IncomingMessageMsg](c, ev.Data)
	case evMessageCorrected:
		sendEvent[ui.MessageCorrectedMsg](c, ev.Data)
	case evMessageRetracted:
		sendEvent[ui.MessageRetractedMsg](c, ev.Data)
	case evMessageDelivered:
		sendEvent[ui.MessageDeliveredMsg](c, ev.Data)
	case evMessageServerAcked:
		sendEvent[ui.MessageServerAckedMsg](c, ev.Data)
	case evMessageSendResolved:
		sendEvent[ui.MessageSendResolvedMsg](c, ev.Data)
	case evOutboxDeleted:
		sendEvent[ui.OutboxDeletedMsg](c, ev.Data)
	case evMessageReactions:
		sendEvent[ui.MessageReactionsMsg](c, ev.Data)
	case evPresence:
		sendEvent[ui.PresenceMsg](c, ev.Data)
	case evDeviceName:
		sendEvent[ui.DeviceNameMsg](c, ev.Data)
	case evTyping:
		sendEvent[ui.TypingMsg](c, ev.Data)
	case evFileTransferProgress:
		sendEvent[ui.FileTransferProgressMsg](c, ev.Data)
	case evHistorySynced:
		sendEvent[ui.HistorySyncedMsg](c, ev.Data)
	case evHistorySyncStarted:
		sendEvent[ui.HistorySyncStartedMsg](c, ev.Data)
	case evHistorySyncFinished:
		sendEvent[ui.HistorySyncFinishedMsg](c, ev.Data)
	case evIncomingCall:
		sendEvent[ui.IncomingCallMsg](c, ev.Data)
	case evCallState:
		sendEvent[ui.CallStateMsg](c, ev.Data)
	case evMissedCall:
		sendEvent[ui.MissedCallMsg](c, ev.Data)
	case evAccountConnected:
		var w wireAccountConnectedMsg
		if err := json.Unmarshal(ev.Data, &w); err != nil {
			slog.Warn("unmarshaling AccountConnected event", "err", err)
			return
		}
		c.program.Send(ui.AccountConnectedMsg{Index: w.Index, Account: w.Account.toAccount()})
	case evAccountLive:
		var w wireAccountLiveMsg
		if err := json.Unmarshal(ev.Data, &w); err != nil {
			slog.Warn("unmarshaling AccountLive event", "err", err)
			return
		}
		c.program.Send(ui.AccountLiveMsg{Index: w.Index, NewChats: chatsFromWire(w.NewChats), NewMessages: w.NewMessages, NewHistoryMore: w.NewHistoryMore})
	case evAccountConnectError:
		var w wireAccountConnectErrorMsg
		if err := json.Unmarshal(ev.Data, &w); err != nil {
			slog.Warn("unmarshaling AccountConnectError event", "err", err)
			return
		}
		var err error
		if w.Err != "" {
			err = errors.New(w.Err)
		}
		c.program.Send(ui.AccountConnectErrorMsg{Index: w.Index, Err: err})
	}
}
