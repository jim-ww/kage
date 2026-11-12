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
	ui.ChatRemovedMsg{}, ui.ChatSettingMsg{}, ui.ChatUnreadMsg{}, ui.AvatarUpdatedMsg{},
	ui.AccountAppearedMsg{}, ui.AccountRemovedMsg{}, ui.AccountConnectedMsg{},
	ui.AccountLiveMsg{}, ui.AccountConnectErrorMsg{}, ui.AccountStatusChangedMsg{},
	ui.HistorySyncedMsg{}, ui.HistorySyncStartedMsg{}, ui.HistorySyncFinishedMsg{},
	ui.IncomingCallMsg{}, ui.CallStateMsg{}, ui.MissedCallMsg{},
}

// isEvent reports whether ev's type is registered as an event.
var isEvent = map[reflect.Type]bool{}

func init() {
	gob.Register(ui.Chat{}) // list.Item, in Account.Chats and friends
	gob.Register(wireError(""))
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
	handle(renameContactReq{})
	handle(addContactReq{})
	handle(removeContactReq{})
	handle(resubscribeContactReq{})
	handle(setChatHiddenReq{})
	handle(setChatMutedReq{})
	handle(setChatPinnedReq{})
	handle(setChatEncryptionReq{})
	handle(resetChatUnreadReq{})
	handle(chatUnreadCountsReq{})
	handle(saveDraftReq{})
	handle(listAccountsReq{})
	handle(addAccountReq{})
	handle(removeAccountReq{})
	handle(setAccountStatusReq{})
	handle(setDefaultAccountReq{})
	handle(setOwnAvatarReq{})
	handle(removeOwnAvatarReq{})
	handle(fetchOwnDeviceListReq{})
	handle(purgeOwnDeviceListReq{})
	handle(changeStoragePasswordReq{})
	handle(setSidebarWidthReq{})
	handle(setSidebarHiddenReq{})
	handle(setInputHeightReq{})
	handle(setFilePickerSortReq{})
	handle(setLastChatReq{})
	handle(recordReactionEmojiUsageReq{})
	handle(setFocusStateReq{})
	handle(callStateReq{})
	handle(startCallReq{})
	handle(answerCallReq{})
	handle(hangupCallReq{})
	handle(rejectCallReq{})
	handle(muteCallReq{})
	handle(screenShareReq{})
	handle(reopenVideoReq{})
	handle(setDesktopAudioReq{})
	handle(setDesktopAudioVolumeReq{})
	handle(listAudioDevicesReq{})
	handle(setAudioDeviceReq{})

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

type resubscribeContactReq struct {
	AccountIdx int
	Address    string
}

func (c resubscribeContactReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.ResubscribeContact(c.AccountIdx, c.Address)
}

type setChatHiddenReq struct {
	AccountJID, ChatAddress string
	Hidden                  bool
}

func (c setChatHiddenReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.SetChatHidden(c.AccountJID, c.ChatAddress, c.Hidden)
}

type setChatMutedReq struct {
	AccountJID, ChatAddress string
	Muted                   bool
}

func (c setChatMutedReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.SetChatMuted(c.AccountJID, c.ChatAddress, c.Muted)
}

type setChatPinnedReq struct {
	AccountJID, ChatAddress string
	Pinned                  bool
}

func (c setChatPinnedReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.SetChatPinned(c.AccountJID, c.ChatAddress, c.Pinned)
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

type setDefaultAccountReq struct{ JID string }

func (c setDefaultAccountReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.SetDefaultAccount(c.JID)
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

type setSidebarWidthReq struct{ Width int }

func (c setSidebarWidthReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.SetSidebarWidth(c.Width)
}

type setSidebarHiddenReq struct{ Hidden bool }

func (c setSidebarHiddenReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.SetSidebarHidden(c.Hidden)
}

type setInputHeightReq struct{ Height int }

func (c setInputHeightReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.SetInputHeight(c.Height)
}

type setFilePickerSortReq struct {
	Field     string
	Ascending bool
}

func (c setFilePickerSortReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.SetFilePickerSort(c.Field, c.Ascending)
}

type setLastChatReq struct{ AccountJID, ChatAddress string }

func (c setLastChatReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.SetLastChat(c.AccountJID, c.ChatAddress)
}

type recordReactionEmojiUsageReq struct{ Emojis []string }

func (c recordReactionEmojiUsageReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.RecordReactionEmojiUsage(c.Emojis)
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

type startCallReq struct {
	AccountIdx int
	To         string
	Video      bool
	UseCamera  bool // with Video: the camera rather than the screen
}

func (c startCallReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	if c.Video {
		return none{}, d.a.StartVideoCall(c.AccountIdx, c.To, c.UseCamera)
	}
	return none{}, d.a.StartCall(c.AccountIdx, c.To)
}

type answerCallReq struct{ AccountIdx int }

func (c answerCallReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.AnswerCall(c.AccountIdx)
}

type hangupCallReq struct{ AccountIdx int }

func (c hangupCallReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.HangupCall(c.AccountIdx)
}

type rejectCallReq struct{ AccountIdx int }

func (c rejectCallReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.RejectCall(c.AccountIdx)
}

type muteCallReq struct {
	AccountIdx int
	Muted      bool
}

func (c muteCallReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.MuteCall(c.AccountIdx, c.Muted)
}

type screenShareReq struct {
	AccountIdx int
	Sharing    bool
	UseCamera  bool // ignored when Sharing is false
}

func (c screenShareReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.ScreenShare(c.AccountIdx, c.Sharing, c.UseCamera)
}

type reopenVideoReq struct{ AccountIdx int }

func (c reopenVideoReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.ReopenVideo(c.AccountIdx)
}

type setDesktopAudioReq struct{ Enabled bool }

func (c setDesktopAudioReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.SetDesktopAudio(c.Enabled)
}

type setDesktopAudioVolumeReq struct{ Percent int }

func (c setDesktopAudioVolumeReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.SetDesktopAudioVolume(c.Percent)
}

type listAudioDevicesReq struct{}

func (listAudioDevicesReq) run(d *daemonServer, _ ipc.ClientID) (ui.AudioDevicesMsg, error) {
	return d.a.AudioDevices()
}

type setAudioDeviceReq struct {
	Output bool // false: the microphone
	Name   string
}

func (c setAudioDeviceReq) run(d *daemonServer, _ ipc.ClientID) (none, error) {
	return none{}, d.a.SetAudioDevice(c.Output, c.Name)
}
