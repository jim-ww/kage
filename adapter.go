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
	"github.com/jim-ww/kage/crypto/localstore"
	"github.com/jim-ww/kage/daemon"
	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/xmpp"
	omemolib "github.com/jim-ww/omemo-go"
)

// adapter implements the daemon-side business logic RPCs are dispatched to
// (see daemon_server.go), encrypting outgoing bodies when a peer key is
// configured and persisting sent messages to storage. sessions is only ever
// appended to (by AddAccount, from an RPC handler goroutine) after startup,
// so existing indices stay stable and Send doesn't need to hold mu itself —
// mu only guards the append plus the read of len(sessions) it races with.
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

// errUploadCanceled is returned (and translated to a distinct "upload
// canceled" message before reaching the UI, see SendFile/UploadFile) when
// CancelUpload aborts an in-flight upload's context.
var errUploadCanceled = errors.New("upload canceled")

// beginUpload registers a cancelable context for the upload identified by
// path (its local file path is unique per in-flight upload, matching the ID
// FileTransferProgress/Done events already use) so a later CancelUpload call
// can abort it. done must be called once the upload finishes, successfully
// or not, to unregister it.
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

// CancelUpload aborts the in-flight upload for path, if any (a no-op
// otherwise — the upload may have already finished by the time the RPC
// arrives). Implements the daemon side of the UI's cancel-on-backspace
// action; see cancelUploadReq.
func (a *adapter) CancelUpload(path string) {
	a.uploadCancelsMu.Lock()
	cancel := a.uploadCancels[path]
	a.uploadCancelsMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// AddAccount resolves and stores the password in
// the OS keyring, persists the account to config.toml, connects it live, and
// starts its supervisor goroutine — mirroring what main does for accounts
// configured at startup, just for one account added mid-session. When
// register is true, it first creates the account on the server via XEP-0077
// in-band registration, then proceeds identically to logging in.
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
		// Prefer the OS keyring (unless use_keyring is off); if that's
		// unavailable (no Secret Service running, headless box, etc.) fall
		// back to storing the password in plaintext in config.toml rather
		// than failing the add outright.
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

	// Every attached client has to learn about this, not just the one that
	// asked: account indices are positional, so a client still on the old,
	// shorter list would place its own next added account at an index the
	// daemon has already given to this one.
	broadcast(a.srv, ui.AccountAppearedMsg{Index: accountIdx, Account: uiAcct})

	return uiAcct, nil
}

// statusConfigValue is the inverse of accountStatus: the config.toml value to
// persist for a ui.Presence account status ("" for online).
func statusConfigValue(status ui.Presence) string {
	switch status {
	case ui.PresenceChat:
		return "chat"
	case ui.PresenceAway:
		return "away"
	case ui.PresenceXA:
		return "xa"
	case ui.PresenceDND:
		return "dnd"
	case ui.PresenceOffline:
		return "offline"
	case ui.PresenceInvisible:
		return "invisible"
	default:
		return ""
	}
}

// SetAccountStatus persists the chosen
// status to config.toml (so a restart comes back up the same way), then
// applies it live - closing the connection outright for PresenceOffline (no
// further traffic to that account's server at all), dialing a currently
// offline account back up for PresenceOnline/PresenceAway, or just updating
// the advertised <show/> in place when already connected.
func (a *adapter) SetAccountStatus(accountIdx int, status ui.Presence) error {
	sess, ok := a.session(accountIdx)
	if !ok {
		return fmt.Errorf("unknown account %d", accountIdx)
	}
	if err := config.SetAccountStatus(a.cfgPath, sess.account.JID, statusConfigValue(status)); err != nil {
		return fmt.Errorf("persisting status: %w", err)
	}
	sess.account.Status = statusConfigValue(status)
	// Best-effort: this SIGHUPs the daemon's own process to re-read
	// config.toml (see daemon.SignalReload) for settings like Notifications
	// that aren't already reflected in the live session state above - never
	// a reason to fail the status change itself.
	if err := daemon.SignalReload(); err != nil {
		slog.Warn("signaling daemon to reload after status change", "err", err)
	}

	ctx := context.Background()
	client, offlineErr := sess.liveClient()
	online := offlineErr == nil

	// The wire-level counterpart lives in xmpp.Client.SetPresence, but going
	// offline closes the connection instead of advertising anything, so it
	// would leave no trace at all without this.
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

	// presenceShow has no <show/> value for invisible - going invisible while
	// offline dials with plain show (connectAccountLive/Dial can't skip their
	// own unconditional available-presence blast), then SetInvisible below
	// corrects it, same brief-visible-window trade-off connectAccountLive
	// already has for every non-default status.
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
		// Don't just give up: the account stays valid for queuing sends
		// (see accountSession.outbox), and this keeps retrying with
		// backoff until it actually gets online, same as a failed initial
		// connect at startup.
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

	// Everything sent to this account while it was switched off went to the
	// archive and nowhere else, exactly as during a disconnect - so this
	// needs the same backfill every other path back online already does
	// (connectAndSuperviseAccount, retryInitialConnect, superviseAccount's
	// reconnect). Without it, flipping an account back to online reconnects
	// but never asks for what it missed, and those messages stay invisible
	// until the whole app is restarted and the startup sync picks them up.
	//
	// Detached, unlike the startup path that runs it inline: this is an RPC
	// the TUI is blocked on, and a backfill can take tens of seconds.
	go triggerArchiveSync(ctx, a.srv, accountIdx, sess)

	broadcast(a.srv, ui.AccountStatusChangedMsg{
		Index: accountIdx, Status: status,
		NewChats: newChats, NewMessages: newMessages, NewHistoryMore: newHistoryMore,
	})
	return nil
}

// session returns the accountSession at accountIdx, guarded by mu since
// AddAccount appends to a.sessions concurrently with reads from here.
func (a *adapter) session(accountIdx int) (*accountSession, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if accountIdx < 0 || accountIdx >= len(a.sessions) || a.sessions[accountIdx] == nil {
		return nil, false
	}
	return a.sessions[accountIdx], true
}

// LoadHistoryWindow fetches a fresh window of
// to's persisted history around anchor (see loadHistoryWindow) as a
// tea.Cmd, off the main goroutine, since it's a disk read plus decrypt of up
// to a window's worth of messages.
func (a *adapter) LoadHistoryWindow(accountIdx int, to string, anchor *ui.HistoryAnchor) (ui.HistoryWindowMsg, error) {
	sess, ok := a.session(accountIdx)
	if !ok {
		return ui.HistoryWindowMsg{}, fmt.Errorf("unknown account %d", accountIdx)
	}
	msgs, hasOlder, hasNewer := loadHistoryWindow(context.Background(), sess, to, sess.rosterName(to), anchor, historyPageSize)
	return ui.HistoryWindowMsg{AccountIdx: accountIdx, From: to, Messages: msgs, HasOlder: hasOlder, HasNewer: hasNewer}, nil
}

// SearchHistory decrypts and scans to's
// entire persisted history (see loadHistory) as a tea.Cmd, off the main
// goroutine, and returns every message whose content contains query
// (case-insensitive) alongside the whole decrypted history — the caller
// loads that wholesale rather than making a second round trip once a result
// is picked.
func (a *adapter) SearchHistory(accountIdx int, to, query string) (ui.HistorySearchResultMsg, error) {
	sess, ok := a.session(accountIdx)
	if !ok {
		return ui.HistorySearchResultMsg{}, fmt.Errorf("unknown account %d", accountIdx)
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
		// Re-evaluate the tray dot: a hidden chat stops feeding it (see
		// badgeable), an unhidden one starts again with whatever count
		// storage held all along.
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

// SetFocusState records whether the calling
// TUI's terminal has OS focus and which chat (if any) it currently has
// open, so handleIncomingMessage (events.go) can skip a desktop
// notification for a message that's already visible on screen.
func (a *adapter) SetFocusState(tuiClient ipc.ClientID, accountJID, chatAddress string, focused bool) error {
	key := focusedChatKey(accountJID, chatAddress)
	clientFocus.set(tuiClient, focusState{focused: focused, chatKey: key})
	slog.Debug("focus state updated", "client", tuiClient, "accountJID", accountJID, "chatAddress", chatAddress, "focused", focused, "key", key)
	return nil
}

// ResetChatUnread zeroes the persisted
// unread counter for a chat, called when it's opened in the UI, and tells
// every attached TUI so the chat reads as read in all of them.
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

// ChatUnreadCounts loads every chat with a
// nonzero persisted unread count for an account, keyed by chat address, so
// the UI can seed Chat.Unread when (re)connecting.
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

// SaveDraft persists chatAddress's compose-box
// text, sealed under the local storage key (crypto/localstore) same as a
// message body when one is configured, or clears the persisted draft
// entirely when text is empty (rather than storing an empty row).
func (a *adapter) SaveDraft(accountJID, chatAddress, text string) error {
	if text == "" {
		return a.queries.DeleteChatDraft(context.Background(), storage.DeleteChatDraftParams{
			AccountJid: accountJID,
			RosterJid:  chatAddress,
		})
	}
	body, encrypted := text, false
	if a.localKey != nil {
		if ct, err := localstore.Seal(a.localKey, text); err == nil {
			body, encrypted = ct, true
		} else {
			slog.Warn("encrypting draft for storage", "err", err)
		}
	}
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

// FetchOwnDeviceList fetches the
// account's currently-published device list for both OMEMO protocols (each
// runs its own separate device pool - see account.go), tagging every entry
// with which protocol it belongs to.
func (a *adapter) FetchOwnDeviceList(accountIdx int) (ui.OmemoDeviceListMsg, error) {
	s, ok := a.session(accountIdx)
	if !ok {
		return ui.OmemoDeviceListMsg{}, fmt.Errorf("account is still connecting")
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

// PurgeOwnDeviceList republishes each
// affected protocol's device list containing only the entries in keep for
// that protocol (this instance's own devices are always included
// regardless, so it can never accidentally remove itself). A protocol whose
// device list is unchanged (no entries in keep or removed for it) isn't
// republished.
func (a *adapter) PurgeOwnDeviceList(accountIdx int, keep []ui.OmemoDevice) (ui.OmemoDevicePurgedMsg, error) {
	s, ok := a.session(accountIdx)
	if !ok {
		return ui.OmemoDevicePurgedMsg{}, fmt.Errorf("account is still connecting")
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

// RemoveAccount purges this account's own
// device from each OMEMO protocol's published device list (so peers stop
// treating it as a valid encryption target), disconnects it, and drops it
// from config.toml. Local storage (history, roster cache, OMEMO
// identity/session state) is never touched — sessions keeps the slot (see
// ui.AccountRemovedMsg for why indices must stay stable), just closed, so
// its reconnect supervisor goroutine exits instead of retrying.
func (a *adapter) RemoveAccount(accountIdx int) error {
	s, ok := a.session(accountIdx)
	if !ok {
		return fmt.Errorf("account is still connecting")
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

// MarkRetracted flags a message as locally
// deleted in storage without sending anything over the network. Used when
// deleting someone else's message, since XEP-0424 retraction only applies to
// messages we sent ourselves.
func (a *adapter) MarkRetracted(accountIdx int, to, id string) error {
	s, ok := a.session(accountIdx)
	if !ok {
		return fmt.Errorf("unknown account %d", accountIdx)
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

// PurgeMessage erases a message outright from
// local storage, reactions included, and broadcasts a MessagePurgedMsg so every
// attached client drops it too. The second half of the two-step delete - only
// reached for a message already flagged retracted (see MarkRetracted and
// Send's RetractID branch), since the first delete is the one that preserves
// a record of what was said. Nothing goes on the wire: XMPP has no "erase it
// from the archive" stanza, so this only ever affects our own copy.
func (a *adapter) PurgeMessage(accountIdx int, to, id string) error {
	s, ok := a.session(accountIdx)
	if !ok {
		return fmt.Errorf("unknown account %d", accountIdx)
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

// DeleteQueued permanently discards a
// still-Pending message from the outbox table without ever sending it. Not
// an error if localID no longer matches anything queued - broadcasting
// an OutboxDeletedMsg (so every attached client removes the same placeholder,
// not just the one that asked) only happens when a row actually existed to
// delete.
func (a *adapter) DeleteQueued(accountIdx int, localID string) error {
	s, ok := a.session(accountIdx)
	if !ok {
		return fmt.Errorf("unknown account %d", accountIdx)
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

// SetTyping sends a XEP-0085 chat state
// notification to "to" — no persistence, no encryption, it's ephemeral.
// A sent "composing" is remembered per calling client so clearTyping can take
// it back if that TUI goes away before clearing it itself.
func (a *adapter) SetTyping(tuiClient ipc.ClientID, accountIdx int, to string, composing bool) error {
	s, ok := a.session(accountIdx)
	if !ok {
		return fmt.Errorf("unknown account %d", accountIdx)
	}
	client, err := s.liveClient()
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

// clearTyping sends "active" to every peer tuiClient left in a "composing"
// state via SetTyping. The daemon outlives the TUI, so without this a client
// that quits (or dies) mid-keystroke — before its own pause timeout or quit
// path clears the state — leaves the peer's client showing a typing indicator
// indefinitely: nothing else on the wire ever contradicts it.
func (a *adapter) clearTyping(tuiClient ipc.ClientID) {
	for accountIdx, to := range a.drainTyping(tuiClient) {
		s, ok := a.session(accountIdx)
		if !ok {
			continue
		}
		client, err := s.liveClient()
		if err != nil {
			continue
		}
		if err := client.SendChatState(context.Background(), to, xmpp.ChatStateActive); err != nil {
			slog.Warn("adapter: clearing typing state", "account", accountIdx, "to", to, "err", err)
		}
	}
}

// RenameContact pushes name as a roster set
// (RFC 6121) to the server, and mirrors it into local storage and the
// in-memory roster cache used by rosterName. An empty name clears the
// contact's nickname there too, matching what a fresh roster fetch would
// show after the server applies the same change.
func (a *adapter) RenameContact(accountIdx int, address, name string) error {
	s, ok := a.session(accountIdx)
	if !ok {
		return fmt.Errorf("unknown account %d", accountIdx)
	}

	client, err := s.liveClient()
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

// AddContact adds address to the roster and
// sends a subscription request, then mirrors it into local storage and the
// in-memory roster cache used by rosterName.
func (a *adapter) AddContact(accountIdx int, address string) error {
	s, ok := a.session(accountIdx)
	if !ok {
		return fmt.Errorf("unknown account %d", accountIdx)
	}

	ctx := context.Background()
	client, err := s.liveClient()
	if err != nil {
		return err
	}
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
	s, ok := a.session(accountIdx)
	if !ok {
		return fmt.Errorf("unknown account %d", accountIdx)
	}

	ctx := context.Background()
	client, err := s.liveClient()
	if err != nil {
		return err
	}
	if err := client.ResubscribeContact(ctx, address); err != nil {
		return err
	}
	return nil
}

// RemoveContact removes address from the
// roster and unsubscribes, then mirrors the removal into local storage and
// the in-memory roster cache.
func (a *adapter) RemoveContact(accountIdx int, address string) error {
	s, ok := a.session(accountIdx)
	if !ok {
		return fmt.Errorf("unknown account %d", accountIdx)
	}

	ctx := context.Background()
	client, err := s.liveClient()
	if err != nil {
		return err
	}
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

// SetChatEncryption persists the chosen
// outgoing message encryption ("omemo-v1", "omemo-v2", "gpg", or "none") for
// one chat.
func (a *adapter) SetChatEncryption(accountIdx int, peerJID, mode string) error {
	s, ok := a.session(accountIdx)
	if !ok {
		return fmt.Errorf("unknown account %d", accountIdx)
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

// flushOutbox replays every send persisted to the outbox table (see its
// schema doc comment) while s was offline, oldest first. Plain sends
// (messages, reactions, retractions, corrections) go straight through
// a.send, the same path a live send would take, so encryption is
// (re)resolved against the fresh connection. Attachments were never
// uploaded, so those go through the same upload-then-send steps
// SendFile/UploadFile+Send would - the result wasn't the return value of a
// Bubble Tea command the UI is waiting on (that call already returned
// "queued" and moved on), so it's pushed into the UI via the same live-event
// broadcast path an incoming message uses.
//
// Each row is deleted right before it's actually attempted, not after - if
// the connection drops again mid-attempt, a.send's own offline branch
// re-inserts it as a fresh row via enqueueOutbox, so nothing is lost, it's
// just replayed under a new row id next time. Deleting after instead would
// risk the opposite: a second concurrent flush (or a slow send outliving a
// disconnect/reconnect cycle) re-sending the same row twice before either
// gets to delete it. Called by reconnectWithBackoff and
// connectAndSuperviseAccount right after either restores s's client.
func (a *adapter) flushOutbox(ctx context.Context, s *accountSession) {
	if _, err := s.liveClient(); err != nil {
		// Nothing to do - called speculatively, or dropped again immediately
		// after reconnecting. Every row stays exactly where it is for the
		// next attempt.
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
				// Went offline again between the top-level check above and
				// this send - a.send already re-enqueued it (as a fresh
				// row), so the UI's placeholder is still accurately Pending.
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
				encrypted, method := false, ""
				switch mode := resolveEncryptionMode(ctx, s, q.to); mode {
				case "gpg":
					encrypted, method = true, "gpg"
				case "omemo-v1", "omemo-v2":
					encrypted, method = true, mode
				case "none", "":
				default:
					encrypted, method = true, "omemo"
				}
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
			// The upload itself failed (not an offline/queued outcome) -
			// same as a plain send's real-failure case, this row is done,
			// not worth retrying forever. Persisted the same way a.send's own
			// defer does for a plain-message failure (see markOutboxFailed's
			// doc comment) - this fails before ever reaching a.send, so
			// nothing else does it here - and resolved by LocalID so the
			// placeholder startAttachedSend showed flips from Pending to
			// Failed instead of sitting unresolved forever.
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
			// Went offline again between the upload finishing and this send -
			// a.send already re-enqueued it (as a fresh plain-text row, now
			// containing the real URL since the upload itself already
			// succeeded) - the UI's placeholder is still accurately Pending.
		case sendErr != nil:
			slog.Warn("sending queued attachment failed", "jid", s.account.JID, "to", q.to, "path", q.filePath, "err", sendErr)
			if q.opts.LocalID != "" {
				broadcast(a.srv, ui.MessageSendResolvedMsg{
					AccountIdx: s.accountIdx, To: q.to, LocalID: q.opts.LocalID, Err: sendErr.Error(),
				})
			}
		default:
			// Resolves the Pending placeholder startAttachedSend showed when
			// this was first queued (see MessageSendResolvedMsg.LocalID) -
			// not a new IncomingMessageMsg, which would just duplicate it: the
			// placeholder is already sitting in chat history, only its
			// ID/Encrypted/EncMethod/Content/Attachments need to catch up now
			// that the real URL is known.
			if q.opts.LocalID != "" {
				encrypted, method := false, ""
				switch mode := resolveEncryptionMode(ctx, s, q.to); mode {
				case "gpg":
					encrypted, method = true, "gpg"
				case "omemo-v1", "omemo-v2":
					encrypted, method = true, mode
				case "none", "":
				default:
					encrypted, method = true, "omemo"
				}
				broadcast(a.srv, ui.MessageSendResolvedMsg{
					AccountIdx: s.accountIdx, To: q.to, LocalID: q.opts.LocalID,
					ID: id, Encrypted: encrypted, EncMethod: method,
					Content: body, Attachments: []string{url},
				})
			}
		}
	}
}

// send is the context-aware implementation behind Send. File uploads use it
// with their deadline so a subsequent peer-key lookup or stanza send cannot
// outlive the operation that initiated it. opts.OOBURLs, if given, marks
// those URLs as XEP-0066 attachments - only applied when the send ends up
// unencrypted, since attaching them in the clear would leak the URL
// alongside an encrypted body.
func (a *adapter) send(ctx context.Context, accountIdx int, to, body string, opts ui.SendOptions) (id string, err error) {
	s, valid := a.session(accountIdx)
	if !valid {
		return "", fmt.Errorf("unknown account %d", accountIdx)
	}

	// A real (non-offline, non-queued) failure below - "omemo not ready", an
	// encrypt error, the server rejecting the stanza - must survive this
	// process restarting the same as a queued send does, or it silently
	// vanishes the moment a different TUI process (which never saw this
	// return value) reloads its view from storage. Skipped for
	// reactions/retractions/corrections: those target an existing message
	// that's already durably stored, nothing here needs to survive on their
	// behalf. See markOutboxFailed's doc comment.
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
		// Queue and replay verbatim once reconnected. Reactions/retractions/
		// corrections reference a target ID from an earlier, already-sent
		// message, so replaying them later is safe as long as that target
		// wasn't itself still queued (see enqueueOutbox).
		//
		// ui.ErrQueued (not nil) is returned so the caller can tell "queued,
		// will go out automatically" apart from "sent" - a nil error here
		// used to be indistinguishable from success up at the UI, which then
		// had no way to avoid rendering an unsent message exactly like a
		// delivered one.
		if qErr := s.enqueueOutbox(ctx, to, body, opts); qErr != nil {
			// Persisting the queue entry itself failed - this is a genuine
			// failure, not "queued, will go out automatically": returning
			// ErrQueued here would tell the UI everything's fine when the
			// message was actually never recorded anywhere.
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
		// The retraction body is fixed fallback text, not user content —
		// nothing to encrypt for the peer. We never delete our own copy: a
		// retraction only flags the message, it doesn't erase local history.
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
			// Deliberately a bare plaintext string, not a structured envelope
			// carrying OOB metadata: an earlier version of this code wrapped
			// it in XML so encrypted file messages could still get attachment
			// styling, but that showed as literal "<kage-payload><body>..."
			// noise in every other OMEMO client (Dino, Conversations, etc).
			// Real interop matters more than attachment icons on encrypted
			// files, so those just render as plain text/links instead - same
			// as GPG.
			refreshPeerDevicesIfStale(ctx, s, protocol, to, mgr.SyncDevices)
			enc, deviceErrs, err := mgr.EncryptMessage(ctx, to, []byte(plaintext))
			if err != nil {
				// The manager only auto-fetches a peer's device list when its
				// cache is empty, so a peer that rotated/added devices since
				// our last fetch looks unencryptable until we force a resync.
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
				// EncryptMessage is best-effort and only returns a non-nil
				// err when EVERY device failed - a partial failure (some
				// devices got a key, one silently didn't) is otherwise
				// invisible, which is exactly what makes "encrypted to the
				// wrong/stale devices, skipped the one that would actually
				// decrypt" undiagnosable without this.
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
			// Chat is configured for OMEMO but the manager for the resolved
			// protocol never came up (see setupOmemoProtocol) - refuse to
			// send rather than silently falling back to plaintext for a chat
			// the user explicitly marked as encrypted.
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
		// The editing TUI already shows the new text; every other attached
		// one only learns of it from this - same reason as the plain-send
		// broadcast below.
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
		// This send may be a manual retry (ui.actionRetryMessage) of a prior
		// failure markOutboxFailed recorded under this same LocalID. Now that
		// it succeeded and is about to get its own fresh messages row below,
		// that stale failed outbox row must go - otherwise it survives
		// forever and keeps reappearing as a Failed placeholder (see
		// pendingOutboxMessagesByPeer) alongside the now-successfully-sent
		// message on every reload/reconnect.
		if _, _, err := s.deleteOutboxByLocalID(ctx, opts.LocalID); err != nil {
			slog.Warn("clearing stale outbox entry after successful retry", "jid", s.account.JID, "to", to, "err", err)
		}
	}
	if opts.SupersedesID != "" {
		// The other shape of retry: the earlier attempt already made it into
		// the messages table under its own ID before an ack timeout flagged
		// it sendFailed (see SendOptions.SupersedesID's doc comment). This
		// send is getting its own fresh row below, so the old one must be
		// removed here too, or it survives forever as a stale Failed
		// duplicate.
		if _, err := s.db.DeleteMessageByID(ctx, storage.DeleteMessageByIDParams{
			AccountJid: s.account.JID,
			IDAttr:     nullString(opts.SupersedesID),
			RosterJid:  nullString(to),
		}); err != nil {
			slog.Warn("clearing superseded failed message after successful retry", "jid", s.account.JID, "to", to, "err", err)
		}
	}

	// Snapshot the replied-to message's author+preview (already known from
	// the UI's own loaded state - see SendOptions.QuotedAuthor/QuotedBody's
	// doc comment) so the reply indicator survives even once that message
	// falls outside whatever window of history gets loaded later (see
	// ui.Message.QuotedAuthor/QuotedPreview).
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

	// More than one TUI process can be attached to this daemon at once (see
	// cmd_daemon.go) sharing this one account's connection - every one of
	// them besides the client that made this particular Send RPC (which
	// already rendered its own local optimistic echo, see
	// ui/message_actions.go's sendCurrentInput) needs to learn about this
	// message some other way, the same way it learns about a message a peer
	// sent. Reusing IncomingMessageMsg (rather than inventing a "just sent by
	// another instance" event) means every attached client, including the
	// sender's own, goes through the exact same handling as a real incoming
	// message - the sender's own copy is a harmless duplicate broadcast of
	// something it already rendered, deduped there by matching message ID
	// (see IncomingMessageMsg's handler in ui/update_messages.go).
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

// uploadFile applies the same peer encryption policy as an outgoing message
// (XEP-0454 AES-256-GCM when the resolved mode is encrypted, plain XEP-0363
// otherwise) and uploads path, returning the URL to put in a message body.
// Shared by SendFile (upload + immediate send) and UploadFile (upload only,
// for staging a pending attachment). onProgress, if non-nil, is called with
// cumulative bytes sent as the PUT streams.
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

	url, err := client.UploadFileWithReader(ctx, path, bytes.NewReader(ciphertext), onProgress)
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

// cacheUploadedAttachment copies a just-uploaded local file to the same
// location a download of url would use (see ui.AttachmentLocalPath), so a
// file we send is available locally in the same format/place as one we'd
// receive, without re-downloading it from the server. Best-effort: a failure
// here doesn't affect the send, which already succeeded.
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

// progressCallback returns a throttled onProgress func suitable for
// xmpp.Client.UploadFile/UploadFileWithReader (or a download loop): it only
// sends a ui.FileTransferProgressMsg to the UI when the whole percentage
// changes, so a large file doesn't flood the update loop with a message per
// 32KB chunk. id is an opaque per-transfer key (e.g. local path for an
// upload, URL for a download) letting the UI track multiple concurrent
// transfers separately.
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
	s, ok := a.session(accountIdx)
	if !ok {
		return result, fmt.Errorf("unknown account %d", accountIdx)
	}
	client, err := s.liveClient()
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

// UploadFile uploads path. Unlike SendFile it only uploads —
// used by startAttachedSend to upload+send each staged attachment as its own
// message. text/opts are only consulted when the account is offline: rather
// than fail (upload needs a live connection, unlike a plain text send),
// upload+send is queued as one unit and replayed together by
// adapter.flushOutbox once reconnected.
func (a *adapter) UploadFile(accountIdx int, to, path, text string, opts ui.SendOptions) (ui.FileUploadResultMsg, error) {
	result := ui.FileUploadResultMsg{Path: path}
	s, ok := a.session(accountIdx)
	if !ok {
		return result, fmt.Errorf("unknown account %d", accountIdx)
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

// --- voice calls (XEP-0166/0167/0176/0353, see callsession.go) ---
//
// Blocking-in-the-RPC-handler is fine here for the same reason SetTyping is:
// each of these only builds and writes one stanza. The slow parts (ICE
// gathering, DTLS, audio) happen on the call's own goroutines and report
// back via CallStateMsg broadcasts.

// CallAction does action to accountIdx's call.
func (a *adapter) CallAction(accountIdx int, action ui.CallAction) error {
	sess, ok := a.session(accountIdx)
	if !ok {
		return fmt.Errorf("unknown account %d", accountIdx)
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
// trusted before a send re-fetches it. Long enough that a busy conversation
// doesn't put a PEP IQ in front of every message, short enough that a
// missed device-list push doesn't go unnoticed for the life of the daemon.
const peerDeviceSyncInterval = 30 * time.Minute

// refreshPeerDevicesIfStale re-fetches to's OMEMO device list if it hasn't
// been synced recently.
//
// omemo-go only auto-fetches a peer's list when its cache is completely
// empty, and send only forces a resync after an encrypt fails outright. A
// cache that is stale but non-empty therefore encrypts happily to the wrong
// set of devices and reports success - the peer's newest device simply never
// gets a key and the message never appears there, with nothing in the log
// beyond a lower-than-expected key count. Device-list PEP pushes normally
// keep the cache honest, but a push missed while either side was
// reconnecting is never resent.
//
// Best-effort: a failed fetch leaves the cache as it was and lets the send
// proceed, which is no worse than not having tried.
// syncDevices is mgr.SyncDevices, taken as a parameter so the throttle can
// be tested without a live Manager.
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
// still be undeliverable. Neither is recoverable here - both are about the
// peer's published devices - but both are invisible without this, which is
// what made them take a packet capture and a peer's logcat to find.
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

	// 1. No key for any device the peer publishes. The stanza goes out, the
	// server accepts it, and it can only ever reach our own other clients -
	// the exact shape of "he never got it but it showed up on my phone".
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

	// 2. A recipient device ID outside OMEMO's addressable range. Every
	// libsignal-derived client parses sid/rid as a signed 32-bit int and
	// drops the WHOLE stanza on overflow, keys for its own devices
	// included - so one such device in the list makes the message
	// undeliverable to every Conversations/Dino/Gajim device in the chat,
	// not just to that one. Nothing can be done from this side; it resolves
	// when that device's client re-numbers itself (see
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
