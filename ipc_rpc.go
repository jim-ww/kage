package main

import (
	"charm.land/bubbles/v2/list"
	"github.com/jim-ww/kage/ui"
)

// RPC method names (client -> daemon, see daemon_server.go/ipc_client.go).
const (
	rpcSend                  = "Send"
	rpcMarkRetracted         = "MarkRetracted"
	rpcSetTyping             = "SetTyping"
	rpcRenameContact         = "RenameContact"
	rpcSetDefaultAccount     = "SetDefaultAccount"
	rpcSetChatEncryption     = "SetChatEncryption"
	rpcSetSidebarWidth       = "SetSidebarWidth"
	rpcSetSidebarHidden      = "SetSidebarHidden"
	rpcSetInputHeight        = "SetInputHeight"
	rpcSetFilePickerSort     = "SetFilePickerSort"
	rpcSetLastChat           = "SetLastChat"
	rpcIncrementChatUnread   = "IncrementChatUnread"
	rpcResetChatUnread       = "ResetChatUnread"
	rpcChatUnreadCounts      = "ChatUnreadCounts"
	rpcSaveDraft             = "SaveDraft"
	rpcChangeStoragePassword = "ChangeStoragePassword"
	rpcSendFile              = "SendFile"
	rpcUploadFile            = "UploadFile"
	rpcLoadOlderHistory      = "LoadOlderHistory"
	rpcAddContact            = "AddContact"
	rpcRemoveContact         = "RemoveContact"
	rpcAddAccount            = "AddAccount"
	rpcRemoveAccount         = "RemoveAccount"
	rpcSetAccountStatus      = "SetAccountStatus"
	rpcFetchOwnDeviceList    = "FetchOwnDeviceList"
	rpcPurgeOwnDeviceList    = "PurgeOwnDeviceList"
	rpcListAccounts          = "ListAccounts"
	rpcSetFocusState         = "SetFocusState"
	rpcStartCall             = "StartCall"
	rpcAnswerCall            = "AnswerCall"
	rpcHangupCall            = "HangupCall"
	rpcRejectCall            = "RejectCall"
	rpcMuteCall              = "MuteCall"
)

// Event kinds (daemon -> client broadcast, see account.go/events.go/adapter.go's
// broadcast() call sites and ipc_client.go's handleEvent).
const (
	evIncomingMessage      = "IncomingMessage"
	evMessageCorrected     = "MessageCorrected"
	evMessageRetracted     = "MessageRetracted"
	evMessageDelivered     = "MessageDelivered"
	evMessageReactions     = "MessageReactions"
	evPresence             = "Presence"
	evTyping               = "Typing"
	evFileTransferProgress = "FileTransferProgress"
	evAccountConnected     = "AccountConnected"
	evAccountLive          = "AccountLive"
	evAccountConnectError  = "AccountConnectError"
	evHistorySynced        = "HistorySynced"
	evHistorySyncStarted   = "HistorySyncStarted"
	evHistorySyncFinished  = "HistorySyncFinished"
	evIncomingCall         = "IncomingCall"
	evCallState            = "CallState"
	evMissedCall           = "MissedCall"
)

// --- RPC param/result structs. Kept plain and JSON-friendly on purpose:
// most ui.*Msg result types round-trip as-is (a nil error field just
// marshals as null), so only methods whose real result needs extra fields
// the caller doesn't already know get one of these. ---

type sendParams struct {
	AccountIdx int
	To, Body   string
	Opts       ui.SendOptions
}
type sendResult struct{ ID string }

type markRetractedParams struct {
	AccountIdx int
	To, ID     string
}
type setTypingParams struct {
	AccountIdx int
	To         string
	Composing  bool
}
type renameContactParams struct {
	AccountIdx    int
	Address, Name string
}
type setDefaultAccountParams struct{ JID string }
type setChatEncryptionParams struct {
	AccountIdx    int
	PeerJID, Mode string
}
type widthParams struct{ Width int }
type hiddenParams struct{ Hidden bool }
type heightParams struct{ Height int }
type filePickerSortParams struct {
	Field     string
	Ascending bool
}
type setLastChatParams struct{ AccountJID, ChatAddress string }
type chatUnreadDeltaParams struct {
	AccountJID, ChatAddress string
	Delta                   int
}
type accountJIDParams struct{ AccountJID string }
type chatUnreadCountsResult struct{ Counts map[string]int }
type saveDraftParams struct{ AccountJID, ChatAddress, Text string }
type changeStoragePasswordParams struct{ NewPassword string }

type sendFileParams struct {
	AccountIdx int
	To, Path   string
	Opts       ui.SendOptions
}
type uploadFileParams struct {
	AccountIdx int
	To, Path   string
}
type loadOlderHistoryParams struct {
	AccountIdx int
	To         string
}
type contactParams struct {
	AccountIdx int
	Address    string
}
type addAccountParams struct{ JID, Password, GPGKeyID string }
type accountIdxParams struct{ AccountIdx int }
type setAccountStatusParams struct {
	AccountIdx int
	Status     ui.Presence
}
type setAccountStatusResult struct {
	Status         ui.Presence
	NewChats       []ui.Chat
	NewMessages    map[int][]ui.Message
	NewHistoryMore map[int]bool
}
type setFocusStateParams struct {
	AccountJID, ChatAddress string
	Focused                 bool
}
type startCallParams struct {
	AccountIdx int
	To         string
}
type muteCallParams struct {
	AccountIdx int
	Muted      bool
}

// incomingCallEvent tells attached clients that a peer is ringing us
// (XEP-0353 propose). The daemon has already answered <ringing/>; nothing
// further happens until an AnswerCall or RejectCall RPC comes back.
type incomingCallEvent struct {
	AccountIdx int
	From       string // bare JID
	SID        string
	Media      string // "audio"
}

// callStateEvent reports every transition of the account's current call. See
// callState.String in callsession.go for the State values, plus "failed" for
// an error teardown; Reason is free text for the UI to show. Muted/Quality
// ride along on every broadcast (not just ones that changed them) so the UI
// never has to remember them across a msg that reset the rest of the state -
// see callSession.broadcastState.
type callStateEvent struct {
	AccountIdx int
	Peer       string // bare JID
	SID        string
	State      string
	Reason     string
	Muted      bool
	Quality    string // "", "good", "fair", "poor" - "" until the first sample lands
}

// missedCallEvent tells attached clients that a peer proposed a call while
// this account already had one in progress - the daemon auto-rejected it
// (see handlePropose's busy branch) rather than offering call waiting.
type missedCallEvent struct {
	AccountIdx int
	From       string // bare JID
	SID        string
}

type purgeOwnDeviceListParams struct {
	AccountIdx int
	Keep       []ui.OmemoDevice
}

// --- list.Item can't be unmarshaled directly (it's an interface) - every
// ui.Account/ui.Chat crossing the wire goes through these instead. ---

func chatsToWire(items []list.Item) []ui.Chat {
	if items == nil {
		return nil
	}
	out := make([]ui.Chat, len(items))
	for i, it := range items {
		out[i] = it.(ui.Chat)
	}
	return out
}

func chatsFromWire(chats []ui.Chat) []list.Item {
	if chats == nil {
		return nil
	}
	out := make([]list.Item, len(chats))
	for i, c := range chats {
		out[i] = c
	}
	return out
}

type wireAccount struct {
	ui.Account
	Chats []ui.Chat // shadows the embedded Account.Chats field for JSON (Go's shallower-field-wins rule applies to both)
}

func toWireAccount(a ui.Account) wireAccount {
	return wireAccount{Account: a, Chats: chatsToWire(a.Chats)}
}

func (w wireAccount) toAccount() ui.Account {
	a := w.Account
	a.Chats = chatsFromWire(w.Chats)
	return a
}

type wireAccountConnectedMsg struct {
	Index   int
	Account wireAccount
}
type wireAccountLiveMsg struct {
	Index          int
	NewChats       []ui.Chat
	NewMessages    map[int][]ui.Message
	NewHistoryMore map[int]bool
}
type wireAccountConnectErrorMsg struct {
	Index int
	Err   string
}
