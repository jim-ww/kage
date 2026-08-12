package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
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
}

// AddAccount implements ui.AccountAdder: resolves and stores the password in
// the OS keyring, persists the account to config.yaml, connects it live, and
// starts its supervisor goroutine — mirroring what main does for accounts
// configured at startup, just for one account added mid-session. When
// register is true, it first creates the account on the server via XEP-0077
// in-band registration, then proceeds identically to logging in.
func (a *adapter) AddAccount(jid, password, gpgKeyID string, register bool) tea.Msg {
	if register {
		if err := xmpp.Register(context.Background(), jid, password, nil); err != nil {
			return ui.AccountAddErrorMsg{Err: fmt.Errorf("registering account: %w", err)}
		}
	}

	acct := config.Account{JID: jid, GPGKeyID: gpgKeyID}
	if password != "" {
		// Prefer the OS keyring (unless use_keyring is off); if that's
		// unavailable (no Secret Service running, headless box, etc.) fall
		// back to storing the password in plaintext in config.yaml rather
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
		return ui.AccountAddErrorMsg{Err: fmt.Errorf("saving account to config: %w", err)}
	}

	ctx := context.Background()
	sess, uiAcct, err := connectAccount(ctx, acct, a.queries, a.localKey, a.useGPG, a.useKeyring)
	if err != nil {
		return ui.AccountAddErrorMsg{Err: err}
	}

	a.mu.Lock()
	accountIdx := len(a.sessions)
	sess.accountIdx = accountIdx
	a.sessions = append(a.sessions, sess)
	a.cfgAccounts = append(a.cfgAccounts, acct)
	a.mu.Unlock()

	go superviseAccount(ctx, a.srv, a, accountIdx, sess)

	return ui.AccountAddedMsg{Account: uiAcct}
}

// statusConfigValue is the inverse of accountStatus: the config.yaml value to
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
	default:
		return ""
	}
}

// SetAccountStatus implements ui.AccountStatusSetter: persists the chosen
// status to config.yaml (so a restart comes back up the same way), then
// applies it live - closing the connection outright for PresenceOffline (no
// further traffic to that account's server at all), dialing a currently
// offline account back up for PresenceOnline/PresenceAway, or just updating
// the advertised <show/> in place when already connected.
func (a *adapter) SetAccountStatus(accountIdx int, status ui.Presence) tea.Msg {
	sess, ok := a.session(accountIdx)
	if !ok {
		return ui.AccountStatusSetMsg{Index: accountIdx, Err: fmt.Errorf("unknown account %d", accountIdx)}
	}
	if err := config.SetAccountStatus(a.cfgPath, sess.account.JID, statusConfigValue(status)); err != nil {
		return ui.AccountStatusSetMsg{Index: accountIdx, Err: fmt.Errorf("persisting status: %w", err)}
	}
	sess.account.Status = statusConfigValue(status)
	// Best-effort: this SIGHUPs the daemon's own process to re-read
	// config.yaml (see daemon.SignalReload) for settings like Notifications
	// that aren't already reflected in the live session state above - never
	// a reason to fail the status change itself.
	if err := daemon.SignalReload(); err != nil {
		slog.Warn("signaling daemon to reload after status change", "err", err)
	}

	ctx := context.Background()
	client, offlineErr := sess.liveClient()
	online := offlineErr == nil

	if status == ui.PresenceOffline {
		if online {
			if err := client.Close(); err != nil {
				slog.Warn("closing while going offline", "jid", sess.account.JID, "err", err)
			}
		}
		return ui.AccountStatusSetMsg{Index: accountIdx, Status: status}
	}

	show := presenceShow(status)
	if online {
		if err := client.SetPresence(ctx, show); err != nil {
			return ui.AccountStatusSetMsg{Index: accountIdx, Status: status, Err: fmt.Errorf("updating presence: %w", err)}
		}
		return ui.AccountStatusSetMsg{Index: accountIdx, Status: status}
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
		return ui.AccountStatusSetMsg{Index: accountIdx, Status: status, Err: err}
	}
	go superviseAccount(ctx, a.srv, a, accountIdx, sess)
	return ui.AccountStatusSetMsg{
		Index: accountIdx, Status: status,
		NewChats: newChats, NewMessages: newMessages, NewHistoryMore: newHistoryMore,
	}
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

// LoadOlderHistory implements ui.HistoryLoader: fetches the next older page
// of to's persisted history (see loadHistoryPage) as a tea.Cmd, off the main
// goroutine, since it's a disk read plus decrypt of up to a page's worth of
// messages.
func (a *adapter) LoadOlderHistory(accountIdx int, to string) tea.Cmd {
	sess, ok := a.session(accountIdx)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		msgs, hasMore := loadHistoryPage(context.Background(), sess, to, sess.rosterName(to))
		return ui.OlderHistoryMsg{AccountIdx: accountIdx, From: to, Messages: msgs, HasMore: hasMore}
	}
}

// SearchHistory implements ui.HistorySearcher: decrypts and scans to's
// entire persisted history (see loadHistory) as a tea.Cmd, off the main
// goroutine, and returns every message whose content contains query
// (case-insensitive) alongside the whole decrypted history — the caller
// loads that wholesale rather than making a second round trip once a result
// is picked.
func (a *adapter) SearchHistory(accountIdx int, to, query string) tea.Cmd {
	sess, ok := a.session(accountIdx)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		msgs := loadHistory(context.Background(), sess, to, sess.rosterName(to))
		needle := strings.ToLower(query)
		var matches []int
		for i, msg := range msgs {
			if strings.Contains(strings.ToLower(msg.Content), needle) {
				matches = append(matches, i)
			}
		}
		return ui.HistorySearchResultMsg{AccountIdx: accountIdx, From: to, Query: query, Messages: msgs, Matches: matches}
	}
}

// SetDefaultAccount implements ui.DefaultAccountSetter: persists jid as the
// account selected on startup.
func (a *adapter) SetDefaultAccount(jid string) error {
	return config.SetDefaultAccount(a.cfgPath, jid)
}

// SetSidebarWidth implements ui.SidebarWidthSetter: persists the
// user-dragged sidebar width so it's restored on the next launch.
func (a *adapter) SetSidebarWidth(width int) error {
	return config.SetSidebarWidth(a.cfgPath, width)
}

// SetSidebarHidden implements ui.SidebarHiddenSetter: persists the chat
// list's visibility so it's restored on the next launch.
func (a *adapter) SetSidebarHidden(hidden bool) error {
	return config.SetSidebarHidden(a.cfgPath, hidden)
}

// SetInputHeight implements ui.InputHeightSetter: persists the user-dragged
// compose box height so it's restored on the next launch.
func (a *adapter) SetInputHeight(height int) error {
	return config.SetInputHeight(a.cfgPath, height)
}

// SetFilePickerSort implements ui.FilePickerSortSetter: persists the
// attach-file picker's sort field/direction so it's restored on the next
// launch.
func (a *adapter) SetFilePickerSort(field string, ascending bool) error {
	return config.SetFilePickerSort(a.cfgPath, field, ascending)
}

// SetLastChat implements ui.LastChatSetter: persists which chat was last
// opened so it can be reopened on startup when open_last_chat is set.
func (a *adapter) SetLastChat(accountJID, chatAddress string) error {
	return config.SetLastChat(a.cfgPath, accountJID, chatAddress)
}

// SetFocusState implements ui.FocusReporter: records whether the attached
// TUI's terminal has OS focus and which chat (if any) it currently has
// open, so handleIncomingMessage (events.go) can skip a desktop
// notification for a message that's already visible on screen.
func (a *adapter) SetFocusState(accountJID, chatAddress string, focused bool) error {
	tuiFocused.Store(focused)
	key := focusedChatKey(accountJID, chatAddress)
	tuiActiveChat.Store(key)
	slog.Debug("focus state updated", "accountJID", accountJID, "chatAddress", chatAddress, "focused", focused, "key", key)
	return nil
}

// IncrementChatUnread implements ui.ChatReadTracker: bumps the persisted
// local-only unread counter for a chat by delta.
func (a *adapter) IncrementChatUnread(accountJID, chatAddress string, delta int) error {
	return a.queries.IncrementChatUnread(context.Background(), storage.IncrementChatUnreadParams{
		AccountJid: accountJID,
		RosterJid:  chatAddress,
		Delta:      int64(delta),
	})
}

// ResetChatUnread implements ui.ChatReadTracker: zeroes the persisted
// unread counter for a chat, called when it's opened in the UI.
func (a *adapter) ResetChatUnread(accountJID, chatAddress string) error {
	return a.queries.ResetChatUnread(context.Background(), storage.ResetChatUnreadParams{
		AccountJid: accountJID,
		RosterJid:  chatAddress,
	})
}

// ChatUnreadCounts implements ui.ChatReadTracker: loads every chat with a
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

// SaveDraft implements ui.DraftSaver: persists chatAddress's compose-box
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

// FetchOwnDeviceList implements ui.OmemoDeviceManager: fetches the
// account's currently-published device list for both OMEMO protocols (each
// runs its own separate device pool - see account.go), tagging every entry
// with which protocol it belongs to.
func (a *adapter) FetchOwnDeviceList(accountIdx int) tea.Msg {
	s, ok := a.session(accountIdx)
	if !ok {
		return ui.OmemoDeviceListMsg{AccountIdx: accountIdx, Err: fmt.Errorf("account is still connecting")}
	}
	if s.omemoMgrV2 == nil && s.omemoMgrV1 == nil {
		return ui.OmemoDeviceListMsg{AccountIdx: accountIdx, Err: fmt.Errorf("omemo isn't ready for this account")}
	}

	ctx := context.Background()
	client := s.client.Load()
	var local, devices []ui.OmemoDevice

	if s.omemoMgrV2 != nil {
		local = append(local, ui.OmemoDevice{Protocol: "v2", ID: uint32(s.omemoMgrV2.LocalDevice().ID)})
		list, err := client.FetchOmemoDeviceList(ctx, s.account.JID)
		if err != nil {
			return ui.OmemoDeviceListMsg{AccountIdx: accountIdx, Err: fmt.Errorf("fetching omemo-v2 device list: %w", err)}
		}
		for _, d := range list.Devices {
			devices = append(devices, ui.OmemoDevice{Protocol: "v2", ID: uint32(d)})
		}
	}
	if s.omemoMgrV1 != nil {
		local = append(local, ui.OmemoDevice{Protocol: "v1", ID: uint32(s.omemoMgrV1.LocalDevice().ID)})
		list, err := client.FetchOmemoDeviceListV1(ctx, s.account.JID)
		if err != nil {
			return ui.OmemoDeviceListMsg{AccountIdx: accountIdx, Err: fmt.Errorf("fetching omemo-v1 device list: %w", err)}
		}
		for _, d := range list.Devices {
			devices = append(devices, ui.OmemoDevice{Protocol: "v1", ID: uint32(d)})
		}
	}

	return ui.OmemoDeviceListMsg{AccountIdx: accountIdx, Local: local, Devices: devices}
}

// PurgeOwnDeviceList implements ui.OmemoDeviceManager: republishes each
// affected protocol's device list containing only the entries in keep for
// that protocol (this instance's own devices are always included
// regardless, so it can never accidentally remove itself). A protocol whose
// device list is unchanged (no entries in keep or removed for it) isn't
// republished.
func (a *adapter) PurgeOwnDeviceList(accountIdx int, keep []ui.OmemoDevice) tea.Msg {
	s, ok := a.session(accountIdx)
	if !ok {
		return ui.OmemoDevicePurgedMsg{AccountIdx: accountIdx, Err: fmt.Errorf("account is still connecting")}
	}
	if s.omemoMgrV2 == nil && s.omemoMgrV1 == nil {
		return ui.OmemoDevicePurgedMsg{AccountIdx: accountIdx, Err: fmt.Errorf("omemo isn't ready for this account")}
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

	if err := purge(omemolib.ProtocolV2, s.omemoMgrV2, client.PublishOmemoDeviceList); err != nil {
		return ui.OmemoDevicePurgedMsg{AccountIdx: accountIdx, Err: err}
	}
	if err := purge(omemolib.ProtocolV1, s.omemoMgrV1, client.PublishOmemoDeviceListV1); err != nil {
		return ui.OmemoDevicePurgedMsg{AccountIdx: accountIdx, Err: err}
	}

	return ui.OmemoDevicePurgedMsg{AccountIdx: accountIdx, Local: local, Devices: devices}
}

// RemoveAccount implements ui.AccountRemover: purges this account's own
// device from each OMEMO protocol's published device list (so peers stop
// treating it as a valid encryption target), disconnects it, and drops it
// from config.yaml. Local storage (history, roster cache, OMEMO
// identity/session state) is never touched — sessions keeps the slot (see
// ui.AccountRemovedMsg for why indices must stay stable), just closed, so
// its reconnect supervisor goroutine exits instead of retrying.
func (a *adapter) RemoveAccount(accountIdx int) tea.Msg {
	s, ok := a.session(accountIdx)
	if !ok {
		return ui.AccountRemoveErrorMsg{Index: accountIdx, Err: fmt.Errorf("account is still connecting")}
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
	if err := purgeSelf(s.omemoMgrV2, client.FetchOmemoDeviceList, client.PublishOmemoDeviceList); err != nil {
		slog.Warn("purging own device from omemo-v2 device list before removal", "jid", s.account.JID, "err", err)
	}
	if err := purgeSelf(s.omemoMgrV1, client.FetchOmemoDeviceListV1, client.PublishOmemoDeviceListV1); err != nil {
		slog.Warn("purging own device from omemo-v1 device list before removal", "jid", s.account.JID, "err", err)
	}

	if err := client.Close(); err != nil {
		slog.Warn("closing connection on account removal", "jid", s.account.JID, "err", err)
	}
	if err := config.RemoveAccount(a.cfgPath, s.account.JID); err != nil {
		return ui.AccountRemoveErrorMsg{Index: accountIdx, Err: fmt.Errorf("removing account from config: %w", err)}
	}

	return ui.AccountRemovedMsg{Index: accountIdx}
}

// MarkRetracted implements ui.MessageSender: flags a message as locally
// deleted in storage without sending anything over the network. Used when
// deleting someone else's message, since XEP-0424 retraction only applies to
// messages we sent ourselves.
func (a *adapter) MarkRetracted(accountIdx int, to, id string) error {
	s, ok := a.session(accountIdx)
	if !ok {
		return fmt.Errorf("unknown account %d", accountIdx)
	}
	_, err := s.db.MarkMessageRetracted(context.Background(), storage.MarkMessageRetractedParams{
		AccountJid: s.account.JID,
		IDAttr:     nullString(id),
		RosterJid:  nullString(to),
	})
	return err
}

// DeleteQueued implements ui.MessageSender: permanently discards a
// still-Pending message from the outbox table without ever sending it. Not
// an error if localID no longer matches anything queued - broadcasting
// evOutboxDeleted (so every attached client removes the same placeholder,
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
		broadcast(a.srv, evOutboxDeleted, ui.OutboxDeletedMsg{AccountIdx: accountIdx, To: to, LocalID: localID})
	}
	return nil
}

// SetTyping implements ui.MessageSender: sends a XEP-0085 chat state
// notification to "to" — no persistence, no encryption, it's ephemeral.
func (a *adapter) SetTyping(accountIdx int, to string, composing bool) error {
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
	return client.SendChatState(context.Background(), to, state)
}

// RenameContact implements ui.ContactRenamer: pushes name as a roster set
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

	subs := ""
	if entries := s.roster.Load(); entries != nil {
		subs = (*entries)[address].Subs
	}
	if err := s.db.UpsertRoster(context.Background(), storage.UpsertRosterParams{
		AccountJid: s.account.JID, Jid: address, Name: name, Subs: subs,
	}); err != nil {
		slog.Warn("persisting renamed roster entry", "address", address, "err", err)
	}

	updated := make(map[string]rosterEntry)
	if entries := s.roster.Load(); entries != nil {
		for k, v := range *entries {
			updated[k] = v
		}
	}
	updated[address] = rosterEntry{Name: name, Subs: subs, Presence: updated[address].Presence}
	s.roster.Store(&updated)

	return nil
}

// AddContact implements ui.ContactManager: adds address to the roster and
// sends a subscription request, then mirrors it into local storage and the
// in-memory roster cache used by rosterName.
func (a *adapter) AddContact(accountIdx int, address string) tea.Msg {
	s, ok := a.session(accountIdx)
	if !ok {
		return ui.ContactAddedMsg{AccountIdx: accountIdx, Address: address, Err: fmt.Errorf("unknown account %d", accountIdx)}
	}

	ctx := context.Background()
	client, err := s.liveClient()
	if err != nil {
		return ui.ContactAddedMsg{AccountIdx: accountIdx, Address: address, Err: err}
	}
	if err := client.AddContact(ctx, address, ""); err != nil {
		return ui.ContactAddedMsg{AccountIdx: accountIdx, Address: address, Err: err}
	}

	if err := s.db.UpsertRoster(ctx, storage.UpsertRosterParams{
		AccountJid: s.account.JID, Jid: address,
	}); err != nil {
		slog.Warn("persisting added roster entry", "address", address, "err", err)
	}
	updated := make(map[string]rosterEntry)
	if entries := s.roster.Load(); entries != nil {
		for k, v := range *entries {
			updated[k] = v
		}
	}
	updated[address] = rosterEntry{}
	s.roster.Store(&updated)

	return ui.ContactAddedMsg{AccountIdx: accountIdx, Address: address}
}

// ResubscribeContact implements ui.ContactManager: re-sends a subscription
// request for an existing contact, without touching its roster item.
func (a *adapter) ResubscribeContact(accountIdx int, address string) tea.Msg {
	s, ok := a.session(accountIdx)
	if !ok {
		return ui.ContactResubscribedMsg{AccountIdx: accountIdx, Address: address, Err: fmt.Errorf("unknown account %d", accountIdx)}
	}

	ctx := context.Background()
	client, err := s.liveClient()
	if err != nil {
		return ui.ContactResubscribedMsg{AccountIdx: accountIdx, Address: address, Err: err}
	}
	if err := client.ResubscribeContact(ctx, address); err != nil {
		return ui.ContactResubscribedMsg{AccountIdx: accountIdx, Address: address, Err: err}
	}
	return ui.ContactResubscribedMsg{AccountIdx: accountIdx, Address: address}
}

// RemoveContact implements ui.ContactManager: removes address from the
// roster and unsubscribes, then mirrors the removal into local storage and
// the in-memory roster cache.
func (a *adapter) RemoveContact(accountIdx int, address string) tea.Msg {
	s, ok := a.session(accountIdx)
	if !ok {
		return ui.ContactRemovedMsg{AccountIdx: accountIdx, Address: address, Err: fmt.Errorf("unknown account %d", accountIdx)}
	}

	ctx := context.Background()
	client, err := s.liveClient()
	if err != nil {
		return ui.ContactRemovedMsg{AccountIdx: accountIdx, Address: address, Err: err}
	}
	if err := client.RemoveContact(ctx, address); err != nil {
		return ui.ContactRemovedMsg{AccountIdx: accountIdx, Address: address, Err: err}
	}

	if err := s.db.DeleteRosterByJID(ctx, storage.DeleteRosterByJIDParams{
		AccountJid: s.account.JID, Jid: address,
	}); err != nil {
		slog.Warn("deleting roster entry", "address", address, "err", err)
	}
	if entries := s.roster.Load(); entries != nil {
		updated := make(map[string]rosterEntry, len(*entries))
		for k, v := range *entries {
			if k != address {
				updated[k] = v
			}
		}
		s.roster.Store(&updated)
	}

	return ui.ContactRemovedMsg{AccountIdx: accountIdx, Address: address}
}

// SetChatEncryption implements ui.ChatEncryptionSetter: persists the chosen
// outgoing message encryption ("omemo-v1", "omemo-v2", "gpg", or "none") for
// one chat.
func (a *adapter) SetChatEncryption(accountIdx int, peerJID, mode string) error {
	s, ok := a.session(accountIdx)
	if !ok {
		return fmt.Errorf("unknown account %d", accountIdx)
	}
	return s.db.SetChatEncryptionMode(context.Background(), storage.SetChatEncryptionModeParams{
		AccountJid: s.account.JID,
		RosterJid:  peerJID,
		Mode:       mode,
	})
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
					broadcast(a.srv, evMessageSendResolved, ui.MessageSendResolvedMsg{
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
					broadcast(a.srv, evMessageSendResolved, ui.MessageSendResolvedMsg{
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
			// not worth retrying forever.
			slog.Warn("uploading queued attachment failed", "jid", s.account.JID, "to", q.to, "path", q.filePath, "err", err)
			if delErr := s.deleteOutboxRow(ctx, q.dbID); delErr != nil {
				slog.Warn("deleting failed outbox attachment entry", "jid", s.account.JID, "to", q.to, "err", delErr)
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
		if sendErr != nil {
			slog.Warn("sending queued attachment failed", "jid", s.account.JID, "to", q.to, "path", q.filePath, "err", sendErr)
			continue
		}
		broadcast(a.srv, evIncomingMessage, ui.IncomingMessageMsg{
			AccountIdx: s.accountIdx,
			From:       q.to,
			ReplyToID:  q.opts.ReplyToID,
			Message: ui.Message{
				ID:          id,
				Author:      "me",
				Content:     body,
				SentAt:      time.Now(),
				IsMe:        true,
				Attachments: []string{url},
			},
		})
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
		return id, nil
	}

	sealedBody, encrypted := encryptForStorage(s, body)
	if _, err := s.db.InsertMessage(ctx, storage.InsertMessageParams{
		AccountJid:    s.account.JID,
		Sent:          true,
		ToAttr:        nullString(to),
		IDAttr:        nullString(id),
		Body:          sealedBody,
		Encrypted:     encrypted,
		E2eEncrypted:  e2eEncrypted,
		E2eeMethod:    nullString(e2eeMethod),
		StanzaType:    "chat",
		RosterJid:     nullString(to),
		ReplyToIDAttr: nullString(opts.ReplyToID),
		OobUrls:       joinOOBURLs(opts.OOBURLs),
	}); err != nil {
		slog.Warn("persisting sent message", "err", err)
	}
	s.trackForServerAck(a, s.accountIdx, to, id)
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
		return client.UploadFile(ctx, path, onProgress)
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
	return url, nil
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
		broadcast(a.srv, evFileTransferProgress, ui.FileTransferProgressMsg{ID: id, Label: label, Sent: sent, Total: total})
	}
}

// SendFile implements ui.FileSender. Uploading and the follow-up message send
// run as a Bubble Tea command, so the terminal remains responsive while a
// slot is discovered and the file is transferred.
func (a *adapter) SendFile(accountIdx int, to, path string, opts ui.SendOptions) tea.Msg {
	result := ui.FileSendResultMsg{AccountIdx: accountIdx, To: to, Path: path, ReplyToID: opts.ReplyToID}
	s, ok := a.session(accountIdx)
	if !ok {
		result.Err = fmt.Errorf("unknown account %d", accountIdx)
		return result
	}
	client, err := s.liveClient()
	if err != nil {
		result.Err = err
		return result
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	url, err := a.uploadFile(ctx, s, client, to, path, a.progressCallback(path, "uploading "+filepath.Base(path)))
	if err != nil {
		result.Err = err
		return result
	}

	opts.OOBURLs = []string{url}
	id, err := a.send(ctx, accountIdx, to, url, opts)
	if err != nil {
		result.Err = err
		return result
	}
	result.URL = url
	result.ID = id
	return result
}

// UploadFile implements ui.FileSender. Unlike SendFile it only uploads —
// used by startAttachedSend to upload+send each staged attachment as its own
// message. text/opts are only consulted when the account is offline: rather
// than fail (upload needs a live connection, unlike a plain text send),
// upload+send is queued as one unit and replayed together by
// adapter.flushOutbox once reconnected.
func (a *adapter) UploadFile(accountIdx int, to, path, text string, opts ui.SendOptions) tea.Msg {
	result := ui.FileUploadResultMsg{Path: path}
	s, ok := a.session(accountIdx)
	if !ok {
		result.Err = fmt.Errorf("unknown account %d", accountIdx)
		return result
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client, err := s.liveClient()
	if err != nil {
		if qErr := s.enqueueOutboxFile(ctx, to, text, path, opts); qErr != nil {
			result.Err = fmt.Errorf("account offline and queuing failed: %w", qErr)
			return result
		}
		slog.Debug("account offline; attachment queued", "jid", s.account.JID, "to", to, "path", path)
		result.Queued = true
		return result
	}

	url, err := a.uploadFile(ctx, s, client, to, path, a.progressCallback(path, "uploading "+filepath.Base(path)))
	if err != nil {
		result.Err = err
		return result
	}
	result.URL = url
	return result
}

// --- voice calls (XEP-0166/0167/0176/0353, see callsession.go) ---
//
// Blocking-in-the-RPC-handler is fine here for the same reason SetTyping is:
// each of these only builds and writes one stanza. The slow parts (ICE
// gathering, DTLS, audio) happen on the call's own goroutines and report
// back via evCallState broadcasts.

// StartCall places an outgoing voice call from accountIdx to "to".
func (a *adapter) StartCall(accountIdx int, to string) error {
	sess, ok := a.session(accountIdx)
	if !ok {
		return fmt.Errorf("unknown account %d", accountIdx)
	}
	return sess.startCall(context.Background(), a.srv, accountIdx, to)
}

// StartVideoCall places a call and starts sending our own video (camera or
// screen, see useCamera) the moment it connects.
func (a *adapter) StartVideoCall(accountIdx int, to string, useCamera bool) error {
	sess, ok := a.session(accountIdx)
	if !ok {
		return fmt.Errorf("unknown account %d", accountIdx)
	}
	return sess.startVideoCall(context.Background(), a.srv, accountIdx, to, useCamera)
}

// AnswerCall accepts the call currently ringing on accountIdx.
func (a *adapter) AnswerCall(accountIdx int) error {
	sess, ok := a.session(accountIdx)
	if !ok {
		return fmt.Errorf("unknown account %d", accountIdx)
	}
	return sess.answerCall(context.Background())
}

// HangupCall ends accountIdx's current call from our side.
func (a *adapter) HangupCall(accountIdx int) error {
	sess, ok := a.session(accountIdx)
	if !ok {
		return fmt.Errorf("unknown account %d", accountIdx)
	}
	return sess.hangupCall(context.Background())
}

// RejectCall declines the call currently ringing on accountIdx.
func (a *adapter) RejectCall(accountIdx int) error {
	sess, ok := a.session(accountIdx)
	if !ok {
		return fmt.Errorf("unknown account %d", accountIdx)
	}
	return sess.rejectCall(context.Background())
}

// MuteCall sets or clears the local mic mute on accountIdx's current call.
func (a *adapter) MuteCall(accountIdx int, muted bool) error {
	sess, ok := a.session(accountIdx)
	if !ok {
		return fmt.Errorf("unknown account %d", accountIdx)
	}
	return sess.muteCall(muted)
}

// ScreenShare starts or stops sending our own video (screen or camera, see
// useCamera) on accountIdx's current call.
func (a *adapter) ScreenShare(accountIdx int, sharing, useCamera bool) error {
	sess, ok := a.session(accountIdx)
	if !ok {
		return fmt.Errorf("unknown account %d", accountIdx)
	}
	return sess.setScreenShare(sharing, useCamera)
}

// ReopenVideo re-requests a keyframe for accountIdx's current call's
// incoming video, reopening the mpv viewer if the peer closed it by
// accident (see callSession.reopenRemoteVideo).
func (a *adapter) ReopenVideo(accountIdx int) error {
	sess, ok := a.session(accountIdx)
	if !ok {
		return fmt.Errorf("unknown account %d", accountIdx)
	}
	return sess.reopenVideo()
}
