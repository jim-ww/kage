package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/crypto/gpg"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/xmpp"
	omemolib "github.com/jim-ww/omemo-go"
)

// accountSession bundles everything needed to send/receive/persist for one
// configured account. client is swapped out on reconnect, so it's stored
// behind an atomic pointer — Send (called from the Bubble Tea event loop)
// and the reconnect supervisor (its own goroutine) touch it concurrently.
type accountSession struct {
	account    config.Account
	client     atomic.Pointer[xmpp.Client]
	tlsConfig  *tls.Config      // reused on reconnect; nil means Dial's default verified config
	db         *storage.Queries // shared across every account: one database, rows scoped by account.JID
	gpg        gpg.Encrypter
	useGPG     bool // mirrors config.UseGPG; gates gpg encrypt/decrypt on incoming/outgoing messages
	useKeyring bool // mirrors config.UseKeyring; gates whether ResolvePassword tries the OS keyring
	// omemoMgrV2/omemoMgrV1 are nil until connectAccountLive sets them up
	// (needs a dialed client for its Transport). Both run concurrently: this
	// account maintains a separate identity/device pool per OMEMO protocol
	// version, since a chat can be pinned to either "omemo-v1" (the default,
	// ui.encryptionModes) or "omemo-v2".
	omemoMgrV2 *omemolib.Manager
	omemoMgrV1 *omemolib.Manager

	// localKey is the AES-256 key message bodies are sealed under at rest
	// (crypto/localstore), derived once in main from the local storage
	// password and shared by every account.
	localKey []byte

	roster atomic.Pointer[map[string]rosterEntry] // bare JID -> cached roster entry, for display and RenameContact

	// histMu guards histPos: loadHistoryPage's "load older" keyset
	// pagination cursor per chat (roster JID), advanced each time a page is
	// fetched. Only ever touched from the Bubble Tea event loop today (via
	// adapter.LoadOlderHistory), but guarded since accountSession's other
	// mutable state is all concurrency-safe by construction.
	histMu  sync.Mutex
	histPos map[string]historyCursor
}

// rosterEntry is a contact's cached roster state, refreshed at connect time
// and kept in sync locally by RenameContact.
type rosterEntry struct {
	Name string
	Subs string
}

// liveClient returns the account's client, or an error if the account is
// currently offline (never dialed, or explicitly disconnected via
// PresenceOffline) — every adapter entrypoint that talks to the network
// calls this instead of s.client.Load() directly, since that would panic
// dereferencing a nil client.
func (s *accountSession) liveClient() (*xmpp.Client, error) {
	client := s.client.Load()
	if client == nil || client.Closed() {
		return nil, fmt.Errorf("account %s is offline", s.account.JID)
	}
	return client, nil
}

// rosterName returns bareJID's roster nickname, or bareJID itself if the
// contact has none (or isn't in the roster at all).
func (s *accountSession) rosterName(bareJID string) string {
	entries := s.roster.Load()
	if entries == nil {
		return bareJID
	}
	if e, ok := (*entries)[bareJID]; ok && e.Name != "" {
		return e.Name
	}
	return bareJID
}

// connectAndSuperviseAccount loads one configured account's local
// roster/history from disk first — fast, no network — and reports it to the
// UI immediately so local chats/messages appear on screen right away. Only
// then does it dial and fetch the live roster in the background, backfill
// anything missed via XEP-0313 MAM, and fall into the normal
// event-listen/reconnect supervisor loop.
func connectAndSuperviseAccount(ctx context.Context, p *tea.Program, a *adapter, idx int, acct config.Account, queries *storage.Queries, localKey []byte) {
	slog.Debug("connectAccountLocal starting", "jid", acct.JID)
	start := time.Now()
	sess, uiAcct, err := connectAccountLocal(ctx, acct, queries, localKey)
	if err != nil {
		slog.Debug("connectAccountLocal failed", "jid", acct.JID, "elapsed", time.Since(start), "err", err)
		p.Send(ui.AccountConnectErrorMsg{Index: idx, Err: err})
		return
	}
	sess.useGPG = a.useGPG
	sess.useKeyring = a.useKeyring
	slog.Debug("connectAccountLocal done", "jid", acct.JID, "elapsed", time.Since(start), "chats", len(uiAcct.Chats))

	if uiAcct.Status == ui.PresenceOffline {
		// Configured offline: never dial at all - not even to fetch a live
		// roster - so nothing about this account touches the network until
		// AccountStatusSetter switches it back on.
		uiAcct.Connecting = false
		a.mu.Lock()
		a.sessions[idx] = sess
		a.mu.Unlock()
		p.Send(ui.AccountConnectedMsg{Index: idx, Account: uiAcct})
		return
	}
	p.Send(ui.AccountConnectedMsg{Index: idx, Account: uiAcct})

	slog.Debug("connectAccountLive starting", "jid", acct.JID)
	start = time.Now()
	newChats, newMessages, newHistoryMore, err := connectAccountLive(ctx, sess, len(uiAcct.Chats), presenceShow(uiAcct.Status))
	if err != nil {
		slog.Debug("connectAccountLive failed", "jid", acct.JID, "elapsed", time.Since(start), "err", err)
		p.Send(ui.AccountConnectErrorMsg{Index: idx, Err: err})
		return
	}
	slog.Debug("connectAccountLive done", "jid", acct.JID, "elapsed", time.Since(start), "new_chats", len(newChats))

	a.mu.Lock()
	a.sessions[idx] = sess
	a.mu.Unlock()

	p.Send(ui.AccountLiveMsg{Index: idx, NewChats: newChats, NewMessages: newMessages, NewHistoryMore: newHistoryMore})

	// Start listening (and reconnecting on drop) right away, concurrently
	// with syncArchive below - not after it. c.serve() has been reading the
	// stream since Dial regardless (SendIQ needs it to unblock), so presence/
	// messages arriving during the MAM backfill were already being queued up
	// on Client.events; running listen sequentially after syncArchive just
	// left them sitting there unprocessed (roster presence looked stuck
	// offline) until the backfill finished, sometimes tens of seconds later.
	go superviseAccount(ctx, p, idx, sess)

	slog.Debug("syncArchive starting", "jid", acct.JID)
	start = time.Now()
	p.Send(ui.HistorySyncStartedMsg{AccountIdx: idx})
	syncArchive(ctx, p, idx, sess)
	p.Send(ui.HistorySyncFinishedMsg{AccountIdx: idx})
	slog.Debug("syncArchive done", "jid", acct.JID, "elapsed", time.Since(start))
}

// connectAccountLocal loads acct's cached roster + history from the shared
// database — no network involved, so this is as fast as local SQLite reads
// and AES decrypts allow, letting local chats/messages appear instantly
// instead of waiting on connectAccountLive. The returned ui.Account has
// Connecting set; the caller clears it once connectAccountLive finishes.
func connectAccountLocal(ctx context.Context, acct config.Account, queries *storage.Queries, localKey []byte) (*accountSession, ui.Account, error) {
	sess := &accountSession{
		account:  acct,
		db:       queries,
		gpg:      gpg.Encrypter{},
		localKey: localKey,
	}

	rows, err := queries.ListRoster(ctx, acct.JID)
	if err != nil {
		return nil, ui.Account{}, fmt.Errorf("account %s: loading local roster: %w", acct.JID, err)
	}
	slog.Debug("local roster loaded", "jid", acct.JID, "contacts", len(rows))

	unreadRows, err := queries.ListChatUnread(ctx, acct.JID)
	if err != nil {
		slog.Warn("loading chat unread counts", "jid", acct.JID, "err", err)
	}
	unread := make(map[string]int, len(unreadRows))
	for _, r := range unreadRows {
		unread[r.Rosterjid] = int(r.Count)
	}

	chats := make([]list.Item, 0, len(rows))
	messages := make(map[int][]ui.Message, len(rows))
	historyMore := make(map[int]bool, len(rows))
	entries := make(map[string]rosterEntry, len(rows))
	for i, r := range rows {
		name := r.Name
		if name == "" {
			name = r.Jid
		}
		entries[r.Jid] = rosterEntry{Name: r.Name, Subs: r.Subs}
		mode, err := queries.GetChatEncryptionMode(ctx, storage.GetChatEncryptionModeParams{AccountJid: acct.JID, RosterJid: r.Jid})
		if err != nil {
			mode = "omemo-v1"
		}
		histStart := time.Now()
		hist, hasMore := loadHistoryPage(ctx, sess, r.Jid, name)
		slog.Debug("loadHistoryPage done", "jid", acct.JID, "peer", r.Jid, "elapsed", time.Since(histStart), "messages", len(hist), "more", hasMore)
		chat := ui.Chat{Name: name, Address: r.Jid, EncryptionMode: mode, Unread: unread[r.Jid]}
		if len(hist) > 0 {
			messages[i] = hist
			chat.LastMessage = hist[len(hist)-1].Content
		}
		chats = append(chats, chat)
		historyMore[i] = hasMore
	}
	sess.roster.Store(&entries)

	return sess, ui.Account{Name: acct.JID, Alias: acct.Alias, Chats: chats, Messages: messages, HistoryMore: historyMore, Connecting: true, Status: accountStatus(acct.Status)}, nil
}

// connectAccountLive dials sess's account, publishes our GPG key, and fetches
// the live roster, merging it into sess's already-loaded (by
// connectAccountLocal) roster cache. Existing contacts' chat/history entries
// are left completely untouched — they're already showing, and anything
// missed while offline is backfilled separately via syncArchive — only
// contacts the local snapshot didn't know about (added from another device)
// get a fresh history load here. The returned chats/messages are new
// entries only, with message indices relative to a Chats slice that starts
// right after existingChatCount, so the caller can append rather than
// replace what's already displayed.
func connectAccountLive(ctx context.Context, sess *accountSession, existingChatCount int, show string) ([]list.Item, map[int][]ui.Message, map[int]bool, error) {
	slog.Debug("resolving password", "jid", sess.account.JID)
	start := time.Now()
	password, err := sess.account.ResolvePassword(sess.useKeyring)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("account %s: %w", sess.account.JID, err)
	}
	slog.Debug("password resolved", "jid", sess.account.JID, "elapsed", time.Since(start))

	var tlsConfig *tls.Config // nil: Dial's default verified config; future config.toml option could set a custom RootCAs pool here
	slog.Debug("dialing", "jid", sess.account.JID)
	start = time.Now()
	client, err := xmpp.Dial(ctx, sess.account.JID, password, tlsConfig)
	if err != nil {
		slog.Debug("dial failed", "jid", sess.account.JID, "elapsed", time.Since(start), "err", err)
		return nil, nil, nil, fmt.Errorf("account %s: %w", sess.account.JID, err)
	}
	slog.Debug("dialed", "jid", sess.account.JID, "elapsed", time.Since(start))
	sess.tlsConfig = tlsConfig
	sess.client.Store(client)

	if show != "" {
		if err := client.SetPresence(ctx, show); err != nil {
			slog.Warn("setting initial presence", "show", show, "jid", sess.account.JID, "err", err)
		}
	}

	if sess.useGPG && sess.account.GPGKeyID != "" {
		slog.Debug("publishOwnGPGKey starting", "jid", sess.account.JID)
		start = time.Now()
		publishOwnGPGKey(ctx, sess)
		slog.Debug("publishOwnGPGKey done", "jid", sess.account.JID, "elapsed", time.Since(start))
	}

	slog.Debug("setupOmemo starting", "jid", sess.account.JID)
	start = time.Now()
	setupOmemo(ctx, sess)
	slog.Debug("setupOmemo done", "jid", sess.account.JID, "elapsed", time.Since(start))

	slog.Debug("fetching live roster", "jid", sess.account.JID)
	start = time.Now()
	contacts, err := client.Roster(ctx)
	if err != nil {
		slog.Debug("roster fetch failed", "jid", sess.account.JID, "elapsed", time.Since(start), "err", err)
		client.Close()
		return nil, nil, nil, fmt.Errorf("account %s: fetching roster: %w", sess.account.JID, err)
	}
	slog.Debug("live roster fetched", "jid", sess.account.JID, "elapsed", time.Since(start), "contacts", len(contacts))

	existing := sess.roster.Load()
	merged := make(map[string]rosterEntry, len(contacts))
	if existing != nil {
		for k, v := range *existing {
			merged[k] = v
		}
	}

	var newChats []list.Item
	newMessages := make(map[int][]ui.Message)
	newHistoryMore := make(map[int]bool)
	for _, c := range contacts {
		name := c.Name
		if name == "" {
			name = c.JID
		}
		_, known := merged[c.JID]
		merged[c.JID] = rosterEntry{Name: c.Name, Subs: c.Subscription}
		if err := sess.db.UpsertRoster(ctx, storage.UpsertRosterParams{
			AccountJid: sess.account.JID, Jid: c.JID, Name: c.Name, Subs: c.Subscription,
		}); err != nil {
			slog.Warn("persisting roster entry", "jid", c.JID, "err", err)
		}
		if known {
			continue
		}
		idx := existingChatCount + len(newChats)
		hist, hasMore := loadHistoryPage(ctx, sess, c.JID, name)
		chat := ui.Chat{Name: name, Address: c.JID}
		if len(hist) > 0 {
			newMessages[idx] = hist
			chat.LastMessage = hist[len(hist)-1].Content
		}
		newChats = append(newChats, chat)
		newHistoryMore[idx] = hasMore
	}
	sess.roster.Store(&merged)

	return newChats, newMessages, newHistoryMore, nil
}

// connectAccount runs connectAccountLocal then connectAccountLive back to
// back and merges the result into one ready-to-show ui.Account — used by
// AddAccount, where the account is brand new (no local history to show
// instantly) and the whole connect already runs off the Bubble Tea event
// loop via a tea.Cmd, so there's nothing to gain from doing it in two steps.
func connectAccount(ctx context.Context, acct config.Account, queries *storage.Queries, localKey []byte, useGPG, useKeyring bool) (*accountSession, ui.Account, error) {
	sess, uiAcct, err := connectAccountLocal(ctx, acct, queries, localKey)
	if err != nil {
		return nil, ui.Account{}, err
	}
	sess.useGPG = useGPG
	sess.useKeyring = useKeyring

	newChats, newMessages, newHistoryMore, err := connectAccountLive(ctx, sess, len(uiAcct.Chats), "")
	if err != nil {
		return nil, ui.Account{}, err
	}
	uiAcct.Status = ui.PresenceOnline

	uiAcct.Chats = append(uiAcct.Chats, newChats...)
	if uiAcct.Messages == nil {
		uiAcct.Messages = make(map[int][]ui.Message)
	}
	for idx, msgs := range newMessages {
		uiAcct.Messages[idx] = msgs
	}
	if uiAcct.HistoryMore == nil {
		uiAcct.HistoryMore = make(map[int]bool)
	}
	for idx, more := range newHistoryMore {
		uiAcct.HistoryMore[idx] = more
	}
	uiAcct.Connecting = false

	return sess, uiAcct, nil
}

// superviseAccount runs listen for s, and on an unexpected disconnect (the
// Events channel closing without Close having been called), reconnects with
// exponential backoff and resumes. Returns once the client is intentionally
// closed (app shutdown).
func superviseAccount(ctx context.Context, p *tea.Program, accountIdx int, s *accountSession) {
	for {
		listen(ctx, p, accountIdx, s)

		client := s.client.Load()
		if client.Closed() || ctx.Err() != nil {
			return
		}
		slog.Warn("account disconnected; reconnecting", "jid", s.account.JID, "err", client.Err())
		reconnectWithBackoff(ctx, s)
	}
}

// reconnectWithBackoff retries Dial with exponential backoff (capped at 60s)
// until it succeeds or ctx is done, then stores the new client on s.
func reconnectWithBackoff(ctx context.Context, s *accountSession) {
	const maxBackoff = 60 * time.Second
	backoff := time.Second

	for {
		if ctx.Err() != nil {
			return
		}

		password, err := s.account.ResolvePassword(s.useKeyring)
		if err == nil {
			var client *xmpp.Client
			client, err = xmpp.Dial(ctx, s.account.JID, password, s.tlsConfig)
			if err == nil {
				if show := presenceShow(accountStatus(s.account.Status)); show != "" {
					if err := client.SetPresence(ctx, show); err != nil {
						slog.Warn("restoring presence after reconnect", "show", show, "jid", s.account.JID, "err", err)
					}
				}
				s.client.Store(client)
				slog.Debug("account reconnected", "jid", s.account.JID)
				return
			}
		}

		slog.Warn("reconnecting failed; retrying", "jid", s.account.JID, "err", err, "backoff", backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// listen bridges one account's xmpp events into the Bubble Tea program.
// Returns when the current client's Events channel closes (client swapped
// out from under it, e.g. by a reconnect, or the client was closed).
func listen(ctx context.Context, p *tea.Program, accountIdx int, s *accountSession) {
	for ev := range s.client.Load().Events() {
		switch ev := ev.(type) {
		case xmpp.PresenceEvent:
			p.Send(ui.PresenceMsg{
				AccountIdx: accountIdx,
				From:       bareJID(ev.From),
				Presence:   mapPresence(ev),
			})
			continue
		case xmpp.SubscriptionRequestEvent:
			from := bareJID(ev.From)
			if err := s.client.Load().ApproveSubscription(ctx, from); err != nil {
				slog.Warn("approving subscription request", "from", from, "jid", s.account.JID, "err", err)
			}
			continue
		case xmpp.MessageEvent:
			handleIncomingMessage(ctx, p, accountIdx, s, ev)
		case xmpp.ChatStateEvent:
			p.Send(ui.TypingMsg{
				AccountIdx: accountIdx,
				From:       bareJID(ev.From),
				Typing:     ev.State == xmpp.ChatStateComposing,
			})
			continue
		case xmpp.MessageDeliveredEvent:
			from := bareJID(ev.From)
			if _, err := s.db.MarkMessageDelivered(ctx, storage.MarkMessageDeliveredParams{
				AccountJid: s.account.JID,
				IDAttr:     nullString(ev.ID),
				RosterJid:  nullString(from),
			}); err != nil {
				slog.Warn("persisting delivery receipt", "err", err)
			}
			p.Send(ui.MessageDeliveredMsg{
				AccountIdx: accountIdx,
				From:       from,
				MessageID:  ev.ID,
			})
			continue
		case xmpp.DeviceListChangedEvent:
			from := bareJID(ev.From)
			// A self-notification about our own just-published device list
			// (some servers omit the from attribute entirely on these,
			// others set it to our own bare JID) needs no action - we
			// already know our own device list, having just written it -
			// and an empty from can't be resolved to a peer JID at all.
			if from == "" || from == s.account.JID {
				continue
			}
			mgr := s.omemoMgrV2
			if ev.Protocol == omemolib.ProtocolV1 {
				mgr = s.omemoMgrV1
			}
			if mgr == nil {
				continue
			}
			// SyncDevices does a network round-trip (an IQ fetch) - run it
			// off to the side rather than blocking this loop, which is the
			// same serial dispatch path every other event for this account
			// (chat messages included) goes through. A slow or hanging
			// fetch here must never stall message delivery.
			go func() {
				if err := mgr.SyncDevices(ctx, from); err != nil {
					slog.Warn("resyncing omemo device list after PEP push", "protocol", ev.Protocol, "from", from, "err", err)
				}
			}()
			continue
		}
	}
}

// accountStatus maps a config.Account's Status field ("", "chat", "away",
// "xa", "dnd", or "offline") to the UI's Presence enum; "" defaults to
// online.
func accountStatus(status string) ui.Presence {
	switch status {
	case "chat":
		return ui.PresenceChat
	case "away":
		return ui.PresenceAway
	case "xa":
		return ui.PresenceXA
	case "dnd":
		return ui.PresenceDND
	case "offline":
		return ui.PresenceOffline
	default:
		return ui.PresenceOnline
	}
}

// presenceShow maps a ui.Presence to the <show/> value SetPresence should
// send: "" for online (no <show/> at all), the RFC 6121 §4.7.2.1 value name
// otherwise (offline is handled separately, by not connecting/by
// disconnecting rather than by any presence stanza).
func presenceShow(status ui.Presence) string {
	switch status {
	case ui.PresenceChat:
		return "chat"
	case ui.PresenceAway:
		return "away"
	case ui.PresenceXA:
		return "xa"
	case ui.PresenceDND:
		return "dnd"
	default:
		return ""
	}
}

// mapPresence reduces an xmpp.PresenceEvent to the UI's Presence enum.
func mapPresence(ev xmpp.PresenceEvent) ui.Presence {
	if !ev.Available {
		return ui.PresenceOffline
	}
	switch ev.Show {
	case "chat":
		return ui.PresenceChat
	case "away":
		return ui.PresenceAway
	case "xa":
		return ui.PresenceXA
	case "dnd":
		return ui.PresenceDND
	default:
		return ui.PresenceOnline
	}
}

// syncArchive backfills every roster contact's history via XEP-0313 (MAM),
// catching up on messages sent/received on another device or while this
// client wasn't running — something local storage alone can never have. Runs
// once, right after an account finishes connecting; best-effort per contact,
// since not every server (or every contact's account) offers MAM.
func syncArchive(ctx context.Context, p *tea.Program, accountIdx int, s *accountSession) {
	client := s.client.Load()

	lastArchiveID := make(map[string]string)
	if latest, err := s.db.ListLatestArchiveIDs(ctx, s.account.JID); err == nil {
		for _, row := range latest {
			if row.Archiveid.Valid && row.Rosterjid.Valid {
				lastArchiveID[row.Rosterjid.String] = row.Archiveid.String
			}
		}
	}

	entries := s.roster.Load()
	if entries == nil {
		return
	}
	slog.Debug("syncArchive: contacts to check", "jid", s.account.JID, "contacts", len(*entries))
	for peerJID := range *entries {
		start := time.Now()
		syncArchiveForContact(ctx, p, accountIdx, s, client, peerJID, lastArchiveID[peerJID])
		slog.Debug("syncArchiveForContact done", "jid", s.account.JID, "peer", peerJID, "elapsed", time.Since(start))
	}
}

// syncArchiveForContact pages through peerJID's MAM archive strictly newer
// than afterArchiveID until the server reports the page set complete,
// persisting and forwarding each message to the UI as it's fetched.
func syncArchiveForContact(ctx context.Context, p *tea.Program, accountIdx int, s *accountSession, client *xmpp.Client, peerJID, afterArchiveID string) {
	const pageSize = 50
	const maxPages = 200 // guards against a misbehaving server never reporting complete

	ownBare := bareJID(s.account.JID)
	name := s.rosterName(peerJID)

	for page := 0; page < maxPages; page++ {
		var newMsgs []ui.Message
		prevAfter := afterArchiveID
		pageCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		pageStart := time.Now()
		items, complete, err := client.FetchArchive(pageCtx, peerJID, afterArchiveID, pageSize)
		cancel()
		slog.Debug("mam page fetched", "jid", s.account.JID, "peer", peerJID, "page", page, "elapsed", time.Since(pageStart), "after", prevAfter, "items", len(items), "complete", complete, "err", err)
		if err != nil {
			// MAM isn't universally supported (by the server or by the peer's
			// own account) — not an error worth surfacing per contact.
			break
		}
		if len(items) == 0 {
			break
		}

		stop := false
		for _, am := range items {
			afterArchiveID = am.ArchiveID

			// A page re-fetched after a stale cursor (e.g. the previous run
			// crashed mid-backfill before advancing lastArchiveID) would
			// otherwise re-run OMEMO decrypt on ciphertext already stored -
			// wasteful at best, and for a message key already consumed out
			// of the double ratchet's skip buffer, not guaranteed to fail
			// cleanly the second time. Check storage before decrypting.
			if exists, err := s.db.MessageExistsByArchiveID(ctx, storage.MessageExistsByArchiveIDParams{
				AccountJid: s.account.JID,
				ArchiveID:  nullString(am.ArchiveID),
			}); err == nil && exists {
				continue
			}

			// The same message can also already be stored via the live path
			// (events.go), which has no archiveID to match against - check
			// its dedup key too, or a MAM resync re-decrypts ciphertext whose
			// ratchet key the live path already consumed (see comment above).
			if am.ID != "" {
				if exists, err := s.db.MessageExistsByIDAttr(ctx, storage.MessageExistsByIDAttrParams{
					AccountJid: s.account.JID,
					RosterJid:  nullString(peerJID),
					IDAttr:     nullString(am.ID),
				}); err == nil && exists {
					continue
				}
			}

			// Retractions/reactions/corrections backfilled via MAM (i.e. they
			// happened while offline, so the live path in handleIncomingMessage
			// never saw them) apply to a message already persisted - by this
			// same sync, an earlier one, or the live path - rather than
			// inserting a row of their own. Applied inline, immediately, same
			// as the live path, instead of batched into newMsgs/HistorySyncedMsg
			// below - the target message may already be on screen.
			if am.RetractID != "" {
				if _, err := s.db.MarkMessageRetracted(ctx, storage.MarkMessageRetractedParams{
					AccountJid: s.account.JID,
					IDAttr:     nullString(am.RetractID),
					RosterJid:  nullString(peerJID),
				}); err != nil {
					slog.Warn("persisting mam retraction", "peer", peerJID, "err", err)
				}
				p.Send(ui.MessageRetractedMsg{AccountIdx: accountIdx, From: peerJID, RetractID: am.RetractID})
				continue
			}
			if am.ReactionTargetID != "" {
				if err := replaceReactions(ctx, s, peerJID, am.ReactionTargetID, bareJID(am.From), am.Reactions); err != nil {
					slog.Warn("persisting mam reactions", "peer", peerJID, "err", err)
				}
				p.Send(ui.MessageReactionsMsg{
					AccountIdx: accountIdx,
					From:       peerJID,
					MessageID:  am.ReactionTargetID,
					Reactions:  loadReactionsForMessage(ctx, s, peerJID, am.ReactionTargetID),
				})
				continue
			}

			// Belt-and-suspenders alongside dispatchArchiveResult's own
			// filtering (xmpp/mam.go) - a plain message with neither body nor
			// an OMEMO payload has nothing to show, matching
			// handleIncomingMessage's live-path guard (events.go).
			if am.Body == "" && am.Encrypted == nil && am.EncryptedV1 == nil && am.ReplaceID == "" {
				continue
			}

			body := am.Body
			e2eEncrypted := am.Encrypted != nil || am.EncryptedV1 != nil || gpg.Looks(body)
			e2eeMethod := ""
			switch {
			case am.Encrypted != nil:
				e2eeMethod = "omemo-v2"
			case am.EncryptedV1 != nil:
				e2eeMethod = "omemo-v1"
			case gpg.Looks(body):
				e2eeMethod = "gpg"
			}
			decryptFailed := false
			if am.Encrypted != nil || am.EncryptedV1 != nil {
				var mgr *omemolib.Manager
				var enc *omemolib.EncryptedMessage
				var decodeErr error
				if am.Encrypted != nil {
					mgr = s.omemoMgrV2
					enc, decodeErr = xmpp.DecodeOmemoMessage(am.Encrypted, bareJID(am.From))
				} else {
					mgr = s.omemoMgrV1
					enc, decodeErr = xmpp.DecodeOmemoMessageV1(am.EncryptedV1, bareJID(am.From))
				}
				if mgr == nil {
					body = "[message could not be decrypted: omemo isn't ready]"
					decryptFailed = true
				} else if decodeErr != nil {
					body = "[message could not be decrypted: " + decodeErr.Error() + "]"
					decryptFailed = true
				} else if pt, err := mgr.DecryptMessage(ctx, enc); errors.Is(err, omemolib.ErrOwnDeviceKeyMissing) {
					// Not a real failure - see handleIncomingMessage's live-path
					// comment. Quietly skip instead of storing a noise row.
					slog.Debug("mam: omemo message has no key for this device, skipping", "archive_id", am.ArchiveID, "from", am.From)
					continue
				} else if err != nil {
					body = "[message could not be decrypted: " + err.Error() + "]"
					decryptFailed = true
				} else if pt == nil {
					continue // key-transport only: session established/refreshed, no content to show
				} else {
					body = string(pt)
				}
			} else if s.useGPG && gpg.Looks(body) {
				if pt, err := s.gpg.Decrypt(body, ""); err == nil {
					body = pt
				}
			}
			// MAM doesn't currently surface the <reply/> element (see
			// ArchivedMessage), so unlike handleIncomingMessage this can't
			// gate on ReplyToID - stripReplyQuote is a no-op on a body that
			// doesn't start with a quote block, so applying it unconditionally
			// is safe and still fixes attachment identification for
			// backfilled replies.
			body = stripReplyQuote(body)
			sent := bareJID(am.From) == ownBare
			sealedBody, encrypted := encryptForStorage(s, body)

			if am.ReplaceID != "" {
				if _, err := s.db.UpdateMessageBodyByID(ctx, storage.UpdateMessageBodyByIDParams{
					AccountJid:   s.account.JID,
					Body:         sealedBody,
					Encrypted:    encrypted,
					E2eEncrypted: e2eEncrypted,
					E2eeMethod:   nullString(e2eeMethod),
					IDAttr:       nullString(am.ReplaceID),
					RosterJid:    nullString(peerJID),
				}); err != nil {
					slog.Warn("persisting mam correction", "peer", peerJID, "err", err)
				}
				p.Send(ui.MessageCorrectedMsg{
					AccountIdx: accountIdx,
					From:       peerJID,
					ReplaceID:  am.ReplaceID,
					NewContent: body,
					Encrypted:  e2eEncrypted,
					EncMethod:  e2eeMethod,
				})
				continue
			}

			_, err := s.db.InsertMessage(ctx, storage.InsertMessageParams{
				AccountJid:   s.account.JID,
				Sent:         sent,
				ToAttr:       nullString(am.To),
				FromAttr:     nullString(am.From),
				IDAttr:       nullString(am.ID),
				Body:         sealedBody,
				Encrypted:    encrypted,
				E2eEncrypted: e2eEncrypted,
				E2eeMethod:   nullString(e2eeMethod),
				StanzaType:   "chat",
				Delay:        am.SentAt.Unix(),
				RosterJid:    nullString(peerJID),
				ArchiveID:    nullString(am.ArchiveID),
			})
			if err != nil {
				if strings.Contains(err.Error(), "archiveID") {
					// Already stored, and results arrive chronologically —
					// everything after it is too.
					stop = true
					break
				}
				slog.Warn("persisting mam history", "peer", peerJID, "err", err)
				continue
			}

			author := name
			if sent {
				author = "me"
			}
			newMsgs = append(newMsgs, ui.Message{
				ID:            am.ID,
				Author:        author,
				Content:       body,
				SentAt:        am.SentAt,
				IsMe:          sent,
				Encrypted:     e2eEncrypted,
				EncMethod:     e2eeMethod,
				Attachments:   attachmentURLs(body),
				DecryptFailed: decryptFailed,
			})
		}
		if len(newMsgs) > 0 {
			p.Send(ui.HistorySyncedMsg{AccountIdx: accountIdx, From: peerJID, Messages: newMsgs})
		}
		if stop || complete || afterArchiveID == prevAfter {
			break
		}
	}
}

// bareJID strips the resource part (after "/") from a full JID, matching the
// bare-JID form roster entries and stored messages are keyed by.
func bareJID(addr string) string {
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		return addr[:i]
	}
	return addr
}
