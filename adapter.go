package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jim-ww/kage/call"
	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/crypto/aesgcm"
	"github.com/jim-ww/kage/daemon"
	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/xmpp"
	omemolib "github.com/jim-ww/omemo-go"
)

// adapter is what the daemon runs the TUI's calls against (see ipc_rpc.go).
// sessions is only appended to after startup, so indices stay stable; mu
// guards it.
type adapter struct {
	mu          sync.Mutex
	sessions    []*accountSession
	cfgAccounts []config.Account // mirrors sessions, but populated for an index before that account finishes connecting - see daemonServer.listAccounts
	cfgPath     string
	srv         *ipc.Server
	db          *sql.DB // raw handle alongside queries, needed for the transaction ChangeStoragePassword runs
	queries     *storage.Queries
	localKey    []byte
	useGPG      bool
	useKeyring  bool

	uploadCancelsMu sync.Mutex
	uploadCancels   map[string]context.CancelFunc // keyed by local file path, see SendFile/UploadFile/CancelUpload

	typingMu sync.Mutex
	typingTo map[ipc.ClientID]map[int]string // client -> accountIdx -> peer it has sent "composing" to and not yet cleared, see SetTyping/clearTyping
}

// errUploadCanceled is an upload's error after CancelUpload.
var errUploadCanceled = errors.New("upload canceled")

// beginUpload registers a context CancelUpload(path) cancels. done
// unregisters it once the upload is over.
func (a *adapter) beginUpload(path string) (ctx context.Context, done func()) {
	ctx, cancel := context.WithCancel(context.Background())
	a.uploadCancelsMu.Lock()
	if a.uploadCancels == nil {
		a.uploadCancels = make(map[string]context.CancelFunc)
	}
	a.uploadCancels[path] = cancel
	a.uploadCancelsMu.Unlock()
	return ctx, func() {
		a.uploadCancelsMu.Lock()
		delete(a.uploadCancels, path)
		a.uploadCancelsMu.Unlock()
		cancel()
	}
}

// CancelUpload aborts the upload of path, if it's still running.
func (a *adapter) CancelUpload(path string) {
	a.uploadCancelsMu.Lock()
	cancel := a.uploadCancels[path]
	a.uploadCancelsMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// AddAccount stores the password, saves the account to config.toml, connects
// and supervises it - registering it on the server first (XEP-0077) when
// register is set.
func (a *adapter) AddAccount(jid, password, gpgKeyID string, register bool) (ui.Account, error) {
	// Checked before registering too: that would create the account on the
	// server only for saving it to fail afterwards.
	if exists, err := config.AccountExists(a.cfgPath, jid); err != nil {
		return ui.Account{}, fmt.Errorf("reading config: %w", err)
	} else if exists {
		return ui.Account{}, config.ErrAccountExists
	}
	if register {
		if err := xmpp.Register(context.Background(), jid, password, nil); err != nil {
			return ui.Account{}, fmt.Errorf("registering account: %w", err)
		}
	}

	acct := config.Account{JID: jid, GPGKeyID: gpgKeyID}
	if password != "" {
		// The OS keyring if available, else plaintext in config.toml.
		keyringErr := fmt.Errorf("use_keyring is disabled")
		if a.useKeyring {
			keyringErr = config.SetKeyringPassword(jid, password)
		}
		if keyringErr != nil {
			acct.Password = password
			slog.Warn("storing password in keyring; falling back to plaintext in config", "jid", jid, "err", keyringErr)
		}
	}
	if err := config.WriteAccount(a.cfgPath, acct); err != nil {
		return ui.Account{}, fmt.Errorf("saving account to config: %w", err)
	}

	ctx := context.Background()
	sess, uiAcct, err := connectAccount(ctx, acct, a.queries, a.localKey, a.useGPG, a.useKeyring)
	if err != nil {
		return ui.Account{}, err
	}

	a.mu.Lock()
	accountIdx := len(a.sessions)
	sess.accountIdx = accountIdx
	a.sessions = append(a.sessions, sess)
	a.cfgAccounts = append(a.cfgAccounts, acct)
	a.mu.Unlock()

	go superviseAccount(ctx, a.srv, a, accountIdx, sess)

	// To every TUI: indices are positional, and one still on the shorter
	// list would misplace its next added account.
	broadcast(a.srv, ui.AccountAppearedMsg{Index: accountIdx, Account: uiAcct})

	return uiAcct, nil
}

// SetAccountStatus saves an account's own status and applies it: offline
// disconnects, anything else connects if needed and sets <show/>.
func (a *adapter) SetAccountStatus(accountIdx int, status ui.Presence) error {
	sess, err := a.session(accountIdx)
	if err != nil {
		return err
	}
	if err := config.SetAccountStatus(a.cfgPath, sess.account.JID, presenceNames[status]); err != nil {
		return fmt.Errorf("persisting status: %w", err)
	}
	sess.account.Status = presenceNames[status]
	// Best-effort reload of config.toml (see daemon.SignalReload).
	if err := daemon.SignalReload(); err != nil {
		slog.Warn("signaling daemon to reload after status change", "err", err)
	}

	ctx := context.Background()
	client, offlineErr := sess.liveClient()
	online := offlineErr == nil

	slog.Debug("own status changed", "jid", sess.account.JID, "status", status, "was_connected", online)

	if status == ui.PresenceOffline {
		if online {
			if err := client.Close(); err != nil {
				slog.Warn("closing while going offline", "jid", sess.account.JID, "err", err)
			}
		}
		broadcast(a.srv, ui.AccountStatusChangedMsg{Index: accountIdx, Status: status})
		return nil
	}

	if status == ui.PresenceInvisible && online {
		if err := client.SetInvisible(ctx); err != nil {
			return fmt.Errorf("updating presence: %w", err)
		}
		broadcast(a.srv, ui.AccountStatusChangedMsg{Index: accountIdx, Status: status})
		return nil
	}

	// Invisible has no <show/>: connecting announces us as available, which
	// SetInvisible then corrects.
	show := presenceShow(status)
	if online {
		if err := client.SetPresence(ctx, show); err != nil {
			return fmt.Errorf("updating presence: %w", err)
		}
		broadcast(a.srv, ui.AccountStatusChangedMsg{Index: accountIdx, Status: status})
		return nil
	}

	// Was offline (never dialed, or previously disconnected) - dial it now.
	existing := 0
	if roster := sess.roster.Load(); roster != nil {
		existing = len(*roster)
	}
	newChats, newMessages, newHistoryMore, err := connectAccountLive(ctx, sess, existing, show)
	if err != nil {
		// Sends queue meanwhile.
		go retryInitialConnect(ctx, a.srv, a, accountIdx, sess, existing, show)
		return err
	}
	if status == ui.PresenceInvisible {
		if newClient, err := sess.liveClient(); err == nil {
			if err := newClient.SetInvisible(ctx); err != nil {
				slog.Warn("setting invisible presence after connect", "jid", sess.account.JID, "err", err)
			}
		}
	}
	go superviseAccount(ctx, a.srv, a, accountIdx, sess)

	// What arrived while switched off is only in the archive. Detached: the
	// TUI waits on this call, and a backfill can take long.
	go triggerArchiveSync(ctx, a.srv, accountIdx, sess)

	broadcast(a.srv, ui.AccountStatusChangedMsg{
		Index: accountIdx, Status: status,
		NewChats: newChats, NewMessages: newMessages, NewHistoryMore: newHistoryMore,
	})
	return nil
}

// session returns the accountSession at accountIdx, guarded by mu since
// AddAccount appends to a.sessions concurrently with reads from here.
func (a *adapter) session(accountIdx int) (*accountSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if accountIdx < 0 || accountIdx >= len(a.sessions) || a.sessions[accountIdx] == nil {
		return nil, fmt.Errorf("unknown account %d", accountIdx)
	}
	return a.sessions[accountIdx], nil
}

// online is accountIdx's session and its connection, if it's online.
func (a *adapter) online(accountIdx int) (*accountSession, *xmpp.Client, error) {
	s, err := a.session(accountIdx)
	if err != nil {
		return nil, nil, err
	}
	client, err := s.liveClient()
	return s, client, err
}

// LoadHistoryWindow loads a window of to's history around anchor (see
// loadHistoryWindow).
func (a *adapter) LoadHistoryWindow(accountIdx int, to string, anchor *ui.HistoryAnchor) (ui.HistoryWindowMsg, error) {
	sess, err := a.session(accountIdx)
	if err != nil {
		return ui.HistoryWindowMsg{}, err
	}
	msgs, hasOlder, hasNewer := loadHistoryWindow(context.Background(), sess, to, sess.rosterName(to), anchor, historyPageSize)
	return ui.HistoryWindowMsg{AccountIdx: accountIdx, From: to, Messages: msgs, HasOlder: hasOlder, HasNewer: hasNewer}, nil
}

// SearchHistory finds the messages of to's whole history containing query,
// case-insensitively, returned with that whole history so picking a result
// needs no second round trip.
func (a *adapter) SearchHistory(accountIdx int, to, query string) (ui.HistorySearchResultMsg, error) {
	sess, err := a.session(accountIdx)
	if err != nil {
		return ui.HistorySearchResultMsg{}, err
	}
	msgs := loadHistory(context.Background(), sess, to, sess.rosterName(to))
	needle := strings.ToLower(query)
	var matches []int
	for i, msg := range msgs {
		if strings.Contains(strings.ToLower(msg.Content), needle) {
			matches = append(matches, i)
		}
	}
	return ui.HistorySearchResultMsg{AccountIdx: accountIdx, From: to, Query: query, Messages: msgs, Matches: matches}, nil
}

// MessageSlots lists the 15-minute slots, as unix seconds, to's history
// has messages in (see ListMessageQuarterHours).
func (a *adapter) MessageSlots(accountIdx int, to string) ([]int64, error) {
	sess, err := a.session(accountIdx)
	if err != nil {
		return nil, err
	}
	slots, err := sess.db.ListMessageQuarterHours(context.Background(), storage.ListMessageQuarterHoursParams{
		AccountJid: sess.account.JID, RosterJid: nullString(to),
	})
	for i := range slots {
		slots[i] *= 900
	}
	return slots, err
}

// SetChatFlag saves one of a chat's local flags and applies it right away.
func (a *adapter) SetChatFlag(accountJID, chatAddress string, flag ui.ChatSetting, on bool) error {
	flags, ok := chatFlagSets[flag]
	if !ok {
		return fmt.Errorf("unknown chat flag %q", flag)
	}
	if err := config.SetChatFlag(a.cfgPath, accountJID, chatAddress, flag, on); err != nil {
		return err
	}
	// Also in memory, so an account that reconnects before the daemon
	// restarts rebuilds its chat list the same.
	flags.flag(accountJID, chatAddress, on)
	if flag == ui.ChatHidden {
		// A hidden chat doesn't count towards the tray dot (see badgeable).
		count := 0
		if !on {
			if n, err := a.queries.GetChatUnread(context.Background(), storage.GetChatUnreadParams{
				AccountJid: accountJID,
				RosterJid:  chatAddress,
			}); err == nil {
				count = int(n)
			}
		}
		unreadBadge.set(accountJID, chatAddress, count)
	}
	broadcast(a.srv, ui.ChatSettingMsg{AccountJID: accountJID, ChatAddress: chatAddress, Setting: flag, On: on})
	return nil
}

// SetFocusState records whether the calling TUI's terminal has focus and
// which chat it shows, so a message already on screen isn't notified.
func (a *adapter) SetFocusState(tuiClient ipc.ClientID, accountJID, chatAddress string, focused bool) error {
	key := focusedChatKey(accountJID, chatAddress)
	clientFocus.set(tuiClient, focusState{focused: focused, chatKey: key})
	slog.Debug("focus state updated", "client", tuiClient, "accountJID", accountJID, "chatAddress", chatAddress, "focused", focused, "key", key)
	return nil
}

// ResetChatUnread clears a chat's unread count, in every TUI.
func (a *adapter) ResetChatUnread(accountJID, chatAddress string) error {
	if err := a.queries.ResetChatUnread(context.Background(), storage.ResetChatUnreadParams{
		AccountJid: accountJID,
		RosterJid:  chatAddress,
	}); err != nil {
		return err
	}
	unreadBadge.set(accountJID, chatAddress, 0)
	broadcast(a.srv, ui.ChatUnreadMsg{AccountJID: accountJID, ChatAddress: chatAddress})
	return nil
}

// ChatUnreadCounts is an account's nonzero unread counts by chat address.
func (a *adapter) ChatUnreadCounts(accountJID string) (map[string]int, error) {
	rows, err := a.queries.ListChatUnread(context.Background(), accountJID)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(rows))
	for _, r := range rows {
		counts[r.Rosterjid] = int(r.Count)
	}
	return counts, nil
}

// SaveDraft stores a chat's unsent text, sealed like a message body; ""
// deletes it.
func (a *adapter) SaveDraft(accountJID, chatAddress, text string) error {
	if text == "" {
		return a.queries.DeleteChatDraft(context.Background(), storage.DeleteChatDraftParams{
			AccountJid: accountJID,
			RosterJid:  chatAddress,
		})
	}
	body, encrypted := sealBody(a.localKey, text)
	return a.queries.SetChatDraft(context.Background(), storage.SetChatDraftParams{
		AccountJid: accountJID,
		RosterJid:  chatAddress,
		Body:       body,
		Encrypted:  encrypted,
	})
}

// omemoProtocolLabel matches ui.OmemoDevice.Protocol's "v1"/"v2" convention.
func omemoProtocolLabel(p omemolib.Protocol) string {
	if p == omemolib.ProtocolV1 {
		return "v1"
	}
	return "v2"
}

// FetchOwnDeviceList fetches the account's published device lists of both
// OMEMO protocols.
func (a *adapter) FetchOwnDeviceList(accountIdx int) (ui.OmemoDeviceListMsg, error) {
	s, err := a.session(accountIdx)
	if err != nil {
		return ui.OmemoDeviceListMsg{}, errors.New("account is still connecting")
	}
	if s.omemoV2() == nil && s.omemoV1() == nil {
		return ui.OmemoDeviceListMsg{}, fmt.Errorf("omemo isn't ready for this account")
	}

	ctx := context.Background()
	client := s.client.Load()
	var local, devices []ui.OmemoDevice

	if v2 := s.omemoV2(); v2 != nil {
		local = append(local, ui.OmemoDevice{Protocol: "v2", ID: uint32(v2.LocalDevice().ID)})
		list, err := client.FetchOmemoDeviceList(ctx, s.account.JID)
		if err != nil {
			return ui.OmemoDeviceListMsg{}, fmt.Errorf("fetching omemo-v2 device list: %w", err)
		}
		for _, d := range list.Devices {
			devices = append(devices, ui.OmemoDevice{Protocol: "v2", ID: uint32(d)})
		}
	}
	if v1 := s.omemoV1(); v1 != nil {
		local = append(local, ui.OmemoDevice{Protocol: "v1", ID: uint32(v1.LocalDevice().ID)})
		list, err := client.FetchOmemoDeviceListV1(ctx, s.account.JID)
		if err != nil {
			return ui.OmemoDeviceListMsg{}, fmt.Errorf("fetching omemo-v1 device list: %w", err)
		}
		for _, d := range list.Devices {
			devices = append(devices, ui.OmemoDevice{Protocol: "v1", ID: uint32(d)})
		}
	}

	// Our own devices are normally in the published list too: reuse those
	// fingerprints rather than fetching the same bundles twice.
	fingerprintDevices(ctx, s, devices)
	var unlisted []int
	for i, l := range local {
		idx := slices.IndexFunc(devices, func(d ui.OmemoDevice) bool { return d.Protocol == l.Protocol && d.ID == l.ID })
		if idx < 0 {
			unlisted = append(unlisted, i)
			continue
		}
		local[i].Fingerprint = devices[idx].Fingerprint
	}
	if len(unlisted) > 0 {
		missing := make([]ui.OmemoDevice, len(unlisted))
		for j, i := range unlisted {
			missing[j] = local[i]
		}
		fingerprintDevices(ctx, s, missing)
		for j, i := range unlisted {
			local[i].Fingerprint = missing[j].Fingerprint
		}
	}
	return ui.OmemoDeviceListMsg{AccountIdx: accountIdx, Local: local, Devices: devices}, nil
}

// deviceFingerprintTimeout bounds fetching every listed device's bundle for
// its fingerprint - one slow or dead device mustn't hold the list up.
const deviceFingerprintTimeout = 5 * time.Second

// fingerprintDevices fills in each of this account's devices' fingerprint
// from its published bundle, fetched concurrently. A bundle that can't be
// fetched leaves that fingerprint empty - the device is still listed.
func fingerprintDevices(ctx context.Context, s *accountSession, devices []ui.OmemoDevice) {
	client := s.client.Load()
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, deviceFingerprintTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for i := range devices {
		d := &devices[i]
		wg.Go(func() {
			dev := omemolib.Device{JID: s.account.JID, ID: omemolib.DeviceID(d.ID)}
			fetch := client.FetchOmemoBundle
			if d.Protocol == "v1" {
				fetch = client.FetchOmemoBundleV1
			}
			bundle, err := fetch(ctx, dev)
			if err != nil {
				slog.Debug("fetching own device bundle for its fingerprint", "jid", s.account.JID, "device", d.ID, "protocol", d.Protocol, "err", err)
				return
			}
			d.Fingerprint = omemoFingerprint(bundle.IdentityKey)
		})
	}
	wg.Wait()
}

// omemoFingerprint renders an OMEMO identity key the way other clients
// show it for comparison: lowercase hex in groups of eight.
func omemoFingerprint(identityKey []byte) string {
	h := hex.EncodeToString(identityKey)
	groups := make([]string, 0, (len(h)+7)/8)
	for len(h) > 8 {
		groups = append(groups, h[:8])
		h = h[8:]
	}
	if h != "" {
		groups = append(groups, h)
	}
	return strings.Join(groups, " ")
}

// PurgeOwnDeviceList republishes each changed protocol's device list with
// only keep's devices, and always this instance's own.
func (a *adapter) PurgeOwnDeviceList(accountIdx int, keep []ui.OmemoDevice) (ui.OmemoDevicePurgedMsg, error) {
	s, err := a.session(accountIdx)
	if err != nil {
		return ui.OmemoDevicePurgedMsg{}, errors.New("account is still connecting")
	}
	if s.omemoV2() == nil && s.omemoV1() == nil {
		return ui.OmemoDevicePurgedMsg{}, fmt.Errorf("omemo isn't ready for this account")
	}

	ctx := context.Background()
	client := s.client.Load()
	var local, devices []ui.OmemoDevice

	purge := func(protocol omemolib.Protocol, mgr *omemolib.Manager, publish func(context.Context, omemolib.DeviceList) error) error {
		if mgr == nil {
			return nil
		}
		label := omemoProtocolLabel(protocol)
		local = append(local, ui.OmemoDevice{Protocol: label, ID: uint32(mgr.LocalDevice().ID)})

		localID := mgr.LocalDevice().ID
		hasLocal := false
		var ids []omemolib.DeviceID
		for _, d := range keep {
			if d.Protocol != label {
				continue
			}
			ids = append(ids, omemolib.DeviceID(d.ID))
			if omemolib.DeviceID(d.ID) == localID {
				hasLocal = true
			}
		}
		if !hasLocal {
			ids = append(ids, localID)
		}

		if err := publish(ctx, omemolib.DeviceList{JID: s.account.JID, Devices: ids}); err != nil {
			return fmt.Errorf("publishing omemo-%s device list: %w", label, err)
		}
		for _, id := range ids {
			devices = append(devices, ui.OmemoDevice{Protocol: label, ID: uint32(id)})
		}
		return nil
	}

	if err := purge(omemolib.ProtocolV2, s.omemoV2(), client.PublishOmemoDeviceList); err != nil {
		return ui.OmemoDevicePurgedMsg{}, err
	}
	if err := purge(omemolib.ProtocolV1, s.omemoV1(), client.PublishOmemoDeviceListV1); err != nil {
		return ui.OmemoDevicePurgedMsg{}, err
	}

	return ui.OmemoDevicePurgedMsg{AccountIdx: accountIdx, Local: local, Devices: devices}, nil
}

// RemoveAccount removes this device from the account's published OMEMO
// device lists, disconnects it and drops it from config.toml. Local storage
// is kept. Its sessions slot stays, closed, as indices must stay stable
// (see ui.AccountRemovedMsg).
func (a *adapter) RemoveAccount(accountIdx int) error {
	s, err := a.session(accountIdx)
	if err != nil {
		return errors.New("account is still connecting")
	}

	ctx := context.Background()
	client := s.client.Load()

	purgeSelf := func(
		mgr *omemolib.Manager,
		fetch func(context.Context, string) (omemolib.DeviceList, error),
		publish func(context.Context, omemolib.DeviceList) error,
	) error {
		if mgr == nil {
			return nil
		}
		list, err := fetch(ctx, s.account.JID)
		if err != nil {
			return err
		}
		localID := mgr.LocalDevice().ID
		ids := make([]omemolib.DeviceID, 0, len(list.Devices))
		for _, id := range list.Devices {
			if id != localID {
				ids = append(ids, id)
			}
		}
		return publish(ctx, omemolib.DeviceList{JID: s.account.JID, Devices: ids})
	}
	if err := purgeSelf(s.omemoV2(), client.FetchOmemoDeviceList, client.PublishOmemoDeviceList); err != nil {
		slog.Warn("purging own device from omemo-v2 device list before removal", "jid", s.account.JID, "err", err)
	}
	if err := purgeSelf(s.omemoV1(), client.FetchOmemoDeviceListV1, client.PublishOmemoDeviceListV1); err != nil {
		slog.Warn("purging own device from omemo-v1 device list before removal", "jid", s.account.JID, "err", err)
	}

	if err := client.Close(); err != nil {
		slog.Warn("closing connection on account removal", "jid", s.account.JID, "err", err)
	}
	if err := config.RemoveAccount(a.cfgPath, s.account.JID); err != nil {
		return fmt.Errorf("removing account from config: %w", err)
	}

	unreadBadge.forgetAccount(s.account.JID)
	broadcast(a.srv, ui.AccountRemovedMsg{Index: accountIdx})

	return nil
}

// MarkRetracted flags someone else's message deleted, locally only: XEP-0424
// retracts only our own.
func (a *adapter) MarkRetracted(accountIdx int, to, id string) error {
	s, err := a.session(accountIdx)
	if err != nil {
		return err
	}
	if _, err := s.db.MarkMessageRetracted(context.Background(), storage.MarkMessageRetractedParams{
		AccountJid: s.account.JID,
		IDAttr:     nullString(id),
		RosterJid:  nullString(to),
	}); err != nil {
		return err
	}
	broadcast(a.srv, ui.MessageRetractedMsg{AccountIdx: accountIdx, From: to, RetractID: id})
	return nil
}

// PurgeMessage erases a retracted message and its reactions from storage, in
// every TUI - the second delete. Local only.
func (a *adapter) PurgeMessage(accountIdx int, to, id string) error {
	s, err := a.session(accountIdx)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if err := s.db.DeleteReactionsByMessage(ctx, storage.DeleteReactionsByMessageParams{
		AccountJid: s.account.JID,
		RosterJid:  to,
		IDAttr:     id,
	}); err != nil {
		return err
	}
	rows, err := s.db.DeleteMessageByID(ctx, storage.DeleteMessageByIDParams{
		AccountJid: s.account.JID,
		IDAttr:     nullString(id),
		RosterJid:  nullString(to),
	})
	if err != nil {
		return err
	}
	// Same reasoning as DeleteQueued: broadcast only when a row really went
	// away, so a repeated purge doesn't make every client re-run the removal.
	if rows > 0 {
		broadcast(a.srv, ui.MessagePurgedMsg{AccountIdx: accountIdx, Peer: to, MessageID: id})
	}
	return nil
}

// DeleteQueued discards a queued message unsent, in every TUI. Not an error
// if it's gone already.
func (a *adapter) DeleteQueued(accountIdx int, localID string) error {
	s, err := a.session(accountIdx)
	if err != nil {
		return err
	}
	to, found, err := s.deleteOutboxByLocalID(context.Background(), localID)
	if err != nil {
		return err
	}
	if found {
		broadcast(a.srv, ui.OutboxDeletedMsg{AccountIdx: accountIdx, To: to, LocalID: localID})
	}
	return nil
}

// SetTyping sends a XEP-0085 chat state. "composing" is remembered per TUI
// for clearTyping.
func (a *adapter) SetTyping(tuiClient ipc.ClientID, accountIdx int, to string, composing bool) error {
	_, client, err := a.online(accountIdx)
	if err != nil {
		return err
	}
	state := xmpp.ChatStateActive
	if composing {
		state = xmpp.ChatStateComposing
	}
	if err := client.SendChatState(context.Background(), to, state); err != nil {
		return err
	}
	a.markTyping(tuiClient, accountIdx, to, composing)
	return nil
}

// markTyping records (composing=true) or forgets (composing=false) the
// "composing" state SetTyping just put on the wire for tuiClient's accountIdx.
func (a *adapter) markTyping(tuiClient ipc.ClientID, accountIdx int, to string, composing bool) {
	a.typingMu.Lock()
	defer a.typingMu.Unlock()
	if composing {
		if a.typingTo == nil {
			a.typingTo = make(map[ipc.ClientID]map[int]string)
		}
		if a.typingTo[tuiClient] == nil {
			a.typingTo[tuiClient] = make(map[int]string)
		}
		a.typingTo[tuiClient][accountIdx] = to
		return
	}
	// Only the peer we're actually marked as composing to: an "active" for
	// some other chat says nothing about this one.
	if a.typingTo[tuiClient][accountIdx] == to {
		delete(a.typingTo[tuiClient], accountIdx)
		if len(a.typingTo[tuiClient]) == 0 {
			delete(a.typingTo, tuiClient)
		}
	}
}

// drainTyping returns and forgets tuiClient's outstanding "composing" peer
// for every account.
func (a *adapter) drainTyping(tuiClient ipc.ClientID) map[int]string {
	a.typingMu.Lock()
	defer a.typingMu.Unlock()
	pending := a.typingTo[tuiClient]
	delete(a.typingTo, tuiClient)
	return pending
}

// clearTyping sends "active" to every peer tuiClient left "composing", for a
// TUI that went away while typing.
func (a *adapter) clearTyping(tuiClient ipc.ClientID) {
	for accountIdx, to := range a.drainTyping(tuiClient) {
		_, client, err := a.online(accountIdx)
		if err != nil {
			continue
		}
		if err := client.SendChatState(context.Background(), to, xmpp.ChatStateActive); err != nil {
			slog.Warn("adapter: clearing typing state", "account", accountIdx, "to", to, "err", err)
		}
	}
}

// RenameContact sets a contact's name with a roster set (RFC 6121), and in
// storage and the roster cache. "" clears it.
func (a *adapter) RenameContact(accountIdx int, address, name string) error {
	s, client, err := a.online(accountIdx)
	if err != nil {
		return err
	}
	if err := client.SetRosterName(context.Background(), address, name); err != nil {
		return err
	}

	var subs string
	s.mutateRoster(func(entries map[string]rosterEntry) {
		e := entries[address]
		// Only the name changes: a rename must not reset the contact's
		// subscription, presence or online resources.
		e.Name = name
		entries[address] = e
		subs = e.Subs
	})
	if err := s.db.UpsertRoster(context.Background(), storage.UpsertRosterParams{
		AccountJid: s.account.JID, Jid: address, Name: name, Subs: subs,
	}); err != nil {
		slog.Warn("persisting renamed roster entry", "address", address, "err", err)
	}

	return nil
}

// AddContact adds address to the roster and asks for its presence.
func (a *adapter) AddContact(accountIdx int, address string) error {
	s, client, err := a.online(accountIdx)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if err := client.AddContact(ctx, address, ""); err != nil {
		return err
	}

	if err := s.db.UpsertRoster(ctx, storage.UpsertRosterParams{
		AccountJid: s.account.JID, Jid: address,
	}); err != nil {
		slog.Warn("persisting added roster entry", "address", address, "err", err)
	}
	s.mutateRoster(func(entries map[string]rosterEntry) {
		if _, known := entries[address]; !known {
			entries[address] = rosterEntry{}
		}
	})

	return nil
}

// ResubscribeContact re-sends a subscription
// request for an existing contact, without touching its roster item.
func (a *adapter) ResubscribeContact(accountIdx int, address string) error {
	_, client, err := a.online(accountIdx)
	if err != nil {
		return err
	}
	return client.ResubscribeContact(context.Background(), address)
}

// RemoveContact removes address from the roster and unsubscribes.
func (a *adapter) RemoveContact(accountIdx int, address string) error {
	s, client, err := a.online(accountIdx)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if err := client.RemoveContact(ctx, address); err != nil {
		return err
	}

	if err := s.db.DeleteRosterByJID(ctx, storage.DeleteRosterByJIDParams{
		AccountJid: s.account.JID, Jid: address,
	}); err != nil {
		slog.Warn("deleting roster entry", "address", address, "err", err)
	}
	forgetChat(ctx, s.db, s.account.JID, address)
	s.mutateRoster(func(entries map[string]rosterEntry) { delete(entries, address) })

	return nil
}

// SetChatEncryption sets a chat's outgoing encryption: "omemo-v1",
// "omemo-v2", "gpg" or "none".
func (a *adapter) SetChatEncryption(accountIdx int, peerJID, mode string) error {
	s, err := a.session(accountIdx)
	if err != nil {
		return err
	}
	if err := s.db.SetChatEncryptionMode(context.Background(), storage.SetChatEncryptionModeParams{
		AccountJid: s.account.JID,
		RosterJid:  peerJID,
		Mode:       mode,
	}); err != nil {
		return err
	}
	broadcast(a.srv, ui.ChatSettingMsg{AccountJID: s.account.JID, ChatAddress: peerJID, Setting: ui.ChatEncryption, Mode: mode})
	return nil
}

func (a *adapter) Send(accountIdx int, to, body string, opts ui.SendOptions) (string, error) {
	return a.send(context.Background(), accountIdx, to, body, opts)
}

// flushOutbox replays the sends queued while s was offline, oldest first,
// through a.send (attachments uploaded first), broadcasting the results.
//
// Each row is deleted right before its attempt, not after: going offline
// again mid-attempt re-queues it as a new row, while deleting after could
// let two flushes send it twice.
func (a *adapter) flushOutbox(ctx context.Context, s *accountSession) {
	if _, err := s.liveClient(); err != nil {
		return
	}

	queued, err := s.loadOutbox(ctx)
	if err != nil {
		slog.Warn("loading outbox for flush", "jid", s.account.JID, "err", err)
		return
	}

	for _, q := range queued {
		if q.filePath == "" {
			if err := s.deleteOutboxRow(ctx, q.dbID); err != nil {
				slog.Warn("deleting flushed outbox entry", "jid", s.account.JID, "to", q.to, "err", err)
			}
			id, err := a.send(ctx, s.accountIdx, q.to, q.body, q.opts)
			switch {
			case errors.Is(err, ui.ErrQueued):
				// Offline again: a.send re-queued it.
			case err != nil:
				slog.Warn("sending queued message failed", "jid", s.account.JID, "to", q.to, "err", err)
				if q.opts.LocalID != "" {
					broadcast(a.srv, ui.MessageSendResolvedMsg{
						AccountIdx: s.accountIdx,
						To:         q.to,
						LocalID:    q.opts.LocalID,
						Err:        err.Error(),
					})
				}
			default:
				encrypted, method := modeEncryption(resolveEncryptionMode(ctx, s, q.to))
				if q.opts.LocalID != "" {
					broadcast(a.srv, ui.MessageSendResolvedMsg{
						AccountIdx: s.accountIdx,
						To:         q.to,
						LocalID:    q.opts.LocalID,
						ID:         id,
						Encrypted:  encrypted,
						EncMethod:  method,
					})
				}
			}
			continue
		}

		client, err := s.liveClient()
		if err != nil {
			// Went offline again - leave this (and every remaining) row
			// alone for the next flush attempt.
			return
		}
		url, err := a.uploadFile(ctx, s, client, q.to, q.filePath, nil)
		if err != nil {
			// Stored as failed (see markOutboxFailed), and the placeholder
			// resolved by LocalID.
			slog.Warn("uploading queued attachment failed", "jid", s.account.JID, "to", q.to, "path", q.filePath, "err", err)
			if delErr := s.deleteOutboxRow(ctx, q.dbID); delErr != nil {
				slog.Warn("deleting failed outbox attachment entry", "jid", s.account.JID, "to", q.to, "err", delErr)
			}
			if pErr := s.markOutboxFailed(ctx, q.to, q.body, q.opts, err.Error()); pErr != nil {
				slog.Warn("persisting failed attachment send", "jid", s.account.JID, "to", q.to, "err", pErr)
			}
			if q.opts.LocalID != "" {
				broadcast(a.srv, ui.MessageSendResolvedMsg{
					AccountIdx: s.accountIdx, To: q.to, LocalID: q.opts.LocalID, Err: err.Error(),
				})
			}
			continue
		}
		body := url
		if q.body != "" {
			body = q.body + "\n" + url
		}
		sendOpts := q.opts
		sendOpts.OOBURLs = []string{url}
		id, sendErr := a.send(ctx, s.accountIdx, q.to, body, sendOpts)
		if delErr := s.deleteOutboxRow(ctx, q.dbID); delErr != nil {
			slog.Warn("deleting flushed outbox attachment entry", "jid", s.account.JID, "to", q.to, "err", delErr)
		}
		switch {
		case errors.Is(sendErr, ui.ErrQueued):
			// Offline again: a.send re-queued it, now with the URL.
		case sendErr != nil:
			slog.Warn("sending queued attachment failed", "jid", s.account.JID, "to", q.to, "path", q.filePath, "err", sendErr)
			if q.opts.LocalID != "" {
				broadcast(a.srv, ui.MessageSendResolvedMsg{
					AccountIdx: s.accountIdx, To: q.to, LocalID: q.opts.LocalID, Err: sendErr.Error(),
				})
			}
		default:
			// Resolves the placeholder shown when it was queued, rather than
			// adding a message.
			if q.opts.LocalID != "" {
				encrypted, method := modeEncryption(resolveEncryptionMode(ctx, s, q.to))
				broadcast(a.srv, ui.MessageSendResolvedMsg{
					AccountIdx: s.accountIdx, To: q.to, LocalID: q.opts.LocalID,
					ID: id, Encrypted: encrypted, EncMethod: method,
					Content: body, Attachments: []string{url},
				})
			}
		}
	}
}

// modeEncryption is whether a message sent under encryption mode is
// end-to-end encrypted, and how.
func modeEncryption(mode string) (encrypted bool, method string) {
	switch mode {
	case "none", "":
		return false, ""
	case "gpg", "omemo-v1", "omemo-v2":
		return true, mode
	}
	return true, "omemo"
}

// send is Send with a context. opts.OOBURLs are marked as XEP-0066
// attachments only when sent unencrypted, or they'd leak beside an encrypted
// body.
func (a *adapter) send(ctx context.Context, accountIdx int, to, body string, opts ui.SendOptions) (id string, err error) {
	s, err := a.session(accountIdx)
	if err != nil {
		return "", err
	}

	// A failed new message is stored as failed (see markOutboxFailed).
	isPlainMessageSend := opts.ReplaceID == "" && opts.RetractID == "" && opts.ReactionTargetID == ""
	defer func() {
		if err != nil && !errors.Is(err, ui.ErrQueued) && isPlainMessageSend && opts.LocalID != "" {
			if pErr := s.markOutboxFailed(ctx, to, body, opts, err.Error()); pErr != nil {
				slog.Warn("persisting failed send", "jid", s.account.JID, "to", to, "err", pErr)
			}
		}
	}()

	client, err := s.liveClient()
	if err != nil {
		// Queued for when we're back online; ui.ErrQueued tells the caller
		// it isn't sent.
		if qErr := s.enqueueOutbox(ctx, to, body, opts); qErr != nil {
			return "", fmt.Errorf("account offline and queuing failed: %w", qErr)
		}
		slog.Debug("account offline; message queued", "jid", s.account.JID, "to", to)
		return "", ui.ErrQueued
	}

	if opts.ReactionTargetID != "" {
		id, err := client.Send(ctx, to, "", xmpp.SendOptions{
			ReactionTargetID: opts.ReactionTargetID,
			Reactions:        opts.Reactions,
		})
		if err != nil {
			return "", err
		}
		if err := replaceReactions(ctx, s, to, opts.ReactionTargetID, meReactorJID, opts.Reactions); err != nil {
			slog.Warn("persisting our own reactions", "err", err)
		}
		// Every other attached TUI only learns of our own reaction from this
		// (see the plain-send broadcast below for why).
		broadcast(a.srv, ui.MessageReactionsMsg{
			AccountIdx: accountIdx,
			From:       to,
			MessageID:  opts.ReactionTargetID,
			Reactions:  loadReactionsForMessage(ctx, s, to, opts.ReactionTargetID),
		})
		return id, nil
	}

	if opts.RetractID != "" {
		// Nothing to encrypt; our own copy is only flagged.
		id, err := client.Send(ctx, to, "", xmpp.SendOptions{RetractID: opts.RetractID})
		if err != nil {
			return "", err
		}
		if _, err := s.db.MarkMessageRetracted(ctx, storage.MarkMessageRetractedParams{
			AccountJid: s.account.JID,
			IDAttr:     nullString(opts.RetractID),
			RosterJid:  nullString(to),
		}); err != nil {
			slog.Warn("marking retracted message in storage", "err", err)
		}
		broadcast(a.srv, ui.MessageRetractedMsg{AccountIdx: accountIdx, From: to, RetractID: opts.RetractID})
		return id, nil
	}

	plaintext := body
	if opts.ReplyToID != "" {
		// Build quoted text for encrypted replies - the quoted text is inside the encrypted payload
		// Don't include author name in encrypted quotes (just the quoted content)
		lines := strings.Split(opts.QuotedBody, "\n")
		for i, l := range lines {
			lines[i] = "> " + l
		}
		quote := strings.Join(lines, "\n") + "\n"
		plaintext = quote + body
	}

	wireBody := plaintext
	sendOpts := xmpp.SendOptions{
		ReplaceID:    opts.ReplaceID,
		ReplyToID:    opts.ReplyToID,
		QuotedAuthor: opts.QuotedAuthor,
		QuotedBody:   opts.QuotedBody,
	}

	e2eEncrypted := false
	e2eeMethod := ""
	mode := resolveEncryptionMode(ctx, s, to)
	switch mode {
	case "omemo-v1", "omemo-v2":
		protocol, mgr := resolveOmemoManagerForMode(ctx, s, mode, to)
		slog.Debug("send: using omemo encryption", "protocol", protocol, "jid", s.account.JID, "to", to)
		if mgr != nil {
			// The plain body, no envelope with attachment metadata: other
			// clients would show it as text.
			refreshPeerDevicesIfStale(ctx, s, protocol, to, mgr.SyncDevices)
			enc, deviceErrs, err := mgr.EncryptMessage(ctx, to, []byte(plaintext))
			if err != nil {
				// The cached device list may be stale; resync and retry.
				slog.Debug("send: omemo encrypt failed; forcing device resync", "protocol", protocol, "jid", s.account.JID, "to", to, "err", err, "device_errs", deviceErrs)
				if syncErr := mgr.SyncDevices(ctx, to); syncErr != nil {
					slog.Debug("send: omemo device resync failed", "protocol", protocol, "to", to, "err", syncErr)
					return "", fmt.Errorf("omemo-encrypting to %s: device list resync failed: %w", to, syncErr)
				}
				enc, deviceErrs, err = mgr.EncryptMessage(ctx, to, []byte(plaintext))
				if err != nil {
					slog.Debug("send: omemo encrypt failed after resync", "protocol", protocol, "jid", s.account.JID, "to", to, "err", err, "device_errs", deviceErrs)
					return "", fmt.Errorf("omemo-encrypting to %s: %w (device errors: %v)", to, err, deviceErrs)
				}
			}
			slog.Debug("send: omemo encrypt succeeded", "protocol", protocol, "jid", s.account.JID, "to", to, "keys", len(enc.Keys))
			checkRecipientCoverage(ctx, s, mgr, protocol, to, enc)
			for _, de := range deviceErrs {
				// EncryptMessage only errors when every device failed.
				slog.Debug("send: omemo device failed (message still sent to other devices)", "protocol", protocol, "device_jid", de.Device.JID, "device_id", de.Device.ID, "err", de.Err)
			}
			if protocol == omemolib.ProtocolV1 {
				sendOpts.EncryptedV1 = xmpp.EncodeOmemoMessageV1(enc)
				e2eeMethod = "omemo-v1"
			} else {
				sendOpts.Encrypted = xmpp.EncodeOmemoMessage(enc)
				e2eeMethod = "omemo-v2"
			}
			wireBody = ""
			e2eEncrypted = true
		} else {
			// Never fall back to plaintext in an encrypted chat.
			slog.Warn("send: omemo not ready; refusing to send unencrypted", "protocol", protocol, "jid", s.account.JID, "to", to)
			return "", fmt.Errorf("omemo(%s) isn't ready for this account; message not sent", protocol)
		}
	case "gpg":
		if !a.useGPG {
			return "", fmt.Errorf("gpg encryption is disabled (use_gpg is off); message not sent")
		}
		if peerKey := resolvePeerKey(ctx, s, to); peerKey != "" {
			ct, err := s.gpg.Encrypt(plaintext, peerKey)
			if err != nil {
				return "", fmt.Errorf("encrypting to %s: %w", to, err)
			}
			wireBody = ct
			e2eEncrypted = true
			e2eeMethod = "gpg"
		}
	}

	if !e2eEncrypted {
		sendOpts.OOBURLs = opts.OOBURLs
	}

	id, err = client.Send(ctx, to, wireBody, sendOpts)
	if err != nil {
		return "", err
	}

	if opts.ReplaceID != "" {
		// A correction amends the original message in place; it isn't a new
		// row in history.
		sealedBody, encrypted := encryptForStorage(s, body)
		if _, err := s.db.UpdateMessageBodyByID(ctx, storage.UpdateMessageBodyByIDParams{
			AccountJid:   s.account.JID,
			Body:         sealedBody,
			Encrypted:    encrypted,
			E2eEncrypted: e2eEncrypted,
			E2eeMethod:   nullString(e2eeMethod),
			Edited:       true,
			IDAttr:       nullString(opts.ReplaceID),
			RosterJid:    nullString(to),
		}); err != nil {
			slog.Warn("persisting correction", "err", err)
		}
		// For the other TUIs, as below.
		broadcast(a.srv, ui.MessageCorrectedMsg{
			AccountIdx: accountIdx,
			From:       to,
			ReplaceID:  opts.ReplaceID,
			NewContent: body,
			Encrypted:  e2eEncrypted,
			EncMethod:  e2eeMethod,
		})
		return id, nil
	}

	if opts.LocalID != "" {
		// A retry of a failure stored under this LocalID: drop that row, or
		// it reappears as failed beside the sent message.
		if _, _, err := s.deleteOutboxByLocalID(ctx, opts.LocalID); err != nil {
			slog.Warn("clearing stale outbox entry after successful retry", "jid", s.account.JID, "to", to, "err", err)
		}
	}
	if opts.SupersedesID != "" {
		// A retry of a stored message an ack timeout failed (see
		// SendOptions.SupersedesID): drop its row the same way.
		if _, err := s.db.DeleteMessageByID(ctx, storage.DeleteMessageByIDParams{
			AccountJid: s.account.JID,
			IDAttr:     nullString(opts.SupersedesID),
			RosterJid:  nullString(to),
		}); err != nil {
			slog.Warn("clearing superseded failed message after successful retry", "jid", s.account.JID, "to", to, "err", err)
		}
	}

	// Stored with the reply, so it shows even once the replied-to message
	// isn't loaded (see ui.Message.QuotedAuthor).
	var quotedAuthor, quotedPreview string
	if opts.ReplyToID != "" {
		quotedAuthor = opts.QuotedAuthor
		quotedPreview = opts.QuotedBody
	}

	sealedBody, encrypted := encryptForStorage(s, body)
	if _, err := s.db.InsertMessage(ctx, storage.InsertMessageParams{
		AccountJid:        s.account.JID,
		Sent:              true,
		ToAttr:            nullString(to),
		IDAttr:            nullString(id),
		Body:              sealedBody,
		Encrypted:         encrypted,
		E2eEncrypted:      e2eEncrypted,
		E2eeMethod:        nullString(e2eeMethod),
		StanzaType:        "chat",
		RosterJid:         nullString(to),
		ReplyToIDAttr:     nullString(opts.ReplyToID),
		ReplyQuoteAuthor:  nullString(quotedAuthor),
		ReplyQuotePreview: nullString(quotedPreview),
		OobUrls:           joinOOBURLs(opts.OOBURLs),
	}); err != nil {
		slog.Warn("persisting sent message", "err", err)
	}
	s.trackForServerAck(a, s.accountIdx, to, id)

	// Every TUI learns of it as an incoming message; the sender's own, which
	// already shows it, dedupes it by ID.
	broadcast(a.srv, ui.IncomingMessageMsg{
		AccountIdx: accountIdx,
		From:       to,
		ReplyToID:  opts.ReplyToID,
		Message: ui.Message{
			ID:            id,
			LocalID:       opts.LocalID,
			Author:        "me",
			Content:       body,
			SentAt:        time.Now(),
			IsMe:          true,
			Encrypted:     e2eEncrypted,
			EncMethod:     e2eeMethod,
			Attachments:   opts.OOBURLs,
			QuotedAuthor:  quotedAuthor,
			QuotedPreview: quotedPreview,
		},
	})
	return id, nil
}

// uploadFile uploads path (XEP-0363), encrypted (XEP-0454) when a message to
// "to" would be, returning the URL to send.
func (a *adapter) uploadFile(ctx context.Context, s *accountSession, client *xmpp.Client, to, path string, onProgress func(sent, total int64)) (string, error) {
	encryptFile := false
	switch mode := resolveEncryptionMode(ctx, s, to); mode {
	case "omemo-v1", "omemo-v2":
		_, mgr := resolveOmemoManagerForMode(ctx, s, mode, to)
		encryptFile = mgr != nil
	case "gpg":
		encryptFile = a.useGPG && resolvePeerKey(ctx, s, to) != ""
	}

	if !encryptFile {
		url, err := client.UploadFile(ctx, path, onProgress)
		if err != nil {
			return "", err
		}
		cacheUploadedAttachment(path, url, to)
		return url, nil
	}

	// Encrypt file with AES-256-GCM before upload (XEP-0454)
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("opening file: %w", err)
	}
	defer f.Close()

	ciphertext, iv, key, err := aesgcm.EncryptReader(f)
	if err != nil {
		return "", fmt.Errorf("encrypting file: %w", err)
	}

	url, err := client.Upload(ctx, filepath.Base(path), bytes.NewReader(ciphertext), int64(len(ciphertext)), onProgress)
	if err != nil {
		return "", fmt.Errorf("uploading encrypted file: %w", err)
	}

	// Build aesgcm:// URL with IV+key in anchor
	url, err = aesgcm.BuildAESGCMURL(url, iv, key)
	if err != nil {
		return "", fmt.Errorf("building aesgcm URL: %w", err)
	}
	cacheUploadedAttachment(path, url, to)
	return url, nil
}

// cacheUploadedAttachment copies an uploaded file to where downloading url
// would put it (see ui.AttachmentLocalPath). Best-effort.
func cacheUploadedAttachment(path, url, to string) {
	dest := ui.AttachmentLocalPath(url, bareJID(to))
	if dest == "" || dest == path {
		return
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		slog.Warn("caching sent attachment: mkdir", "path", dest, "err", err)
		return
	}
	src, err := os.Open(path)
	if err != nil {
		slog.Warn("caching sent attachment: open source", "path", path, "err", err)
		return
	}
	defer src.Close()

	dst, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		slog.Warn("caching sent attachment: create dest", "path", dest, "err", err)
		return
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		slog.Warn("caching sent attachment: copy", "path", dest, "err", err)
	}
}

// progressCallback is an upload's onProgress, broadcasting a
// ui.FileTransferProgressMsg when the whole percentage changes. id tells
// transfers apart.
func (a *adapter) progressCallback(id, label string) func(sent, total int64) {
	lastPercent := -1
	return func(sent, total int64) {
		percent := -1
		if total > 0 {
			percent = int(sent * 100 / total)
		}
		if percent == lastPercent {
			return
		}
		lastPercent = percent
		broadcast(a.srv, ui.FileTransferProgressMsg{ID: id, Label: label, Sent: sent, Total: total})
	}
}

// SendFile uploads path (XEP-0363) and sends its URL to "to".
func (a *adapter) SendFile(accountIdx int, to, path string, opts ui.SendOptions) (ui.FileSendResultMsg, error) {
	result := ui.FileSendResultMsg{AccountIdx: accountIdx, To: to, Path: path, ReplyToID: opts.ReplyToID}
	s, client, err := a.online(accountIdx)
	if err != nil {
		return result, err
	}
	uploadCtx, doneUpload := a.beginUpload(path)
	defer doneUpload()
	ctx, cancel := context.WithTimeout(uploadCtx, 2*time.Minute)
	defer cancel()

	label := "uploading " + filepath.Base(path)
	broadcast(a.srv, ui.FileTransferProgressMsg{ID: path, Label: label})
	url, err := a.uploadFile(ctx, s, client, to, path, a.progressCallback(path, label))
	broadcast(a.srv, ui.FileTransferDoneMsg{ID: path})
	if err != nil {
		if errors.Is(uploadCtx.Err(), context.Canceled) {
			err = errUploadCanceled
		}
		return result, err
	}

	opts.OOBURLs = []string{url}
	id, err := a.send(ctx, accountIdx, to, url, opts)
	if err != nil {
		return result, err
	}
	result.URL = url
	result.ID = id
	return result, nil
}

// UploadFile uploads path without sending it. Offline, the upload and the
// send of text/opts are queued as one (see flushOutbox).
func (a *adapter) UploadFile(accountIdx int, to, path, text string, opts ui.SendOptions) (ui.FileUploadResultMsg, error) {
	result := ui.FileUploadResultMsg{Path: path}
	s, err := a.session(accountIdx)
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client, err := s.liveClient()
	if err != nil {
		if qErr := s.enqueueOutboxFile(ctx, to, text, path, opts); qErr != nil {
			return result, fmt.Errorf("account offline and queuing failed: %w", qErr)
		}
		slog.Debug("account offline; attachment queued", "jid", s.account.JID, "to", to, "path", path)
		result.Queued = true
		return result, nil
	}

	uploadCtx, doneUpload := a.beginUpload(path)
	defer doneUpload()
	uploadCtx, cancelUpload := context.WithTimeout(uploadCtx, 2*time.Minute)
	defer cancelUpload()

	label := "uploading " + filepath.Base(path)
	broadcast(a.srv, ui.FileTransferProgressMsg{ID: path, Label: label})
	url, err := a.uploadFile(uploadCtx, s, client, to, path, a.progressCallback(path, label))
	broadcast(a.srv, ui.FileTransferDoneMsg{ID: path})
	if err != nil {
		if errors.Is(uploadCtx.Err(), context.Canceled) {
			err = errUploadCanceled
		}
		return result, err
	}
	result.URL = url
	return result, nil
}

// --- calls (see callsession.go) ---

// CallAction does action to accountIdx's call.
func (a *adapter) CallAction(accountIdx int, action ui.CallAction) error {
	sess, err := a.session(accountIdx)
	if err != nil {
		return err
	}
	ctx := context.Background()
	switch c := action.(type) {
	case ui.CallStart:
		if c.Video {
			return sess.startVideoCall(ctx, a.srv, accountIdx, c.To, c.UseCamera)
		}
		return sess.startCall(ctx, a.srv, accountIdx, c.To)
	case ui.CallAnswer:
		return sess.answerCall(ctx)
	case ui.CallReject:
		return sess.rejectCall(ctx)
	case ui.CallHangup:
		return sess.hangupCall(ctx)
	case ui.CallMute:
		return sess.muteCall(c.Muted)
	case ui.CallShareVideo:
		return sess.setScreenShare(c.Sharing, c.UseCamera)
	case ui.CallReopenVideo:
		return sess.reopenVideo()
	}
	return fmt.Errorf("unknown call action %T", action)
}

// AudioDevices lists the devices a call can use and which ones are picked
// ("" for the system default).
func (a *adapter) AudioDevices() (ui.AudioDevicesMsg, error) {
	inputs, outputs, err := call.AudioDevices()
	if err != nil {
		return ui.AudioDevicesMsg{}, err
	}
	msg := ui.AudioDevicesMsg{}
	msg.Input, msg.Output = currentAudioDevices()
	for _, d := range inputs {
		msg.Inputs = append(msg.Inputs, ui.AudioDevice{Name: d.Name, Default: d.Default})
	}
	for _, d := range outputs {
		msg.Outputs = append(msg.Outputs, ui.AudioDevice{Name: d.Name, Default: d.Default})
	}
	return msg, nil
}

// SaveState saves change for the next start, applying the call settings
// among them right away.
func (a *adapter) SaveState(change ui.StateChange) error {
	if v, ok := change.(ui.DesktopAudioVolume); ok {
		change = ui.DesktopAudioVolume(clampDesktopAudioVolume(int(v)))
	}
	err := config.SaveState(a.cfgPath, change)
	switch c := change.(type) {
	case ui.DesktopAudio:
		desktopAudioEnabled.Store(bool(c))
		for _, call := range a.calls() {
			call.syncDesktopAudio()
			call.broadcastUpdate()
		}
	case ui.DesktopAudioVolume:
		// A share in progress picks it up on its next frame.
		desktopAudioVolume.Store(int32(c))
		for _, call := range a.calls() {
			call.broadcastUpdate()
		}
	case ui.AudioDeviceChoice:
		input, output := currentAudioDevices()
		if c.Output {
			output = c.Name
		} else {
			input = c.Name
		}
		setAudioDevices(input, output)
		for _, call := range a.calls() {
			err = errors.Join(err, call.setAudioDevices(input, output, !c.Output, c.Output))
		}
	}
	return err
}

// calls is every call in progress.
func (a *adapter) calls() []*callSession {
	a.mu.Lock()
	sessions := slices.Clone(a.sessions)
	a.mu.Unlock()
	var calls []*callSession
	for _, sess := range sessions {
		if sess == nil {
			continue
		}
		if c := sess.currentCall(); c != nil {
			calls = append(calls, c)
		}
	}
	return calls
}

// shutdownHangupTimeout bounds how long daemon shutdown waits on hanging up
// calls - a dead connection mustn't hold the whole shutdown hostage.
const shutdownHangupTimeout = 2 * time.Second

// hangupAllCalls hangs up every account's call in progress, giving up
// after timeout.
func (a *adapter) hangupAllCalls(timeout time.Duration) {
	a.mu.Lock()
	sessions := append([]*accountSession(nil), a.sessions...)
	a.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var wg sync.WaitGroup
	for _, sess := range sessions {
		if sess == nil || sess.currentCall() == nil {
			continue
		}
		wg.Go(func() {
			if err := sess.hangupCall(ctx); err != nil {
				slog.Warn("hanging up call on shutdown", "jid", sess.account.JID, "err", err)
			}
		})
	}
	wg.Wait()
}

// peerDeviceSyncInterval is how long a peer's cached OMEMO device list is
// trusted before a send re-fetches it.
const peerDeviceSyncInterval = 30 * time.Minute

// refreshPeerDevicesIfStale re-fetches to's OMEMO device list if it wasn't
// synced within peerDeviceSyncInterval. omemo-go only fetches an empty
// cached list by itself, so a stale one encrypts without error but without
// a key for the peer's newer devices. Best-effort. syncDevices is
// mgr.SyncDevices.
func refreshPeerDevicesIfStale(
	ctx context.Context, s *accountSession, protocol omemolib.Protocol, to string,
	syncDevices func(context.Context, string) error,
) {
	key := protocol.String() + "\x00" + to
	s.peerDeviceSyncMu.Lock()
	last, ok := s.peerDeviceSyncedAt[key]
	fresh := ok && time.Since(last) < peerDeviceSyncInterval
	if !fresh {
		if s.peerDeviceSyncedAt == nil {
			s.peerDeviceSyncedAt = make(map[string]time.Time)
		}
		// Recorded before the fetch, not after: a server that is slow or
		// refusing shouldn't turn every message into another doomed IQ.
		s.peerDeviceSyncedAt[key] = time.Now()
	}
	s.peerDeviceSyncMu.Unlock()
	if fresh {
		return
	}

	if err := syncDevices(ctx, to); err != nil {
		slog.Debug("send: refreshing stale peer device list failed", "protocol", protocol, "jid", s.account.JID, "to", to, "err", err)
		return
	}
	slog.Debug("send: refreshed stale peer device list", "protocol", protocol, "jid", s.account.JID, "to", to)
}

// checkRecipientCoverage logs the two ways an OMEMO encrypt can succeed and
// still not reach the peer, which are otherwise invisible.
func checkRecipientCoverage(
	ctx context.Context, s *accountSession, mgr *omemolib.Manager,
	protocol omemolib.Protocol, to string, enc *omemolib.EncryptedMessage,
) {
	peerDevices, err := mgr.KnownDevices(ctx, to)
	if err != nil {
		slog.Debug("send: reading peer device list for coverage check failed", "protocol", protocol, "to", to, "err", err)
		return
	}

	keyed := make(map[omemolib.DeviceID]bool, len(enc.Keys))
	for _, k := range enc.Keys {
		keyed[k.Device] = true
	}

	// 1. No key for any device the peer publishes: only our own other
	// devices can read it.
	covered := 0
	for _, id := range peerDevices {
		if keyed[id] {
			covered++
		}
	}
	if covered == 0 {
		slog.Warn("send: message carries no key for any of the peer's devices; they will not be able to read it",
			"protocol", protocol, "jid", s.account.JID, "to", to,
			"peer_devices", peerDevices, "keyed_devices", len(enc.Keys))
	}

	// 2. A device ID out of OMEMO's range: libsignal-based clients parse
	// rid as a signed int32 and drop the whole stanza on overflow (see
	// rotateOutOfRangeDeviceID).
	for _, id := range peerDevices {
		if !id.Valid() {
			slog.Warn("send: peer publishes an unaddressable omemo device id; libsignal-based clients will drop this message entirely",
				"protocol", protocol, "jid", s.account.JID, "to", to, "device", id)
		}
	}
	if !enc.Sender.ID.Valid() {
		slog.Warn("send: our own omemo device id is unaddressable; libsignal-based clients will drop this message entirely",
			"protocol", protocol, "jid", s.account.JID, "device", enc.Sender.ID)
	}
}
