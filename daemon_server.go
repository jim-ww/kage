package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/ui"
)

// daemonServer answers RPCs from attached TUI clients by calling straight
// into adapter's existing business logic - it's a thin dispatch shim, not
// new logic. Constructed once in --background mode.
type daemonServer struct {
	a   *adapter
	srv *ipc.Server
}

func unmarshalParams[T any](params json.RawMessage) (T, error) {
	var v T
	if len(params) == 0 {
		return v, nil
	}
	err := json.Unmarshal(params, &v)
	return v, err
}

// handle is the ipc.Handler passed to ipc.Server.Accept.
func (d *daemonServer) handle(method string, params json.RawMessage) (any, error) {
	switch method {
	case rpcSend:
		p, err := unmarshalParams[sendParams](params)
		if err != nil {
			return nil, err
		}
		id, err := d.a.Send(p.AccountIdx, p.To, p.Body, p.Opts)
		if errors.Is(err, ui.ErrQueued) {
			// A queued send isn't an RPC failure - see sendResult.Queued's
			// doc comment for why this can't just be returned as err here.
			return sendResult{Queued: true}, nil
		}
		if err != nil {
			return nil, err
		}
		return sendResult{ID: id}, nil

	case rpcMarkRetracted:
		p, err := unmarshalParams[markRetractedParams](params)
		if err != nil {
			return nil, err
		}
		return nil, d.a.MarkRetracted(p.AccountIdx, p.To, p.ID)

	case rpcDeleteQueued:
		p, err := unmarshalParams[deleteQueuedParams](params)
		if err != nil {
			return nil, err
		}
		return nil, d.a.DeleteQueued(p.AccountIdx, p.LocalID)

	case rpcSetTyping:
		p, err := unmarshalParams[setTypingParams](params)
		if err != nil {
			return nil, err
		}
		return nil, d.a.SetTyping(p.AccountIdx, p.To, p.Composing)

	case rpcRenameContact:
		p, err := unmarshalParams[renameContactParams](params)
		if err != nil {
			return nil, err
		}
		return nil, d.a.RenameContact(p.AccountIdx, p.Address, p.Name)

	case rpcSetDefaultAccount:
		p, err := unmarshalParams[setDefaultAccountParams](params)
		if err != nil {
			return nil, err
		}
		return nil, d.a.SetDefaultAccount(p.JID)

	case rpcSetChatEncryption:
		p, err := unmarshalParams[setChatEncryptionParams](params)
		if err != nil {
			return nil, err
		}
		return nil, d.a.SetChatEncryption(p.AccountIdx, p.PeerJID, p.Mode)

	case rpcSetSidebarWidth:
		p, err := unmarshalParams[widthParams](params)
		if err != nil {
			return nil, err
		}
		return nil, d.a.SetSidebarWidth(p.Width)

	case rpcSetSidebarHidden:
		p, err := unmarshalParams[hiddenParams](params)
		if err != nil {
			return nil, err
		}
		return nil, d.a.SetSidebarHidden(p.Hidden)

	case rpcSetInputHeight:
		p, err := unmarshalParams[heightParams](params)
		if err != nil {
			return nil, err
		}
		return nil, d.a.SetInputHeight(p.Height)

	case rpcSetFilePickerSort:
		p, err := unmarshalParams[filePickerSortParams](params)
		if err != nil {
			return nil, err
		}
		return nil, d.a.SetFilePickerSort(p.Field, p.Ascending)

	case rpcSetLastChat:
		p, err := unmarshalParams[setLastChatParams](params)
		if err != nil {
			return nil, err
		}
		return nil, d.a.SetLastChat(p.AccountJID, p.ChatAddress)

	case rpcIncrementChatUnread:
		p, err := unmarshalParams[chatUnreadDeltaParams](params)
		if err != nil {
			return nil, err
		}
		return nil, d.a.IncrementChatUnread(p.AccountJID, p.ChatAddress, p.Delta)

	case rpcResetChatUnread:
		p, err := unmarshalParams[setLastChatParams](params)
		if err != nil {
			return nil, err
		}
		return nil, d.a.ResetChatUnread(p.AccountJID, p.ChatAddress)

	case rpcChatUnreadCounts:
		p, err := unmarshalParams[accountJIDParams](params)
		if err != nil {
			return nil, err
		}
		counts, err := d.a.ChatUnreadCounts(p.AccountJID)
		if err != nil {
			return nil, err
		}
		return chatUnreadCountsResult{Counts: counts}, nil

	case rpcSaveDraft:
		p, err := unmarshalParams[saveDraftParams](params)
		if err != nil {
			return nil, err
		}
		return nil, d.a.SaveDraft(p.AccountJID, p.ChatAddress, p.Text)

	case rpcChangeStoragePassword:
		p, err := unmarshalParams[changeStoragePasswordParams](params)
		if err != nil {
			return nil, err
		}
		return nil, d.a.ChangeStoragePassword(p.NewPassword)

	case rpcSendFile:
		p, err := unmarshalParams[sendFileParams](params)
		if err != nil {
			return nil, err
		}
		msg := d.a.SendFile(p.AccountIdx, p.To, p.Path, p.Opts).(ui.FileSendResultMsg)
		if msg.Err != nil {
			return nil, msg.Err
		}
		return msg, nil

	case rpcUploadFile:
		p, err := unmarshalParams[uploadFileParams](params)
		if err != nil {
			return nil, err
		}
		msg := d.a.UploadFile(p.AccountIdx, p.To, p.Path, p.Text, p.Opts).(ui.FileUploadResultMsg)
		if msg.Err != nil {
			return nil, msg.Err
		}
		return msg, nil

	case rpcLoadOlderHistory:
		p, err := unmarshalParams[loadOlderHistoryParams](params)
		if err != nil {
			return nil, err
		}
		cmd := d.a.LoadOlderHistory(p.AccountIdx, p.To)
		if cmd == nil {
			return nil, fmt.Errorf("unknown account %d", p.AccountIdx)
		}
		return cmd().(ui.OlderHistoryMsg), nil

	case rpcSearchHistory:
		p, err := unmarshalParams[searchHistoryParams](params)
		if err != nil {
			return nil, err
		}
		cmd := d.a.SearchHistory(p.AccountIdx, p.To, p.Query)
		if cmd == nil {
			return nil, fmt.Errorf("unknown account %d", p.AccountIdx)
		}
		return cmd().(ui.HistorySearchResultMsg), nil

	case rpcAddContact:
		p, err := unmarshalParams[contactParams](params)
		if err != nil {
			return nil, err
		}
		msg := d.a.AddContact(p.AccountIdx, p.Address).(ui.ContactAddedMsg)
		if msg.Err != nil {
			return nil, msg.Err
		}
		return msg, nil

	case rpcRemoveContact:
		p, err := unmarshalParams[contactParams](params)
		if err != nil {
			return nil, err
		}
		msg := d.a.RemoveContact(p.AccountIdx, p.Address).(ui.ContactRemovedMsg)
		if msg.Err != nil {
			return nil, msg.Err
		}
		return msg, nil

	case rpcResubscribeContact:
		p, err := unmarshalParams[contactParams](params)
		if err != nil {
			return nil, err
		}
		msg := d.a.ResubscribeContact(p.AccountIdx, p.Address).(ui.ContactResubscribedMsg)
		if msg.Err != nil {
			return nil, msg.Err
		}
		return msg, nil

	case rpcAddAccount:
		p, err := unmarshalParams[addAccountParams](params)
		if err != nil {
			return nil, err
		}
		switch msg := d.a.AddAccount(p.JID, p.Password, p.GPGKeyID, p.Register).(type) {
		case ui.AccountAddErrorMsg:
			return nil, msg.Err
		case ui.AccountAddedMsg:
			return toWireAccount(msg.Account), nil
		default:
			return nil, fmt.Errorf("unexpected AddAccount result %T", msg)
		}

	case rpcRemoveAccount:
		p, err := unmarshalParams[accountIdxParams](params)
		if err != nil {
			return nil, err
		}
		switch msg := d.a.RemoveAccount(p.AccountIdx).(type) {
		case ui.AccountRemoveErrorMsg:
			return nil, msg.Err
		case ui.AccountRemovedMsg:
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected RemoveAccount result %T", msg)
		}

	case rpcSetAccountStatus:
		p, err := unmarshalParams[setAccountStatusParams](params)
		if err != nil {
			return nil, err
		}
		msg := d.a.SetAccountStatus(p.AccountIdx, p.Status).(ui.AccountStatusSetMsg)
		if msg.Err != nil {
			return nil, msg.Err
		}
		return setAccountStatusResult{
			Status:         msg.Status,
			NewChats:       chatsToWire(msg.NewChats),
			NewMessages:    msg.NewMessages,
			NewHistoryMore: msg.NewHistoryMore,
		}, nil

	case rpcFetchOwnDeviceList:
		p, err := unmarshalParams[accountIdxParams](params)
		if err != nil {
			return nil, err
		}
		msg := d.a.FetchOwnDeviceList(p.AccountIdx).(ui.OmemoDeviceListMsg)
		if msg.Err != nil {
			return nil, msg.Err
		}
		return msg, nil

	case rpcPurgeOwnDeviceList:
		p, err := unmarshalParams[purgeOwnDeviceListParams](params)
		if err != nil {
			return nil, err
		}
		msg := d.a.PurgeOwnDeviceList(p.AccountIdx, p.Keep).(ui.OmemoDevicePurgedMsg)
		if msg.Err != nil {
			return nil, msg.Err
		}
		return msg, nil

	case rpcSetFocusState:
		p, err := unmarshalParams[setFocusStateParams](params)
		if err != nil {
			return nil, err
		}
		return nil, d.a.SetFocusState(p.AccountJID, p.ChatAddress, p.Focused)

	case rpcStartCall:
		p, err := unmarshalParams[startCallParams](params)
		if err != nil {
			return nil, err
		}
		return nil, d.a.StartCall(p.AccountIdx, p.To)

	case rpcStartVideoCall:
		p, err := unmarshalParams[startVideoCallParams](params)
		if err != nil {
			return nil, err
		}
		return nil, d.a.StartVideoCall(p.AccountIdx, p.To, p.UseCamera)

	case rpcAnswerCall:
		p, err := unmarshalParams[accountIdxParams](params)
		if err != nil {
			return nil, err
		}
		return nil, d.a.AnswerCall(p.AccountIdx)

	case rpcHangupCall:
		p, err := unmarshalParams[accountIdxParams](params)
		if err != nil {
			return nil, err
		}
		return nil, d.a.HangupCall(p.AccountIdx)

	case rpcRejectCall:
		p, err := unmarshalParams[accountIdxParams](params)
		if err != nil {
			return nil, err
		}
		return nil, d.a.RejectCall(p.AccountIdx)

	case rpcMuteCall:
		p, err := unmarshalParams[muteCallParams](params)
		if err != nil {
			return nil, err
		}
		return nil, d.a.MuteCall(p.AccountIdx, p.Muted)

	case rpcScreenShare:
		p, err := unmarshalParams[screenShareParams](params)
		if err != nil {
			return nil, err
		}
		return nil, d.a.ScreenShare(p.AccountIdx, p.Sharing, p.UseCamera)

	case rpcReopenVideo:
		p, err := unmarshalParams[reopenVideoParams](params)
		if err != nil {
			return nil, err
		}
		return nil, d.a.ReopenVideo(p.AccountIdx)

	case rpcListAccounts:
		return d.listAccounts(context.Background()), nil

	case rpcGetCallState:
		return d.getCallState(), nil
	}
	return nil, fmt.Errorf("unknown rpc method %q", method)
}

// listAccounts builds a fresh snapshot of every configured account from
// local storage (reusing connectAccountLocal - a plain disk read, no
// network) so a newly-attached client, even one attaching well after the
// daemon finished its own boot sequence, gets accurate current state instead
// of stale "connecting…" placeholders.
func (d *daemonServer) listAccounts(ctx context.Context) []wireAccount {
	d.a.mu.Lock()
	accts := append([]config.Account(nil), d.a.cfgAccounts...)
	sessions := append([]*accountSession(nil), d.a.sessions...)
	queries := d.a.queries
	localKey := d.a.localKey
	d.a.mu.Unlock()

	out := make([]wireAccount, len(accts))
	for i, acct := range accts {
		if i < len(sessions) && sessions[i] != nil {
			acct = sessions[i].account
		}
		_, uiAcct, err := connectAccountLocal(ctx, acct, queries, localKey)
		if err != nil {
			out[i] = toWireAccount(ui.Account{Name: acct.JID, Alias: acct.Alias, ConnectError: err.Error()})
			continue
		}
		uiAcct.Connecting = false
		if i < len(sessions) && sessions[i] != nil {
			if client := sessions[i].client.Load(); client != nil && !client.Closed() {
				uiAcct.Status = accountStatus(sessions[i].account.Status)
			}
			// connectAccountLocal above rebuilds a fresh roster from disk
			// (no Presence there — that's live-only state), so pull current
			// presence from the live session's roster cache, kept up to
			// date by listen()'s PresenceEvent handling.
			if live := sessions[i].roster.Load(); live != nil {
				for ci, item := range uiAcct.Chats {
					chat, ok := item.(ui.Chat)
					if !ok {
						continue
					}
					if e, ok := (*live)[chat.Address]; ok {
						chat.Presence = e.Presence
						uiAcct.Chats[ci] = chat
					}
				}
			}
		}
		out[i] = toWireAccount(uiAcct)
	}
	return out
}

// callStatePriority ranks callState for getCallState's cross-account pick:
// higher wins. An unanswered incoming ring is ranked lowest - it's the call
// the attaching user is least likely to already be dealing with, whereas a
// call actively connecting or connected is almost certainly the one they
// care about (and, if it's their own outgoing call, the one whose account
// index every call-bar key/mouse action needs to target - see rejectCall's
// pre-proceed branch for what happens when the wrong one gets picked).
func callStatePriority(s callState) int {
	switch s {
	case callConnected:
		return 5
	case callNegotiating:
		return 4
	case callRingingRemote:
		return 3
	case callProposing:
		return 2
	case callRingingLocal:
		return 1
	default:
		return 0
	}
}

// getCallState reports whichever call is currently in progress across all
// accounts, if any - see getCallStateResult. There's at most one call per
// account, but with more than one account configured, more than one can be
// active at once (e.g. two of the user's own accounts calling each other) -
// callStatePriority picks the one most likely to be what the attaching user
// actually cares about, since the UI only ever shows one call bar app-wide.
func (d *daemonServer) getCallState() getCallStateResult {
	d.a.mu.Lock()
	sessions := append([]*accountSession(nil), d.a.sessions...)
	d.a.mu.Unlock()

	var best getCallStateResult
	bestPriority := -1
	for _, sess := range sessions {
		if sess == nil {
			continue
		}
		c := sess.currentCall()
		if c == nil {
			continue
		}
		c.mu.Lock()
		state, muted, quality, sharing, connectedAt := c.state, c.muted, c.quality, c.sharing, c.connectedAt
		sas, fpChanged := c.fingerprintSAS, c.fingerprintChanged
		c.mu.Unlock()
		if state == callIdle || state == callEnded {
			continue
		}
		if p := callStatePriority(state); p > bestPriority {
			bestPriority = p
			best = getCallStateResult{
				Active: true, AccountIdx: c.accountIdx, Peer: c.peer, SID: c.sid,
				State: state.String(), Muted: muted, Quality: quality, Sharing: sharing, StartedAt: connectedAt,
				SAS: sas, FingerprintChanged: fpChanged,
			}
		}
	}
	return best
}
