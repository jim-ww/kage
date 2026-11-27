package main

import (
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/version"
)

// request is one call the TUI makes to the daemon, answered with an R. Each
// is a struct of its arguments; run is the daemon side of it.
type request[R any] interface {
	run(d *daemonServer, client ipc.ClientID) (R, error)
}

// none is the reply of a call that only succeeds or fails.
type none struct{}

// wireError carries an error inside an event across the socket; gob can only
// send registered concrete types.
type wireError string

func (e wireError) Error() string { return string(e) }

// handlers maps each call's type to its daemon side.
var handlers = map[reflect.Type]func(d *daemonServer, client ipc.ClientID, c any) (any, error){}

// wireTypes is every type that crosses the socket, for protocolHash.
var wireTypes []reflect.Type

// handle registers c's type as a request and its reply type for gob.
func handle[R any](c request[R]) {
	var r R
	gob.Register(c)
	gob.Register(r)
	wireTypes = append(wireTypes, reflect.TypeOf(c), reflect.TypeOf(&r).Elem())
	handlers[reflect.TypeOf(c)] = func(d *daemonServer, client ipc.ClientID, c any) (any, error) {
		return c.(request[R]).run(d, client)
	}
}

// serve is the ipc.Handler passed to ipc.Server.Accept.
func (d *daemonServer) serve(client ipc.ClientID, c any) (any, error) {
	h, ok := handlers[reflect.TypeOf(c)]
	if !ok {
		return nil, fmt.Errorf("unknown call %T", c)
	}
	return h(d, client, c)
}

// do makes request c over the TUI's connection to the daemon.
func do[R any](conn *ipc.Conn, c request[R]) (R, error) {
	reply, err := conn.Call(c)
	r, _ := reply.(R)
	return r, err
}

// events are the messages the daemon broadcasts to every attached TUI.
var events = []any{
	ui.IncomingMessageMsg{}, ui.MessageCorrectedMsg{}, ui.MessageRetractedMsg{},
	ui.MessageDeliveredMsg{}, ui.MessageServerAckedMsg{}, ui.MessageSendFailedMsg{},
	ui.MessageSendResolvedMsg{}, ui.OutboxDeletedMsg{}, ui.MessagePurgedMsg{},
	ui.MessageReactionsMsg{}, ui.PresenceMsg{}, ui.DeviceNameMsg{}, ui.TypingMsg{},
	ui.FileTransferProgressMsg{}, ui.FileTransferDoneMsg{}, ui.ChatAddedMsg{},
	ui.ChatRemovedMsg{}, ui.ChatSettingMsg{}, ui.ChatUnreadMsg{}, ui.ChatDraftMsg{}, ui.AvatarUpdatedMsg{},
	ui.AccountAppearedMsg{}, ui.AccountRemovedMsg{}, ui.AccountConnectedMsg{},
	ui.AccountLiveMsg{}, ui.AccountConnectErrorMsg{}, ui.AccountStatusChangedMsg{},
	ui.HistorySyncedMsg{}, ui.HistorySyncStartedMsg{}, ui.HistorySyncFinishedMsg{},
	ui.IncomingCallMsg{}, ui.CallStateMsg{}, ui.MissedCallMsg{},
	ui.MessageDecryptedMsg{}, ui.GPGLockedMsg{}, ui.AttachmentCachedMsg{},
	ui.RoomStateMsg{}, ui.RoomInviteMsg{},
}

// isEvent reports whether ev's type is registered as an event.
var isEvent = map[reflect.Type]bool{}

func init() {
	gob.Register(ui.Chat{}) // list.Item, in Account.Chats and friends
	gob.Register(wireError(""))
	for _, ch := range []ui.StateChange{ui.SidebarWidth(0), ui.SidebarHidden(false), ui.InputHeight(0),
		ui.FilePickerSort{}, ui.LastChat{}, ui.DefaultAccount(""), ui.ReactionsSent(nil),
		ui.DesktopAudio(false), ui.DesktopAudioVolume(0), ui.AudioDeviceChoice{}} {
		gob.Register(ch)
		wireTypes = append(wireTypes, reflect.TypeOf(ch))
	}
	for _, a := range []ui.CallAction{ui.CallStart{}, ui.CallAnswer{}, ui.CallReject{},
		ui.CallHangup{}, ui.CallMute{}, ui.CallShareVideo{}, ui.CallReopenVideo{}} {
		gob.Register(a)
		wireTypes = append(wireTypes, reflect.TypeOf(a))
	}
	for _, ev := range events {
		gob.Register(ev)
		isEvent[reflect.TypeOf(ev)] = true
		wireTypes = append(wireTypes, reflect.TypeOf(ev))
	}

	handle(versionReq{})
	handle(sendReq{})
	handle(markRetractedReq{})
	handle(deleteQueuedReq{})
	handle(purgeMessageReq{})
	handle(setTypingReq{})
	handle(sendFileReq{})
	handle(uploadFileReq{})
	handle(cancelUploadReq{})
	handle(loadHistoryWindowReq{})
	handle(searchHistoryReq{})
	handle(chatFilesReq{})
	handle(messageSlotsReq{})
	handle(renameContactReq{})
	handle(addContactReq{})
	handle(removeContactReq{})
	handle(setBlockedReq{})
	handle(joinRoomReq{})
	handle(leaveRoomReq{})
	handle(startChatReq{})
	handle(setRoomNickReq{})
	handle(setRoomSubjectReq{})
	handle(resubscribeContactReq{})
	handle(resyncChatReq{})
	handle(setChatFlagReq{})
	handle(setChatEncryptionReq{})
	handle(resetChatUnreadReq{})
	handle(chatUnreadCountsReq{})
	handle(saveDraftReq{})
	handle(retryGPGDecryptReq{})
	handle(listAccountsReq{})
	handle(addAccountReq{})
	handle(removeAccountReq{})
	handle(setAccountStatusReq{})
	handle(setOwnAvatarReq{})
	handle(removeOwnAvatarReq{})
	handle(syncOwnAvatarReq{})
	handle(fetchOwnDeviceListReq{})
	handle(purgeOwnDeviceListReq{})
	handle(changeStoragePasswordReq{})
	handle(saveStateReq{})
	handle(setFocusStateReq{})
	handle(callStateReq{})
	handle(callActionReq{})
	handle(listAudioDevicesReq{})

	thisBuild = buildVersion{Version: version.Version, Protocol: protocolHash()}
}

// ── Version ─────────────────────────────────────────────────────────────

// versionReq asks the daemon which build it is, so a TUI talking to a
// different one can say so. Never rename it or change its reply: an older
// or newer daemon must still understand it.
type versionReq struct{}

// buildVersion identifies a build: its release, and a hash of every type
// that crosses the socket, which tells apart two "dev" builds that would
// misunderstand each other.
type buildVersion struct {
	Version  string
	Protocol string
}

var thisBuild buildVersion

func (versionReq) run(*daemonServer, ipc.ClientID) (buildVersion, error) {
	return thisBuild, nil
}

func (v buildVersion) String() string { return v.Version + "/" + v.Protocol }

// protocolHash hashes the shape (gob-visible fields, recursively) of every
// wire type.
func protocolHash() string {
	descs := make([]string, len(wireTypes))
	for i, t := range wireTypes {
		descs[i] = describeType(t, map[reflect.Type]bool{})
	}
	slices.Sort(descs)
	sum := sha256.Sum256([]byte(strings.Join(descs, "\n")))
	return hex.EncodeToString(sum[:6])
}

var gobEncoder = reflect.TypeFor[gob.GobEncoder]()

func describeType(t reflect.Type, seen map[reflect.Type]bool) string {
	if t.Implements(gobEncoder) || reflect.PointerTo(t).Implements(gobEncoder) {
		return t.String()
	}
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return t.Kind().String() + "(" + describeType(t.Elem(), seen) + ")"
	case reflect.Map:
		return "map(" + describeType(t.Key(), seen) + "," + describeType(t.Elem(), seen) + ")"
	case reflect.Struct:
		if seen[t] {
			return t.String()
		}
		seen[t] = true
		var b strings.Builder
		b.WriteString(t.String() + "{")
		for f := range t.Fields() {
			if !f.IsExported() || f.Type.Kind() == reflect.Func || f.Type.Kind() == reflect.Chan {
				continue
			}
			b.WriteString(f.Name + " " + describeType(f.Type, seen) + ";")
		}
		b.WriteString("}")
		return b.String()
	}
	return t.String()
}

// ── Messages ────────────────────────────────────────────────────────────

type sendReq struct {
	AccountIdx int
	To, Body   string
	Opts       ui.SendOptions
}

// sendResult's Queued carries ui.ErrQueued across the socket, where errors
// travel only as their text.
type sendResult struct {
	ID     string
	Queued bool
}

func (c sendReq) run(d *daemonServer, _ ipc.ClientID) (sendResult, error) {
	id, err := d.a.Send(c.AccountIdx, c.To, c.Body, c.Opts)
	if errors.Is(err, ui.ErrQueued) {
		return sendResult{Queued: true}, nil
	}
	return sendResult{ID: id}, err
}

type markRetractedReq struct {
	AccountIdx int
	To, ID     string
}

func (c markRetractedReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.MarkRetracted(c.AccountIdx, c.To, c.ID)
}

type deleteQueuedReq struct {
	AccountIdx int
	LocalID    string
}

func (c deleteQueuedReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.DeleteQueued(c.AccountIdx, c.LocalID)
}

type purgeMessageReq struct {
	AccountIdx int
	To, ID     string
}

func (c purgeMessageReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.PurgeMessage(c.AccountIdx, c.To, c.ID)
}

type setTypingReq struct {
	AccountIdx int
	To         string
	Composing  bool
}

func (c setTypingReq) run(d *daemonServer, client ipc.ClientID) (none, error) {
	return none{}, d.a.SetTyping(client, c.AccountIdx, c.To, c.Composing)
}

// ── Files ───────────────────────────────────────────────────────────────

type sendFileReq struct {
	AccountIdx int
	To, Path   string
	Opts       ui.SendOptions
}

func (c sendFileReq) run(d *daemonServer, _ ipc.ClientID) (ui.FileSendResultMsg, error) {
	return d.a.SendFile(c.AccountIdx, c.To, c.Path, c.Opts)
}

type uploadFileReq struct {
	AccountIdx     int
	To, Path, Text string
	Opts           ui.SendOptions
}

func (c uploadFileReq) run(d *daemonServer, _ ipc.ClientID) (ui.FileUploadResultMsg, error) {
	return d.a.UploadFile(c.AccountIdx, c.To, c.Path, c.Text, c.Opts)
}

type cancelUploadReq struct{ Path string }

func (c cancelUploadReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	d.a.CancelUpload(c.Path)
	return none{}, nil
}

// ── History ─────────────────────────────────────────────────────────────

type loadHistoryWindowReq struct {
	AccountIdx int
	To         string
	Anchor     *ui.HistoryAnchor
}

func (c loadHistoryWindowReq) run(d *daemonServer, _ ipc.ClientID) (ui.HistoryWindowMsg, error) {
	return d.a.LoadHistoryWindow(c.AccountIdx, c.To, c.Anchor)
}

type searchHistoryReq struct {
	AccountIdx int
	To, Query  string
}

func (c searchHistoryReq) run(d *daemonServer, _ ipc.ClientID) (ui.HistorySearchResultMsg, error) {
	return d.a.SearchHistory(c.AccountIdx, c.To, c.Query)
}

type chatFilesReq struct {
	AccountIdx int
	To         string
}

func (c chatFilesReq) run(d *daemonServer, _ ipc.ClientID) (ui.ChatFilesMsg, error) {
	return d.a.ChatFiles(c.AccountIdx, c.To)
}

type messageSlotsReq struct {
	AccountIdx int
	To         string
}

func (c messageSlotsReq) run(d *daemonServer, _ ipc.ClientID) ([]int64, error) {
	return d.a.MessageSlots(c.AccountIdx, c.To)
}

// ── Contacts and chats ──────────────────────────────────────────────────

type renameContactReq struct {
	AccountIdx    int
	Address, Name string
}

func (c renameContactReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.RenameContact(c.AccountIdx, c.Address, c.Name)
}

type addContactReq struct {
	AccountIdx int
	Address    string
}

func (c addContactReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.AddContact(c.AccountIdx, c.Address)
}

type removeContactReq struct {
	AccountIdx int
	Address    string
}

func (c removeContactReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.RemoveContact(c.AccountIdx, c.Address)
}

type setBlockedReq struct {
	AccountIdx int
	Address    string
	Blocked    bool
}

func (c setBlockedReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.SetBlocked(c.AccountIdx, c.Address, c.Blocked)
}

type joinRoomReq struct {
	AccountIdx           int
	Room, Nick, Password string
}

func (c joinRoomReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.JoinRoom(c.AccountIdx, c.Room, c.Nick, c.Password)
}

type leaveRoomReq struct {
	AccountIdx int
	Room       string
}

func (c leaveRoomReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.LeaveRoom(c.AccountIdx, c.Room)
}

type setRoomNickReq struct {
	AccountIdx int
	Room, Nick string
}

func (c setRoomNickReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.SetRoomNick(c.AccountIdx, c.Room, c.Nick)
}

type setRoomSubjectReq struct {
	AccountIdx    int
	Room, Subject string
}

func (c setRoomSubjectReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.SetRoomSubject(c.AccountIdx, c.Room, c.Subject)
}

type startChatReq struct {
	AccountIdx int
	Address    string
}

func (c startChatReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.StartChat(c.AccountIdx, c.Address)
}

type resubscribeContactReq struct {
	AccountIdx int
	Address    string
}

func (c resubscribeContactReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.ResubscribeContact(c.AccountIdx, c.Address)
}

type resyncChatReq struct {
	AccountIdx int
	Address    string
}

func (c resyncChatReq) run(d *daemonServer, _ ipc.ClientID) (int, error) {
	return d.a.ResyncChat(c.AccountIdx, c.Address)
}

type setChatFlagReq struct {
	AccountJID, ChatAddress string
	Flag                    ui.ChatSetting
	On                      bool
}

func (c setChatFlagReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.SetChatFlag(c.AccountJID, c.ChatAddress, c.Flag, c.On)
}

type setChatEncryptionReq struct {
	AccountIdx    int
	PeerJID, Mode string
}

func (c setChatEncryptionReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.SetChatEncryption(c.AccountIdx, c.PeerJID, c.Mode)
}

type resetChatUnreadReq struct{ AccountJID, ChatAddress string }

func (c resetChatUnreadReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.ResetChatUnread(c.AccountJID, c.ChatAddress)
}

type chatUnreadCountsReq struct{ AccountJID string }

func (c chatUnreadCountsReq) run(d *daemonServer, _ ipc.ClientID) (map[string]int, error) {
	return d.a.ChatUnreadCounts(c.AccountJID)
}

type retryGPGDecryptReq struct{ AccountIdx int }

func (c retryGPGDecryptReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.RetryGPGDecrypt(c.AccountIdx)
}

type saveDraftReq struct{ AccountJID, ChatAddress, Text string }

func (c saveDraftReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.SaveDraft(c.AccountJID, c.ChatAddress, c.Text)
}

// ── Accounts ────────────────────────────────────────────────────────────

// listAccountsReq is the snapshot of every configured account a TUI starts
// from.
type listAccountsReq struct{}

func (listAccountsReq) run(d *daemonServer, _ ipc.ClientID) ([]ui.Account, error) {
	return d.listAccounts(), nil
}

type addAccountReq struct {
	JID, Password, GPGKeyID string
	Register                bool
}

func (c addAccountReq) run(d *daemonServer, _ ipc.ClientID) (ui.Account, error) {
	return d.a.AddAccount(c.JID, c.Password, c.GPGKeyID, c.Register)
}

type removeAccountReq struct{ AccountIdx int }

func (c removeAccountReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.RemoveAccount(c.AccountIdx)
}

type setAccountStatusReq struct {
	AccountIdx int
	Status     ui.Presence
}

func (c setAccountStatusReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.SetAccountStatus(c.AccountIdx, c.Status)
}

type setOwnAvatarReq struct {
	AccountIdx int
	Path       string
}

func (c setOwnAvatarReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.SetOwnAvatar(c.AccountIdx, c.Path)
}

type removeOwnAvatarReq struct{ AccountIdx int }

func (c removeOwnAvatarReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.RemoveOwnAvatar(c.AccountIdx)
}

type syncOwnAvatarReq struct{ AccountIdx int }

func (c syncOwnAvatarReq) run(d *daemonServer, _ ipc.ClientID) (ownAvatar, error) {
	return d.a.SyncOwnAvatar(c.AccountIdx)
}

type fetchOwnDeviceListReq struct{ AccountIdx int }

func (c fetchOwnDeviceListReq) run(d *daemonServer, _ ipc.ClientID) (ui.OmemoDeviceListMsg, error) {
	return d.a.FetchOwnDeviceList(c.AccountIdx)
}

type purgeOwnDeviceListReq struct {
	AccountIdx int
	Keep       []ui.OmemoDevice
}

func (c purgeOwnDeviceListReq) run(d *daemonServer, _ ipc.ClientID) (ui.OmemoDevicePurgedMsg, error) {
	return d.a.PurgeOwnDeviceList(c.AccountIdx, c.Keep)
}

type changeStoragePasswordReq struct{ NewPassword string }

func (c changeStoragePasswordReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.ChangeStoragePassword(c.NewPassword)
}

// ── UI state ────────────────────────────────────────────────────────────

// saveStateReq saves a piece of UI state for the next start.
type saveStateReq struct{ Change ui.StateChange }

func (c saveStateReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.SaveState(c.Change)
}

type setFocusStateReq struct {
	AccountJID, ChatAddress string
	Focused                 bool
}

func (c setFocusStateReq) run(d *daemonServer, client ipc.ClientID) (none, error) {
	return none{}, d.a.SetFocusState(client, c.AccountJID, c.ChatAddress, c.Focused)
}

// ── Calls ───────────────────────────────────────────────────────────────

// callStateReq asks for the call in progress, if any (State "" if none), so
// a TUI attaching mid-call shows it right away.
type callStateReq struct{}

func (callStateReq) run(d *daemonServer, _ ipc.ClientID) (ui.CallStateMsg, error) {
	return d.callState(), nil
}

type callActionReq struct {
	AccountIdx int
	Action     ui.CallAction
}

func (c callActionReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.CallAction(c.AccountIdx, c.Action)
}

type listAudioDevicesReq struct{}

func (listAudioDevicesReq) run(d *daemonServer, _ ipc.ClientID) (ui.AudioDevicesMsg, error) {
	return d.a.AudioDevices()
}
