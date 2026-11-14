package main

import (
	"cmp"
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"charm.land/bubbles/v2/list"
	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/crypto/gpg"
	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/xmpp"
	omemolib "github.com/jim-ww/omemo-go"
)

// accountSession is everything needed to send, receive and persist for one
// configured account. client is swapped on reconnect while RPCs read it, so
// it's an atomic pointer.
type accountSession struct {
	account    config.Account
	accountIdx int // index into adapter.sessions; set once at connect time, needed to replay queued sends via adapter.send
	client     atomic.Pointer[xmpp.Client]
	tlsConfig  *tls.Config      // reused on reconnect; nil means Dial's default verified config
	db         *storage.Queries // shared across every account: one database, rows scoped by account.JID
	gpg        gpg.Encrypter
	useGPG     bool // mirrors config.UseGPG; gates gpg encrypt/decrypt on incoming/outgoing messages
	useKeyring bool // mirrors config.UseKeyring; gates whether ResolvePassword tries the OS keyring
	// omemoMgrV2/omemoMgrV1 are nil until setupOmemo builds them on a dialed
	// client. Each protocol has its own identity and device pool.
	//
	// Atomic because reconnects rebuild them while the event loop, MAM
	// backfill and sends read them. Read through omemoV1/omemoV2, once per
	// operation: never re-read halfway through an encrypt.
	omemoMgrV2 atomic.Pointer[omemolib.Manager]
	omemoMgrV1 atomic.Pointer[omemolib.Manager]

	// omemoReady is closed once setupOmemo first finishes, so an OMEMO message
	// arriving before that can wait instead of being undecryptable (see
	// waitOmemoReady). nil skips waiting.
	omemoReady     chan struct{}
	omemoReadyOnce sync.Once

	// omemoWaitGaveUp latches once waitOmemoReady timed out, so an account
	// whose OMEMO setup never succeeds stalls its (sequential) event loop
	// once, not on every message.
	omemoWaitGaveUp atomic.Bool

	// peerDeviceSyncedAt is when each protocol+peer's OMEMO device list was
	// last re-fetched on a send, so not every message waits on a PEP IQ.
	peerDeviceSyncedAt map[string]time.Time
	peerDeviceSyncMu   sync.Mutex

	// localKey seals message bodies at rest (crypto/localstore); shared by
	// every account.
	localKey []byte

	roster atomic.Pointer[map[string]rosterEntry] // bare JID -> cached roster entry, for display and RenameContact

	// rosterMu serializes roster's copy-modify-store cycles (see
	// mutateRoster): presence, device names and avatars update it from
	// separate goroutines, and overlapping cycles would drop updates.
	rosterMu sync.Mutex

	// omemoMu serializes OMEMO decrypt+persist between the live path and MAM
	// backfill: decrypting one message twice concurrently consumes its
	// prekey or cached message key in the first, failing the second.
	omemoMu sync.Mutex

	// callMu guards call, the account's one call in progress (callsession.go).
	callMu sync.Mutex
	call   *callSession

	// ackMu guards ackPending/ackTimer, see trackForServerAck.
	ackMu      sync.Mutex
	ackPending []pendingAck
	ackTimer   *time.Timer

	// archiveSyncing keeps to one backfill at a time, see triggerArchiveSync.
	archiveSyncing atomic.Bool
}

// triggerArchiveSync runs syncArchive for s unless one is already running,
// which covers what this caller wanted too: MAM cursors only move forward.
func triggerArchiveSync(ctx context.Context, srv *ipc.Server, accountIdx int, s *accountSession) {
	if !s.archiveSyncing.CompareAndSwap(false, true) {
		return
	}
	defer s.archiveSyncing.Store(false)

	broadcast(srv, ui.HistorySyncStartedMsg{AccountIdx: accountIdx})
	syncArchive(ctx, srv, accountIdx, s)
	broadcast(srv, ui.HistorySyncFinishedMsg{AccountIdx: accountIdx})
}

type pendingAck struct {
	to, id string
}

// ackDebounce is how long trackForServerAck waits after the last send before
// pinging, so a burst of messages shares one ping.
const ackDebounce = 500 * time.Millisecond

// trackForServerAck queues a sent message to be confirmed by a debounced
// XEP-0199 ping: a send without error only means it reached the local
// socket buffer, not the server (see Message.ServerAcked).
func (s *accountSession) trackForServerAck(a *adapter, accountIdx int, to, id string) {
	s.ackMu.Lock()
	defer s.ackMu.Unlock()
	s.ackPending = append(s.ackPending, pendingAck{to: to, id: id})
	if s.ackTimer != nil {
		s.ackTimer.Stop()
	}
	s.ackTimer = time.AfterFunc(ackDebounce, func() {
		s.confirmPendingAcks(context.Background(), a.srv, accountIdx)
	})
}

// confirmPendingAcks pings the server once and marks every queued message
// ServerAcked if it answers. Runs on its own goroutine (time.AfterFunc), so
// it recovers its own panics.
func (s *accountSession) confirmPendingAcks(ctx context.Context, srv *ipc.Server, accountIdx int) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic confirming server acks", "jid", s.account.JID, "panic", r, "stack", string(debug.Stack()))
		}
	}()

	s.ackMu.Lock()
	pending := s.ackPending
	s.ackPending = nil
	s.ackTimer = nil
	s.ackMu.Unlock()
	if len(pending) == 0 {
		return
	}

	client, err := s.liveClient()
	if err != nil {
		return // offline: the batch stays unacked rather than guessed
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := client.Ping(ctx); err != nil {
		// No answer: likely a half-open connection, where writes still
		// succeed locally. Mark the batch failed, and kill the connection so
		// superviseAccount reconnects - a half-open one may never error on
		// its own.
		slog.Warn("confirming server ack failed; marking batch failed and forcing reconnect", "jid", s.account.JID, "batch", len(pending), "err", err)
		// A fresh context: ctx's deadline likely just expired in Ping.
		persistCtx, persistCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer persistCancel()
		for _, p := range pending {
			if _, err := s.db.MarkMessageSendFailed(persistCtx, storage.MarkMessageSendFailedParams{
				AccountJid: s.account.JID,
				IDAttr:     nullString(p.id),
				RosterJid:  nullString(p.to),
			}); err != nil {
				slog.Warn("persisting send failure", "jid", s.account.JID, "to", p.to, "err", err)
			}
			broadcast(srv, ui.MessageSendFailedMsg{
				AccountIdx: accountIdx,
				To:         p.to,
				MessageID:  p.id,
			})
		}
		client.Kill()
		return
	}

	persistCtx, persistCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer persistCancel()
	for _, p := range pending {
		if _, err := s.db.MarkMessageServerAcked(persistCtx, storage.MarkMessageServerAckedParams{
			AccountJid: s.account.JID,
			IDAttr:     nullString(p.id),
			RosterJid:  nullString(p.to),
		}); err != nil {
			slog.Warn("persisting server ack", "jid", s.account.JID, "to", p.to, "err", err)
			continue
		}
		broadcast(srv, ui.MessageServerAckedMsg{
			AccountIdx: accountIdx,
			To:         p.to,
			MessageID:  p.id,
		})
	}
}

// queuedSend is one outbox row for adapter.flushOutbox to replay. filePath
// is set for a staged attachment (uploaded, then sent with body as text).
type queuedSend struct {
	dbID     int64 // storage.Outbox.ID
	to, body string
	opts     ui.SendOptions
	filePath string
}

// enqueueOutbox stores a send for adapter.flushOutbox to replay once online.
// A reaction, retraction or correction of a message that was itself still
// queued has no target ID yet - see adapter.send.
func (s *accountSession) enqueueOutbox(ctx context.Context, to, body string, opts ui.SendOptions) error {
	return s.enqueueOutboxRow(ctx, to, body, opts, "")
}

// enqueueOutboxFile is enqueueOutbox's counterpart for a staged attachment's
// upload+send.
func (s *accountSession) enqueueOutboxFile(ctx context.Context, to, text, path string, opts ui.SendOptions) error {
	return s.enqueueOutboxRow(ctx, to, text, opts, path)
}

func (s *accountSession) enqueueOutboxRow(ctx context.Context, to, body string, opts ui.SendOptions, filePath string) error {
	_, err := s.insertOutboxEntry(ctx, to, body, opts, filePath, false, "")
	return err
}

// markOutboxFailed stores a real send failure as a failed outbox row, so a
// restarted TUI still shows the message as failed. Always a new row: a
// replayed one was already deleted before its attempt (see flushOutbox).
func (s *accountSession) markOutboxFailed(ctx context.Context, to, body string, opts ui.SendOptions, errText string) error {
	_, err := s.insertOutboxEntry(ctx, to, body, opts, "", true, errText)
	return err
}

func (s *accountSession) insertOutboxEntry(ctx context.Context, to, body string, opts ui.SendOptions, filePath string, failed bool, errText string) (storage.Outbox, error) {
	sealedBody, encrypted := encryptForStorage(s, body)
	return s.db.InsertOutboxEntry(ctx, storage.InsertOutboxEntryParams{
		AccountJid:       s.account.JID,
		LocalID:          opts.LocalID,
		ToAttr:           to,
		Body:             sealedBody.String,
		Encrypted:        encrypted,
		FilePath:         nullString(filePath),
		ReplaceID:        nullString(opts.ReplaceID),
		ReplyToID:        nullString(opts.ReplyToID),
		QuotedAuthor:     nullString(opts.QuotedAuthor),
		QuotedBody:       nullString(opts.QuotedBody),
		RetractID:        nullString(opts.RetractID),
		ReactionTargetID: nullString(opts.ReactionTargetID),
		Reactions:        joinOOBURLs(opts.Reactions),
		OobUrls:          joinOOBURLs(opts.OOBURLs),
		Failed:           failed,
		ErrorText:        nullString(errText),
	})
}

// pendingOutboxMessagesByPeer is every queued or failed new message and
// attachment in the outbox, by recipient, oldest first, to show at the end
// of their chat. Reactions, retractions and corrections are skipped: they
// change a message already shown.
func pendingOutboxMessagesByPeer(ctx context.Context, queries *storage.Queries, acct config.Account, localKey []byte) (map[string][]ui.Message, error) {
	rows, err := queries.ListOutboxByAccount(ctx, acct.JID)
	if err != nil {
		return nil, err
	}
	byPeer := make(map[string][]ui.Message)
	for _, r := range rows {
		if r.Replaceid.Valid || r.Retractid.Valid || r.Reactiontargetid.Valid {
			continue
		}
		byPeer[r.Toattr] = append(byPeer[r.Toattr], outboxRowToMessage(r, localKey))
	}
	return byPeer, nil
}

// loadOutbox is every not-failed outbox send, oldest first, for flushOutbox
// - which must never resend one the user already saw fail.
func (s *accountSession) loadOutbox(ctx context.Context) ([]queuedSend, error) {
	rows, err := s.db.ListPendingOutboxByAccount(ctx, s.account.JID)
	if err != nil {
		return nil, err
	}
	queued := make([]queuedSend, len(rows))
	for i, r := range rows {
		queued[i] = queuedSend{
			dbID:     r.ID,
			to:       r.Toattr,
			body:     decryptOutboxBody(r, s.localKey),
			filePath: r.Filepath.String,
			opts: ui.SendOptions{
				LocalID:          r.Localid,
				ReplaceID:        r.Replaceid.String,
				ReplyToID:        r.Replytoid.String,
				QuotedAuthor:     r.Quotedauthor.String,
				QuotedBody:       r.Quotedbody.String,
				RetractID:        r.Retractid.String,
				ReactionTargetID: r.Reactiontargetid.String,
				Reactions:        splitOOBURLs(r.Reactions),
				OOBURLs:          splitOOBURLs(r.Ooburls),
			},
		}
	}
	return queued, nil
}

func (s *accountSession) deleteOutboxRow(ctx context.Context, dbID int64) error {
	return s.db.DeleteOutboxEntry(ctx, dbID)
}

// deleteOutboxByLocalID discards a queued send unsent. found is false if it's
// gone already, e.g. flushOutbox sent it meanwhile.
func (s *accountSession) deleteOutboxByLocalID(ctx context.Context, localID string) (to string, found bool, err error) {
	to, err = s.db.DeleteOutboxEntryByLocalID(ctx, storage.DeleteOutboxEntryByLocalIDParams{
		AccountJid: s.account.JID,
		LocalID:    localID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return to, err == nil, err
}

// rosterEntry is a contact's cached roster state.
type rosterEntry struct {
	Name      string
	Subs      string
	Ask       bool                  // a subscription request we sent is still pending
	Presence  ui.Presence           // last known live presence; zero value (PresenceOffline) until a PresenceEvent arrives
	Resources []ui.ResourcePresence // currently online devices (full-JID resources), sorted by resource name
}

// setRosterPresence records bareJID's live presence in the roster cache, so
// a TUI attaching later gets it from listAccounts. resource ("" if none) is
// folded into Resources.
func (s *accountSession) setRosterPresence(bareJID, resource string, presence ui.Presence) {
	s.mutateRoster(func(entries map[string]rosterEntry) {
		e := entries[bareJID]
		was := e.Presence
		e.Resources = ui.WithResource(e.Resources, resource, presence)
		if resource == "" {
			e.Presence = presence
		} else {
			e.Presence = ui.AggregatePresence(e.Resources)
		}
		// Logs changes of the aggregate, which the chat list shows, not of
		// each stanza.
		if was != e.Presence {
			slog.Debug("contact presence changed", "jid", s.account.JID, "contact", bareJID,
				"resource", resource, "resource_presence", presence, "from", was, "to", e.Presence)
		}
		entries[bareJID] = e
	})
}

// resolveDeviceName looks up a contact resource's client name (XEP-0030
// disco#info), caches and broadcasts it. Runs on its own goroutine: many
// clients answer slowly or never, and the UI shows the resource until then.
func (s *accountSession) resolveDeviceName(ctx context.Context, srv *ipc.Server, accountIdx int, bareJID, resource string) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic resolving device name", "jid", s.account.JID, "peer", bareJID, "resource", resource, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client := s.client.Load()
	if client == nil {
		return
	}
	name, err := client.DeviceName(ctx, bareJID+"/"+resource)
	if err != nil || name == "" {
		return
	}

	s.mutateRoster(func(entries map[string]rosterEntry) {
		e := entries[bareJID]
		// Copied: concurrent readers share the old slice.
		e.Resources = slices.Clone(e.Resources)
		for i := range e.Resources {
			if e.Resources[i].Resource == resource {
				e.Resources[i].Name = name
			}
		}
		entries[bareJID] = e
	})

	broadcast(srv, ui.DeviceNameMsg{
		AccountIdx: accountIdx,
		From:       bareJID,
		Resource:   resource,
		Name:       name,
	})
}

func derefRoster(m *map[string]rosterEntry) map[string]rosterEntry {
	if m == nil {
		return nil
	}
	return *m
}

// liveClient is the account's client, or an error while it's offline.
func (s *accountSession) liveClient() (*xmpp.Client, error) {
	client := s.client.Load()
	if client == nil || client.Closed() {
		return nil, fmt.Errorf("account %s is offline", s.account.JID)
	}
	return client, nil
}

// rosterName is bareJID's roster name, or bareJID if it has none.
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

// inRoster reports whether bareJID has a roster entry, i.e. a chat row.
func (s *accountSession) inRoster(bareJID string) bool {
	entries := s.roster.Load()
	if entries == nil {
		return false
	}
	_, ok := (*entries)[bareJID]
	return ok
}

// ownResources is this account's other online devices' resources, known
// from their presence like a contact's (RFC 6121 §4.4.2). Lets
// rejectAndNotifyOwnDevices address each one: not every server fans a
// bare-JID message out to all of them.
func (s *accountSession) ownResources() []string {
	entries := s.roster.Load()
	if entries == nil {
		return nil
	}
	e, ok := (*entries)[s.account.JID]
	if !ok {
		return nil
	}
	resources := make([]string, 0, len(e.Resources))
	for _, r := range e.Resources {
		if r.Presence != ui.PresenceOffline {
			resources = append(resources, r.Resource)
		}
	}
	return resources
}

// connectAndSuperviseAccount reports an account's stored chats first, then
// connects, backfills through MAM (XEP-0313) and supervises the connection.
func connectAndSuperviseAccount(ctx context.Context, srv *ipc.Server, a *adapter, idx int, acct config.Account, queries *storage.Queries, localKey []byte) {
	slog.Debug("connectAccountLocal starting", "jid", acct.JID)
	start := time.Now()
	sess, uiAcct, err := connectAccountLocal(ctx, acct, queries, localKey)
	if err != nil {
		slog.Debug("connectAccountLocal failed", "jid", acct.JID, "elapsed", time.Since(start), "err", err)
		broadcast(srv, ui.AccountConnectErrorMsg{Index: idx, Err: wireError(err.Error())})
		return
	}
	sess.accountIdx = idx
	sess.useGPG = a.useGPG
	sess.useKeyring = a.useKeyring
	slog.Debug("connectAccountLocal done", "jid", acct.JID, "elapsed", time.Since(start), "chats", len(uiAcct.Chats))

	// Registered before dialing, which can take long offline: a send
	// meanwhile must find the session and queue.
	a.mu.Lock()
	a.sessions[idx] = sess
	a.mu.Unlock()

	if uiAcct.Status == ui.PresenceOffline {
		uiAcct.Connecting = false
		broadcast(srv, ui.AccountConnectedMsg{Index: idx, Account: uiAcct})
		return
	}
	broadcast(srv, ui.AccountConnectedMsg{Index: idx, Account: uiAcct})

	slog.Debug("connectAccountLive starting", "jid", acct.JID)
	start = time.Now()
	newChats, newMessages, newHistoryMore, err := connectAccountLive(ctx, sess, len(uiAcct.Chats), presenceShow(uiAcct.Status))
	if err != nil {
		slog.Debug("connectAccountLive failed", "jid", acct.JID, "elapsed", time.Since(start), "err", err)
		broadcast(srv, ui.AccountConnectErrorMsg{Index: idx, Err: wireError(err.Error())})
		go retryInitialConnect(ctx, srv, a, idx, sess, len(uiAcct.Chats), presenceShow(uiAcct.Status))
		return
	}
	slog.Debug("connectAccountLive done", "jid", acct.JID, "elapsed", time.Since(start), "new_chats", len(newChats))

	// Sends queued by a previous run.
	a.flushOutbox(ctx, sess)

	broadcast(srv, ui.AccountLiveMsg{Index: idx, NewChats: newChats, NewMessages: newMessages, NewHistoryMore: newHistoryMore, SupportsInvisible: invisibleSupported(sess)})

	// Alongside syncArchive, not after it: events arriving during a long
	// backfill would wait unprocessed.
	go superviseAccount(ctx, srv, a, idx, sess)

	slog.Debug("syncArchive starting", "jid", acct.JID)
	start = time.Now()
	broadcast(srv, ui.HistorySyncStartedMsg{AccountIdx: idx})
	syncArchive(ctx, srv, idx, sess)
	broadcast(srv, ui.HistorySyncFinishedMsg{AccountIdx: idx})
	slog.Debug("syncArchive done", "jid", acct.JID, "elapsed", time.Since(start))
}

// retryInitialConnect retries connectAccountLive with backoff after the first
// attempt failed. Unlike reconnectWithBackoff it repeats the whole first-time
// setup, then carries on as connectAndSuperviseAccount does.
func retryInitialConnect(ctx context.Context, srv *ipc.Server, a *adapter, idx int, sess *accountSession, existingChatCount int, show string) {
	const maxBackoff = 60 * time.Second
	backoff := time.Second
	for {
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}

		newChats, newMessages, newHistoryMore, err := connectAccountLive(ctx, sess, existingChatCount, show)
		if err != nil {
			slog.Warn("retrying initial connect failed", "jid", sess.account.JID, "err", err, "backoff", backoff)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		slog.Debug("initial connect succeeded on retry", "jid", sess.account.JID)
		broadcast(srv, ui.AccountLiveMsg{Index: idx, NewChats: newChats, NewMessages: newMessages, NewHistoryMore: newHistoryMore, SupportsInvisible: invisibleSupported(sess)})
		go superviseAccount(ctx, srv, a, idx, sess)
		a.flushOutbox(ctx, sess)

		broadcast(srv, ui.HistorySyncStartedMsg{AccountIdx: idx})
		syncArchive(ctx, srv, idx, sess)
		broadcast(srv, ui.HistorySyncFinishedMsg{AccountIdx: idx})
		return
	}
}

// connectAccountLocal loads acct's stored roster and history, no network.
// The returned ui.Account has Connecting set.
func connectAccountLocal(ctx context.Context, acct config.Account, queries *storage.Queries, localKey []byte) (*accountSession, ui.Account, error) {
	sess := &accountSession{
		account:    acct,
		db:         queries,
		gpg:        gpg.Encrypter{},
		localKey:   localKey,
		omemoReady: make(chan struct{}),
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
	// Only chats in the roster count towards the badge: a count without a
	// chat row could never be cleared.
	badge := make(map[string]int, len(unread))
	for _, r := range rows {
		if n := unread[r.Jid]; n > 0 {
			badge[r.Jid] = n
		}
	}
	unreadBadge.seedAccount(acct.JID, badge)

	draftRows, err := queries.ListChatDrafts(ctx, acct.JID)
	if err != nil {
		slog.Warn("loading chat drafts", "jid", acct.JID, "err", err)
	}
	drafts := make(map[string]string, len(draftRows))
	for _, r := range draftRows {
		drafts[r.Rosterjid] = loadDraft(ctx, queries, acct.JID, r, localKey)
	}

	pendingByPeer, err := pendingOutboxMessagesByPeer(ctx, queries, acct, localKey)
	if err != nil {
		slog.Warn("loading pending outbox messages", "jid", acct.JID, "err", err)
	}

	chats := make([]list.Item, 0, len(rows))
	messages := make(map[int][]ui.Message, len(rows))
	historyMore := make(map[int]bool, len(rows))
	entries := make(map[string]rosterEntry, len(rows))
	for i, r := range rows {
		entries[r.Jid] = rosterEntry{Name: r.Name, Subs: r.Subs}
		chat, hist, hasMore := sess.newChat(ctx, r.Jid, r.Name, pendingByPeer[r.Jid])
		chat.Unread, chat.Draft, chat.Subscription = unread[r.Jid], drafts[r.Jid], r.Subs
		if len(hist) > 0 {
			messages[i] = hist
		}
		chats = append(chats, chat)
		historyMore[i] = hasMore
	}
	// Stored directly: nothing else has sess yet.
	sess.roster.Store(&entries)

	return sess, ui.Account{Name: acct.JID, Alias: acct.Alias, Chats: chats, Messages: messages, HistoryMore: historyMore, Connecting: true, Status: accountStatus(acct.Status)}, nil
}

// connectAccountLive dials sess's account, sets up GPG and OMEMO, and merges
// the live roster into sess's. It returns chats only for contacts new to
// it, indexed from existingChatCount, for the caller to append.
func connectAccountLive(ctx context.Context, sess *accountSession, existingChatCount int, show string) ([]list.Item, map[int][]ui.Message, map[int]bool, error) {
	slog.Debug("resolving password", "jid", sess.account.JID)
	start := time.Now()
	password, err := sess.account.ResolvePassword(sess.useKeyring)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("account %s: %w", sess.account.JID, err)
	}
	slog.Debug("password resolved", "jid", sess.account.JID, "elapsed", time.Since(start))

	var tlsConfig *tls.Config // nil: Dial's default verified config
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

	// GPG, OMEMO and the roster fetch are independent, so run concurrently.
	var wg sync.WaitGroup
	if sess.useGPG && sess.account.GPGKeyID != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slog.Debug("publishOwnGPGKey starting", "jid", sess.account.JID)
			start := time.Now()
			publishOwnGPGKey(ctx, sess)
			slog.Debug("publishOwnGPGKey done", "jid", sess.account.JID, "elapsed", time.Since(start))
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		slog.Debug("setupOmemo starting", "jid", sess.account.JID)
		start := time.Now()
		setupOmemo(ctx, sess)
		slog.Debug("setupOmemo done", "jid", sess.account.JID, "elapsed", time.Since(start))
	}()

	// Warmed here so listAccounts, which a starting TUI waits on, doesn't
	// make this round trip.
	wg.Add(1)
	go func() {
		defer wg.Done()
		discoCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		start := time.Now()
		supported := client.InvisibleSupported(discoCtx)
		slog.Debug("invisible-presence support resolved", "jid", sess.account.JID, "supported", supported, "elapsed", time.Since(start))
	}()

	slog.Debug("fetching live roster", "jid", sess.account.JID)
	start = time.Now()
	contacts, err := client.Roster(ctx)
	if err != nil {
		slog.Debug("roster fetch failed", "jid", sess.account.JID, "elapsed", time.Since(start), "err", err)
		wg.Wait()
		client.Close()
		return nil, nil, nil, fmt.Errorf("account %s: fetching roster: %w", sess.account.JID, err)
	}
	slog.Debug("live roster fetched", "jid", sess.account.JID, "elapsed", time.Since(start), "contacts", len(contacts))
	wg.Wait()

	// A pre-fetch snapshot, used only to decide which contacts are new to
	// us (and so need a chat row built below). The authoritative merge
	// happens in one shot further down, under rosterMu.
	snapshot := derefRoster(sess.roster.Load())

	// Loaded once up front rather than per-contact: a chatDraft row can exist
	// for a JID even when it's not yet "known" to the roster cache below
	// (e.g. the contact was removed and re-added while a draft sat unsent).
	draftRows, err := sess.db.ListChatDrafts(ctx, sess.account.JID)
	if err != nil {
		slog.Warn("loading chat drafts", "jid", sess.account.JID, "err", err)
	}
	drafts := make(map[string]string, len(draftRows))
	for _, r := range draftRows {
		drafts[r.Rosterjid] = loadDraft(ctx, sess.db, sess.account.JID, r, sess.localKey)
	}

	var newChats []list.Item
	newChatIdx := make(map[string]int)
	newMessages := make(map[int][]ui.Message)
	newHistoryMore := make(map[int]bool)
	for _, c := range contacts {
		prior, known := snapshot[c.JID]
		if err := sess.db.UpsertRoster(ctx, storage.UpsertRosterParams{
			AccountJid: sess.account.JID, Jid: c.JID, Name: c.Name, Subs: c.Subscription,
		}); err != nil {
			slog.Warn("persisting roster entry", "jid", c.JID, "err", err)
		}
		if known {
			continue
		}
		idx := existingChatCount + len(newChats)
		chat, hist, hasMore := sess.newChat(ctx, c.JID, c.Name, nil)
		chat.Draft, chat.Presence, chat.Resources = drafts[c.JID], prior.Presence, prior.Resources
		chat.Subscription, chat.SubscriptionPending = c.Subscription, c.Ask
		if len(hist) > 0 {
			newMessages[idx] = hist
		}
		newChatIdx[c.JID] = len(newChats)
		newChats = append(newChats, chat)
		newHistoryMore[idx] = hasMore
	}
	// Presence may have arrived during the fetch, so only the fields the
	// roster is authoritative for are written onto the current entries.
	merged := sess.mutateRoster(func(entries map[string]rosterEntry) {
		for _, c := range contacts {
			e := entries[c.JID]
			e.Name, e.Subs, e.Ask = c.Name, c.Subscription, c.Ask
			entries[c.JID] = e
		}
	})
	// Chats built above captured presence from the pre-fetch snapshot; give
	// them whatever the merge settled on.
	for jid, idx := range newChatIdx {
		entry, ok := merged[jid]
		if !ok {
			continue
		}
		chat := newChats[idx].(ui.Chat)
		if chat.Presence == entry.Presence && slices.Equal(chat.Resources, entry.Resources) {
			continue
		}
		chat.Presence, chat.Resources = entry.Presence, entry.Resources
		newChats[idx] = chat
	}

	resyncPeerDeviceLists(ctx, sess, merged)
	// As refreshRoster does on reconnect.
	healLopsidedSubscriptions(ctx, sess, contacts, client.ResubscribeContact)
	probeRosterPresence(ctx, client, merged)

	return newChats, newMessages, newHistoryMore, nil
}

// probeRosterPresence asks for every contact's presence rather than relying
// on the server's initial burst (see Client.ProbePresence). Best-effort.
func probeRosterPresence(ctx context.Context, client *xmpp.Client, roster map[string]rosterEntry) {
	for peerJID := range roster {
		if err := client.ProbePresence(ctx, peerJID); err != nil {
			slog.Debug("probing presence on connect", "peer", peerJID, "err", err)
		}
	}
}

// resyncPeerDeviceLists re-fetches every contact's OMEMO device lists on
// connect. The manager only refreshes an empty cached list by itself and
// otherwise relies on PEP pushes, which can be missed while offline.
// Best-effort.
func resyncPeerDeviceLists(ctx context.Context, s *accountSession, roster map[string]rosterEntry) {
	// Read once: a reconnect can swap them mid-loop.
	v2, v1 := s.omemoV2(), s.omemoV1()
	for peerJID := range roster {
		if v2 != nil {
			if err := v2.SyncDevices(ctx, peerJID); err != nil {
				slog.Debug("resyncing omemo-v2 device list on connect", "peer", peerJID, "jid", s.account.JID, "err", err)
			}
		}
		if v1 != nil {
			if err := v1.SyncDevices(ctx, peerJID); err != nil {
				slog.Debug("resyncing omemo-v1 device list on connect failed", "peer", peerJID, "jid", s.account.JID, "err", err)
			} else {
				slog.Debug("resynced omemo-v1 device list on connect", "peer", peerJID, "jid", s.account.JID)
			}
		}
	}
}

// connectAccount is connectAccountLocal and connectAccountLive in one, for a
// newly added account, which has no stored history to show first.
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

// superviseAccount runs listen for s, reconnecting after an unexpected
// disconnect, until the client is closed on purpose.
func superviseAccount(ctx context.Context, srv *ipc.Server, a *adapter, accountIdx int, s *accountSession) {
	go keepAlive(ctx, s, keepAliveInterval)

	for {
		listen(ctx, srv, accountIdx, s)

		client := s.client.Load()
		if client.Closed() || ctx.Err() != nil {
			return
		}
		slog.Warn("account disconnected; reconnecting", "jid", s.account.JID, "err", client.Err())
		reconnectWithBackoff(ctx, a, s)
		if ctx.Err() != nil {
			return
		}

		// What was sent while disconnected is only in the archive.
		triggerArchiveSync(ctx, srv, accountIdx, s)
	}
}

const (
	// keepAliveInterval is how often each account pings its server: under
	// the idle timeout of NATs on the path.
	keepAliveInterval = 60 * time.Second

	// keepAliveTimeout is generous: a false positive costs a reconnect and a
	// MAM resync.
	keepAliveTimeout = 20 * time.Second
)

// keepAlive pings s's server on a timer, killing the connection when a ping
// goes unanswered so superviseAccount reconnects. A half-open TCP connection
// (NAT timeout, sleep) never errors on read, and writes still succeed
// locally, so an idle account wouldn't notice otherwise.
//
// A tick arriving long after the last one means the process was suspended:
// messages sent meanwhile are only in the archive, so that forces a
// reconnect and resync too, even if the connection still works.
func keepAlive(ctx context.Context, s *accountSession, interval time.Duration) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic in keepalive", "jid", s.account.JID, "panic", r, "stack", string(debug.Stack()))
		}
	}()

	const suspendGapThreshold = 3 * keepAliveInterval

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	lastTick := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		gap := time.Since(lastTick)
		lastTick = time.Now()

		client, err := s.liveClient()
		if err != nil {
			continue // offline; reconnectWithBackoff owns getting back
		}

		if gap > suspendGapThreshold {
			slog.Warn("keepalive: large gap since last tick, likely process suspend; forcing reconnect", "jid", s.account.JID, "gap", gap)
			client.Kill()
			continue
		}

		pingCtx, cancel := context.WithTimeout(ctx, keepAliveTimeout)
		err = client.Ping(pingCtx)
		cancel()
		if err == nil {
			continue
		}
		if client.Closed() || ctx.Err() != nil {
			return // shutting down, not a dead connection
		}
		slog.Warn("keepalive ping failed; forcing reconnect", "jid", s.account.JID, "err", err)
		client.Kill()
	}
}

// reconnectWithBackoff retries Dial with exponential backoff (capped at 60s)
// until it succeeds or ctx is done, then stores the new client on s and
// flushes any messages queued while it was offline.
func reconnectWithBackoff(ctx context.Context, a *adapter, s *accountSession) {
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
				// The OMEMO managers' Transport is bound to the dead client.
				setupOmemo(ctx, s)
				// First: a new stream gets no roster pushes until it asked
				// for the roster (see refreshRoster).
				refreshRoster(ctx, a.srv, s.accountIdx, s, client)
				resyncPeerDeviceLists(ctx, s, derefRoster(s.roster.Load()))
				probeRosterPresence(ctx, client, derefRoster(s.roster.Load()))
				slog.Debug("account reconnected", "jid", s.account.JID)
				a.flushOutbox(ctx, s)
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

// listen handles one account's xmpp events until its client's Events
// channel closes.
func listen(ctx context.Context, srv *ipc.Server, accountIdx int, s *accountSession) {
	for ev := range s.client.Load().Events() {
		dispatchEvent(ctx, srv, accountIdx, s, ev)
	}
}

// dispatchEvent handles one xmpp.Event. Its recover is the only panic
// boundary for event handling: one bad event mustn't kill the daemon.
func dispatchEvent(ctx context.Context, srv *ipc.Server, accountIdx int, s *accountSession, ev xmpp.Event) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic handling xmpp event", "jid", s.account.JID, "event_type", fmt.Sprintf("%T", ev), "panic", r, "stack", string(debug.Stack()))
		}
	}()
	switch ev := ev.(type) {
	case xmpp.PresenceEvent:
		from := bareJID(ev.From)
		resource := resourcePart(ev.From)
		presence := mapPresence(ev)
		slog.Debug("presence received", "jid", s.account.JID, "from", from, "resource", resource, "presence", presence, "available", ev.Available, "show", ev.Show)
		s.setRosterPresence(from, resource, presence)
		if !ev.Available && resource != "" {
			s.peerWentOffline(ctx, ev.From)
		}
		broadcast(srv, ui.PresenceMsg{
			AccountIdx: accountIdx,
			From:       from,
			Presence:   presence,
			Resource:   resource,
		})
		if presence != ui.PresenceOffline && resource != "" {
			go s.resolveDeviceName(ctx, srv, accountIdx, from, resource)
			// Avatars are fetched as contacts come online, rather than
			// for the whole roster on connect.
			go s.syncAvatarOnce(ctx, srv, accountIdx, from)
		}
	case xmpp.SubscriptionRequestEvent:
		s.approveAndReciprocate(ctx, bareJID(ev.From))
	case xmpp.RosterPushEvent:
		s.handleRosterPush(ctx, srv, accountIdx, ev)
	case xmpp.MessageEvent:
		handleIncomingMessage(ctx, srv, accountIdx, s, ev)
	case xmpp.JingleMessageEvent:
		s.handleJingleMessage(ctx, srv, accountIdx, ev)
	case xmpp.JingleEvent:
		s.handleJingle(ctx, srv, accountIdx, ev)
	case xmpp.MessageBouncedEvent:
		s.handleBounce(ctx, srv, accountIdx, ev)
	case xmpp.ChatStateEvent:
		broadcast(srv, ui.TypingMsg{
			AccountIdx: accountIdx,
			From:       bareJID(ev.From),
			Typing:     ev.State == xmpp.ChatStateComposing,
		})
	case xmpp.MessageDeliveredEvent:
		from := bareJID(ev.From)
		if _, err := s.db.MarkMessageDelivered(ctx, storage.MarkMessageDeliveredParams{
			AccountJid: s.account.JID,
			IDAttr:     nullString(ev.ID),
			RosterJid:  nullString(from),
		}); err != nil {
			slog.Warn("persisting delivery receipt", "err", err)
		}
		broadcast(srv, ui.MessageDeliveredMsg{
			AccountIdx: accountIdx,
			From:       from,
			MessageID:  ev.ID,
		})
	case xmpp.AvatarChangedEvent:
		from := bareJID(ev.From)
		slog.Debug("received avatar PEP push", "from", from, "jid", s.account.JID)
		// Not syncAvatarOnce: the avatar changed.
		go s.syncAvatar(ctx, srv, accountIdx, from)
	case xmpp.DeviceListChangedEvent:
		from := bareJID(ev.From)
		// Our own JID must not be skipped: it's how our other clients'
		// device changes arrive, and without them our messages carry no
		// key for those devices.
		slog.Debug("received omemo device-list PEP push", "protocol", ev.Protocol, "from", from, "jid", s.account.JID)
		if from == "" {
			return
		}
		mgr := s.omemoV2()
		if ev.Protocol == omemolib.ProtocolV1 {
			mgr = s.omemoV1()
		}
		if mgr == nil {
			return
		}
		// Off the event loop: a slow fetch mustn't hold up messages.
		go func() {
			if err := mgr.SyncDevices(ctx, from); err != nil {
				slog.Warn("resyncing omemo device list after PEP push", "protocol", ev.Protocol, "from", from, "err", err)
				return
			}
			slog.Debug("resynced omemo device list after PEP push", "protocol", ev.Protocol, "from", from, "jid", s.account.JID)
		}()
	}
}

// presenceNames is each ui.Presence's config.toml status name, also its
// <show/> value (RFC 6121 §4.7.2.1) for chat/away/xa/dnd. Online has none:
// "" in config, no <show/> on the wire.
var presenceNames = map[ui.Presence]string{
	ui.PresenceChat: "chat", ui.PresenceAway: "away", ui.PresenceXA: "xa", ui.PresenceDND: "dnd",
	ui.PresenceOffline: "offline", ui.PresenceInvisible: "invisible",
}

// accountStatus maps a config.Account's status name to its ui.Presence;
// anything unknown is online.
func accountStatus(name string) ui.Presence {
	for p, n := range presenceNames {
		if n == name {
			return p
		}
	}
	return ui.PresenceOnline
}

// presenceShow is the <show/> value SetPresence sends for status, "" for
// none (offline is handled by not connecting rather than by any stanza).
func presenceShow(status ui.Presence) string {
	switch status {
	case ui.PresenceChat, ui.PresenceAway, ui.PresenceXA, ui.PresenceDND:
		return presenceNames[status]
	}
	return ""
}

// mapPresence reduces an xmpp.PresenceEvent to the UI's Presence enum.
func mapPresence(ev xmpp.PresenceEvent) ui.Presence {
	if !ev.Available {
		return ui.PresenceOffline
	}
	if p := accountStatus(ev.Show); presenceShow(p) != "" {
		return p
	}
	return ui.PresenceOnline
}

// syncArchive backfills every contact's history from the server archive
// (XEP-0313 MAM): what was sent from other devices or while offline.
// Best-effort per contact, since not every server offers MAM.
func syncArchive(ctx context.Context, srv *ipc.Server, accountIdx int, s *accountSession) {
	client := s.client.Load()

	lastCursor := make(map[string]mamCursor)
	if cursors, err := s.db.ListMamSyncCursors(ctx, s.account.JID); err == nil {
		for _, row := range cursors {
			lastCursor[row.Rosterjid] = mamCursor{archiveID: row.Archiveid, sentAt: row.Lastsentat}
		}
	}

	entries := s.roster.Load()
	if entries == nil {
		return
	}
	slog.Debug("syncArchive: contacts to check", "jid", s.account.JID, "contacts", len(*entries))
	for peerJID := range *entries {
		start := time.Now()
		syncArchiveForContact(ctx, srv, accountIdx, s, client, peerJID, lastCursor[peerJID])
		slog.Debug("syncArchiveForContact done", "jid", s.account.JID, "peer", peerJID, "elapsed", time.Since(start))
	}
}

// mamCursor is how far a contact's archive has been backfilled: the archive
// id to resume after and its item's time (0 if unknown). The time is kept
// with it because the cursor may sit on an item that has no messages row
// (see recoverMAMCursor).
type mamCursor struct {
	archiveID string
	sentAt    int64
}

// unixOrZero is t as a Unix timestamp, 0 for the zero time.
func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// syncArchiveForContact pages through peerJID's archive after cursor,
// storing and broadcasting each page.
func syncArchiveForContact(ctx context.Context, srv *ipc.Server, accountIdx int, s *accountSession, client *xmpp.Client, peerJID string, cursor mamCursor) {
	const pageSize = 50
	const maxPages = 200 // guards against a misbehaving server never reporting complete

	ownBare := bareJID(s.account.JID)
	name := s.rosterName(peerJID)

	// healed is the sender devices this sync already healed a broken session
	// with: one broken session fails every message after it.
	healed := make(map[omemolib.Device]bool)

	for page := 0; page < maxPages; page++ {
		var newMsgs []ui.Message
		prev := cursor
		pageCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		pageStart := time.Now()
		res, err := client.FetchArchive(pageCtx, peerJID, cursor.archiveID, pageSize)
		cancel()
		slog.Debug("mam page fetched", "jid", s.account.JID, "peer", peerJID, "page", page, "elapsed", time.Since(pageStart), "after", prev.archiveID, "items", len(res.Items), "last", res.Last, "complete", res.Complete, "err", err)
		if err != nil {
			break // no MAM here
		}
		if len(res.Items) == 0 && res.Last == "" {
			// A server can stop resolving an old cursor id and answer with
			// an empty page; retry from its time instead.
			if cursor.archiveID == "" {
				break
			}
			recovered, ok := s.recoverMAMCursor(ctx, client, peerJID, cursor, pageSize)
			if !ok {
				break
			}
			res = recovered
		}
		complete := res.Complete

		stop := false
		for _, am := range res.Items {
			cursor = mamCursor{archiveID: am.ArchiveID, sentAt: unixOrZero(am.SentAt)}

			outcome := s.processMAMItem(ctx, srv, accountIdx, peerJID, ownBare, name, am, healed)
			if outcome.stopSync {
				stop = true
				break
			}
			if outcome.msg != nil {
				newMsgs = append(newMsgs, *outcome.msg)
			}
		}
		if len(newMsgs) > 0 {
			broadcast(srv, ui.HistorySyncedMsg{AccountIdx: accountIdx, From: peerJID, Messages: newMsgs})
			// Messages that arrived while offline count as unread.
			countUnread(ctx, srv, s, peerJID, newMsgs)
		}
		// Resume from where the server says the page ended, not from the last
		// item kept: chat states and receipts are archived too and filtered
		// out, so a page can yield no items without being the end. The time
		// stays the last seen item's: as a recovery <start>, older is safe
		// and newer would skip.
		if !stop && res.Last != "" {
			cursor.archiveID = res.Last
		}
		// Advanced even when no item got a messages row, or an undecryptable
		// one would be retried on every sync. Also written when only the
		// time changed, so a recovery walk isn't repeated.
		if cursor != prev {
			if err := s.db.UpsertMamSyncCursor(ctx, storage.UpsertMamSyncCursorParams{
				AccountJid: s.account.JID,
				RosterJid:  peerJID,
				ArchiveID:  cursor.archiveID,
				LastSentAt: cursor.sentAt,
			}); err != nil {
				slog.Warn("persisting mam sync cursor", "peer", peerJID, "err", err)
			}
		}
		if stop || complete || cursor.archiveID == prev.archiveID {
			break
		}
	}
}

// recoverMAMCursor re-fetches from cursor's time with an XEP-0313 <start>
// filter, for when the RSM <after> query came back empty though the server
// accepted the id. Without a time (a cursor stored before it was recorded),
// it's looked up from the messages table, and failing that the archive is
// re-walked from the start - safe, since processMAMItem dedupes. ok is false
// if this comes back empty too, so the empty page was real.
func (s *accountSession) recoverMAMCursor(ctx context.Context, client *xmpp.Client, peerJID string, cursor mamCursor, max uint64) (page xmpp.ArchivePage, ok bool) {
	var since time.Time
	if cursor.sentAt > 0 {
		since = time.Unix(cursor.sentAt, 0)
	} else if delay, err := s.db.GetMessageDelayByArchiveID(ctx, storage.GetMessageDelayByArchiveIDParams{
		AccountJid: s.account.JID,
		ArchiveID:  sql.NullString{String: cursor.archiveID, Valid: true},
	}); err == nil {
		since = time.Unix(delay, 0)
	}

	pageCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	page, err := client.FetchArchiveSince(pageCtx, peerJID, since, max)
	slog.Debug("mam start-date fallback fetched", "jid", s.account.JID, "peer", peerJID, "since", since, "full_rewalk", since.IsZero(), "items", len(page.Items), "last", page.Last, "complete", page.Complete, "err", err)
	if err != nil || (len(page.Items) == 0 && page.Last == "") {
		return xmpp.ArchivePage{}, false
	}
	return page, true
}

// mamItemOutcome is one archive item's result: a new message to report,
// and/or stopSync once an item turned out already stored.
type mamItemOutcome struct {
	msg      *ui.Message
	stopSync bool
}

// processMAMItem decrypts and stores one archive item, under s.omemoMu.
func (s *accountSession) processMAMItem(ctx context.Context, srv *ipc.Server, accountIdx int, peerJID, ownBare, name string, am xmpp.ArchivedMessage, healed map[omemolib.Device]bool) mamItemOutcome {
	s.omemoMu.Lock()
	defer s.omemoMu.Unlock()

	// Checked before decrypting: an OMEMO message can't be decrypted twice,
	// its key is consumed. Stored already by an earlier sync...
	if exists, err := s.db.MessageExistsByArchiveID(ctx, storage.MessageExistsByArchiveIDParams{
		AccountJid: s.account.JID,
		ArchiveID:  nullString(am.ArchiveID),
	}); err == nil && exists {
		return mamItemOutcome{}
	}

	// ...or by the live path, which has no archive id...
	if am.ID != "" {
		if exists, err := s.db.MessageExistsByIDAttr(ctx, storage.MessageExistsByIDAttrParams{
			AccountJid: s.account.JID,
			RosterJid:  nullString(peerJID),
			IDAttr:     nullString(am.ID),
		}); err == nil && exists {
			return mamItemOutcome{}
		}
	}

	// ...or a correction already applied, which has no row of its own (see
	// appliedCorrections in schema.sql).
	if am.ArchiveID != "" {
		if exists, err := s.db.CorrectionAppliedByArchiveID(ctx, storage.CorrectionAppliedByArchiveIDParams{
			AccountJid: s.account.JID,
			ArchiveID:  nullString(am.ArchiveID),
		}); err == nil && exists {
			return mamItemOutcome{}
		}
	}
	if am.ID != "" {
		if exists, err := s.db.CorrectionAppliedByIDAttr(ctx, storage.CorrectionAppliedByIDAttrParams{
			AccountJid: s.account.JID,
			RosterJid:  peerJID,
			IDAttr:     nullString(am.ID),
		}); err == nil && exists {
			return mamItemOutcome{}
		}
	}

	// Retractions, reactions and corrections change a stored message, which
	// may be on screen, so they're applied and broadcast right away.
	if am.RetractID != "" {
		if _, err := s.db.MarkMessageRetracted(ctx, storage.MarkMessageRetractedParams{
			AccountJid: s.account.JID,
			IDAttr:     nullString(am.RetractID),
			RosterJid:  nullString(peerJID),
			Sent:       bareJID(am.From) == ownBare,
		}); err != nil {
			slog.Warn("persisting mam retraction", "peer", peerJID, "err", err)
		}
		broadcast(srv, ui.MessageRetractedMsg{AccountIdx: accountIdx, From: peerJID, RetractID: am.RetractID})
		return mamItemOutcome{}
	}
	if am.ReactionTargetID != "" {
		if err := replaceReactions(ctx, s, peerJID, am.ReactionTargetID, bareJID(am.From), am.Reactions); err != nil {
			slog.Warn("persisting mam reactions", "peer", peerJID, "err", err)
		}
		broadcast(srv, ui.MessageReactionsMsg{
			AccountIdx: accountIdx,
			From:       peerJID,
			MessageID:  am.ReactionTargetID,
			Reactions:  loadReactionsForMessage(ctx, s, peerJID, am.ReactionTargetID),
		})
		return mamItemOutcome{}
	}

	if am.Body == "" && am.Encrypted == nil && am.EncryptedV1 == nil && am.ReplaceID == "" {
		return mamItemOutcome{}
	}

	body := am.Body
	oobURLs := am.OOBURLs
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
		var protocol omemolib.Protocol
		var enc *omemolib.EncryptedMessage
		var decodeErr error
		if am.Encrypted != nil {
			mgr, protocol = s.omemoV2(), omemolib.ProtocolV2
			enc, decodeErr = xmpp.DecodeOmemoMessage(am.Encrypted, bareJID(am.From))
		} else {
			mgr, protocol = s.omemoV1(), omemolib.ProtocolV1
			enc, decodeErr = xmpp.DecodeOmemoMessageV1(am.EncryptedV1, bareJID(am.From))
		}
		if mgr == nil {
			slog.Warn("mam: decrypting omemo message failed", "jid", s.account.JID, "peer", peerJID, "archive_id", am.ArchiveID, "from", am.From, "sent_at", am.SentAt, "err", "omemo isn't ready")
			body = "[message could not be decrypted: omemo isn't ready]"
			decryptFailed = true
		} else if decodeErr != nil {
			slog.Warn("mam: decrypting omemo message failed", "jid", s.account.JID, "peer", peerJID, "archive_id", am.ArchiveID, "from", am.From, "sent_at", am.SentAt, "err", decodeErr)
			body = "[message could not be decrypted: " + decodeErr.Error() + "]"
			decryptFailed = true
		} else if pt, err := mgr.DecryptMessage(ctx, enc); errors.Is(err, omemolib.ErrOwnDeviceKeyMissing) {
			// Not for this device; see handleIncomingMessage.
			slog.Debug("mam: omemo message has no key for this device, skipping", "archive_id", am.ArchiveID, "from", am.From)
			return mamItemOutcome{}
		} else if err != nil {
			// sent_at tells a broken session (recent) from an item older than
			// the ratchet keys that could read it.
			slog.Warn("mam: decrypting omemo message failed", "jid", s.account.JID, "peer", peerJID, "archive_id", am.ArchiveID, "from", am.From, "sent_at", am.SentAt, "err", err)
			body = "[message could not be decrypted: " + err.Error() + "]"
			decryptFailed = true
			if (errors.Is(err, omemolib.ErrUnknownSession) || errors.Is(err, omemolib.ErrPreKeyNotFound)) && !healed[enc.Sender] {
				// As handleIncomingMessage does, once per device per sync.
				healed[enc.Sender] = true
				healBrokenSession(ctx, s, mgr, protocol, enc.Sender, bareJID(am.From))
			}
		} else if pt == nil {
			return mamItemOutcome{} // key-transport only: session established/refreshed, no content to show
		} else {
			body = string(pt)
		}
	} else if s.useGPG && gpg.Looks(body) {
		if pt, err := s.gpg.Decrypt(body, ""); err == nil {
			body = pt
		}
	}
	// The in-band fallback quote some clients add to replies.
	body = stripReplyQuote(body)
	if len(oobURLs) == 0 {
		oobURLs = aesgcmURLsInBody(body)
	}
	sent := bareJID(am.From) == ownBare
	quotedAuthor, quotedPreview := resolveReplyQuote(ctx, s, peerJID, name, am.ReplyToID)
	sealedBody, encrypted := encryptForStorage(s, body)

	if am.ReplaceID != "" {
		if _, err := s.db.UpdateMessageBodyByID(ctx, storage.UpdateMessageBodyByIDParams{
			AccountJid:   s.account.JID,
			Body:         sealedBody,
			Encrypted:    encrypted,
			E2eEncrypted: e2eEncrypted,
			E2eeMethod:   nullString(e2eeMethod),
			Edited:       true,
			IDAttr:       nullString(am.ReplaceID),
			RosterJid:    nullString(peerJID),
			Sent:         sent,
		}); err != nil {
			slog.Warn("persisting mam correction", "peer", peerJID, "err", err)
		}
		if am.ID != "" || am.ArchiveID != "" {
			if err := s.db.InsertAppliedCorrection(ctx, storage.InsertAppliedCorrectionParams{
				AccountJid: s.account.JID,
				RosterJid:  peerJID,
				IDAttr:     nullString(am.ID),
				ArchiveID:  nullString(am.ArchiveID),
			}); err != nil {
				slog.Warn("recording applied mam correction", "peer", peerJID, "err", err)
			}
		}
		broadcast(srv, ui.MessageCorrectedMsg{
			AccountIdx: accountIdx,
			From:       peerJID,
			ReplaceID:  am.ReplaceID,
			NewContent: body,
			Encrypted:  e2eEncrypted,
			EncMethod:  e2eeMethod,
		})
		return mamItemOutcome{}
	}

	_, err := s.db.InsertMessage(ctx, storage.InsertMessageParams{
		AccountJid:        s.account.JID,
		Sent:              sent,
		ToAttr:            nullString(am.To),
		FromAttr:          nullString(am.From),
		IDAttr:            nullString(am.ID),
		Body:              sealedBody,
		Encrypted:         encrypted,
		E2eEncrypted:      e2eEncrypted,
		E2eeMethod:        nullString(e2eeMethod),
		StanzaType:        "chat",
		Delay:             am.SentAt.Unix(),
		RosterJid:         nullString(peerJID),
		ArchiveID:         nullString(am.ArchiveID),
		OobUrls:           joinOOBURLs(oobURLs),
		ReplyToIDAttr:     nullString(am.ReplyToID),
		ReplyQuoteAuthor:  nullString(quotedAuthor),
		ReplyQuotePreview: nullString(quotedPreview),
	})
	if err != nil {
		if strings.Contains(err.Error(), "archiveID") {
			// Already stored, and so is everything after it.
			return mamItemOutcome{stopSync: true}
		}
		slog.Warn("persisting mam history", "peer", peerJID, "err", err)
		return mamItemOutcome{}
	}

	author := name
	if sent {
		author = "me"
	}
	return mamItemOutcome{msg: &ui.Message{
		ID:            am.ID,
		Author:        author,
		Content:       body,
		SentAt:        am.SentAt,
		IsMe:          sent,
		Encrypted:     e2eEncrypted,
		EncMethod:     e2eeMethod,
		Attachments:   oobURLs,
		DecryptFailed: decryptFailed,
		ReplyToID:     am.ReplyToID,
		QuotedAuthor:  quotedAuthor,
		QuotedPreview: quotedPreview,
	}}
}

// bareJID strips a JID's resource.
func bareJID(addr string) string {
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		return addr[:i]
	}
	return addr
}

// resourcePart is a JID's resource, "" if none.
func resourcePart(addr string) string {
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		return addr[i+1:]
	}
	return ""
}

// mutateRoster applies fn to a copy of the cached roster and stores it, under
// rosterMu. Every roster update goes through here. fn must not block: every
// presence stanza does this.
func (s *accountSession) mutateRoster(fn func(entries map[string]rosterEntry)) map[string]rosterEntry {
	s.rosterMu.Lock()
	defer s.rosterMu.Unlock()

	current := derefRoster(s.roster.Load())
	updated := make(map[string]rosterEntry, len(current)+1)
	for k, v := range current {
		updated[k] = v
	}
	fn(updated)
	s.roster.Store(&updated)
	return updated
}

// handleBounce marks our message the recipient's side bounced as failed -
// it never arrived, whatever our own server's ack said - and tells every
// attached TUI why. A bounce whose ID isn't one of our stored messages (a
// chat state, receipt or reaction of ours) has nothing to mark; it's only
// logged.
func (s *accountSession) handleBounce(ctx context.Context, srv *ipc.Server, accountIdx int, ev xmpp.MessageBouncedEvent) {
	peer := bareJID(ev.From)
	n, err := s.db.MarkMessageBounced(ctx, storage.MarkMessageBouncedParams{
		AccountJid: s.account.JID,
		IDAttr:     nullString(ev.ID),
		RosterJid:  nullString(peer),
	})
	if err != nil {
		slog.Warn("persisting bounced message", "jid", s.account.JID, "to", peer, "id", ev.ID, "err", err)
	}
	if n == 0 {
		slog.Debug("bounce for nothing we stored", "jid", s.account.JID, "from", ev.From, "id", ev.ID, "reason", ev.Reason)
		return
	}
	slog.Warn("message not delivered", "jid", s.account.JID, "to", ev.From, "id", ev.ID, "reason", ev.Reason)
	broadcast(srv, ui.MessageSendFailedMsg{
		AccountIdx: accountIdx,
		To:         peer,
		MessageID:  ev.ID,
		Reason:     ev.Reason,
	})
}

// approveAndReciprocate approves peer's subscription request and, unless we
// already have their presence, asks for it in return, as other clients do:
// approving alone leaves a one-sided subscription="from", where peer looks
// offline to us. Not asked when we already have it, which would prompt peer
// for nothing.
func (s *accountSession) approveAndReciprocate(ctx context.Context, peer string) {
	client := s.client.Load()
	if client == nil {
		return
	}
	if err := client.ApproveSubscription(ctx, peer); err != nil {
		slog.Warn("approving subscription request", "from", peer, "jid", s.account.JID, "err", err)
		return
	}

	subs := derefRoster(s.roster.Load())[peer].Subs
	if subs == "to" || subs == "both" {
		slog.Debug("approved subscription request; already subscribed to peer", "from", peer, "jid", s.account.JID, "subs", subs)
		return
	}
	if err := client.ResubscribeContact(ctx, peer); err != nil {
		slog.Warn("requesting peer's presence after approving their request", "from", peer, "jid", s.account.JID, "err", err)
		return
	}
	slog.Debug("approved subscription request and requested peer's presence in return", "from", peer, "jid", s.account.JID, "subs", subs)
}

// handleRosterPush applies an RFC 6121 §2.1.6 roster push - a roster change
// this client didn't make - to the cache, storage and attached TUIs.
func (s *accountSession) handleRosterPush(ctx context.Context, srv *ipc.Server, accountIdx int, ev xmpp.RosterPushEvent) {
	peer := bareJID(ev.JID)
	if peer == s.account.JID {
		return // our own bare JID is not a contact
	}
	slog.Debug("roster push received", "jid", s.account.JID, "contact", peer,
		"subscription", ev.Subscription, "ask", ev.Ask, "removed", ev.Removed)

	if ev.Removed {
		s.mutateRoster(func(entries map[string]rosterEntry) { delete(entries, peer) })
		if err := s.db.DeleteRosterByJID(ctx, storage.DeleteRosterByJIDParams{
			AccountJid: s.account.JID, Jid: peer,
		}); err != nil {
			slog.Warn("deleting roster entry after roster push", "contact", peer, "err", err)
		}
		forgetChat(ctx, s.db, s.account.JID, peer)
		broadcast(srv, ui.ChatRemovedMsg{AccountIdx: accountIdx, Address: peer})
		return
	}

	var known bool
	s.mutateRoster(func(entries map[string]rosterEntry) {
		e, ok := entries[peer]
		known = ok
		e.Name, e.Subs, e.Ask = ev.Name, ev.Subscription, ev.Ask
		entries[peer] = e
	})
	if err := s.db.UpsertRoster(ctx, storage.UpsertRosterParams{
		AccountJid: s.account.JID, Jid: peer, Name: ev.Name, Subs: ev.Subscription,
	}); err != nil {
		slog.Warn("persisting roster entry after roster push", "contact", peer, "err", err)
	}

	// For a known contact too: ChatAddedMsg is an upsert, carrying renames and
	// subscription changes.
	s.broadcastChat(ctx, srv, accountIdx, peer, ev.Name)

	// The server only sends a newly visible contact's presence at our next
	// connect, unless probed.
	if !known || ev.Subscription == "to" || ev.Subscription == "both" {
		if client := s.client.Load(); client != nil {
			if err := client.ProbePresence(ctx, peer); err != nil {
				slog.Debug("probing presence after roster push", "contact", peer, "err", err)
			}
		}
	}
}

// broadcastChat tells attached clients about a chat that exists daemon-side,
// with whatever history storage holds for it. Safe to call for a chat a
// client already shows: ui.ChatAddedMsg is an upsert.
func (s *accountSession) broadcastChat(ctx context.Context, srv *ipc.Server, accountIdx int, peer, name string) {
	chat, hist, _ := s.newChat(ctx, peer, name, nil)
	entry := derefRoster(s.roster.Load())[peer]
	chat.Presence, chat.Resources = entry.Presence, entry.Resources
	chat.Subscription, chat.SubscriptionPending = entry.Subs, entry.Ask
	if unread, err := s.db.GetChatUnread(ctx, storage.GetChatUnreadParams{
		AccountJid: s.account.JID, RosterJid: peer,
	}); err == nil { // no row yet simply means nothing unread
		chat.Unread = int(unread)
	}
	broadcast(srv, ui.ChatAddedMsg{AccountIdx: accountIdx, Chat: chat, Messages: hist})
}

// newChat is peer's chat row as storage and the chat flags have it, with
// its latest page of history and then pending appended. name is its roster
// name, "" for none.
func (s *accountSession) newChat(ctx context.Context, peer, name string, pending []ui.Message) (chat ui.Chat, hist []ui.Message, hasMore bool) {
	name = cmp.Or(name, peer)
	hist, hasMore, _ = loadHistoryWindow(ctx, s, peer, name, nil, historyPageSize)
	hist = append(hist, pending...)
	chat = ui.Chat{
		Name:           name,
		Address:        peer,
		EncryptionMode: resolveEncryptionMode(ctx, s, peer),
		Hidden:         hiddenChats.has(s.account.JID, peer),
		Pinned:         pinnedChats.has(s.account.JID, peer),
		Muted:          mutedChats.has(s.account.JID, peer),
	}
	if len(hist) > 0 {
		chat.LastMessage = ui.MessagePreviewContent(hist[len(hist)-1])
		chat.LastActivity = hist[len(hist)-1].SentAt
	}
	return chat, hist, hasMore
}

// omemoSetupGrace is how long an incoming OMEMO message waits for setupOmemo
// before being stored as undecryptable. Generous: setup is several PEP round
// trips and can take seconds.
const omemoSetupGrace = 15 * time.Second

// signalOmemoReady marks this account's OMEMO setup as finished. Idempotent:
// setupOmemo runs on every reconnect.
func (s *accountSession) signalOmemoReady() {
	if s.omemoReady == nil {
		return
	}
	s.omemoReadyOnce.Do(func() { close(s.omemoReady) })
}

// waitOmemoReady blocks until this account's OMEMO setup has completed, ctx
// is done, or d elapses.
func (s *accountSession) waitOmemoReady(ctx context.Context, d time.Duration) {
	if s.omemoReady == nil {
		return
	}
	select {
	case <-s.omemoReady:
		return
	default:
	}
	if s.omemoWaitGaveUp.Load() {
		return
	}
	slog.Debug("waiting for omemo setup to finish", "jid", s.account.JID, "timeout", d)
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-s.omemoReady:
	case <-ctx.Done():
	case <-timer.C:
		s.omemoWaitGaveUp.Store(true)
		slog.Warn("gave up waiting for omemo setup; will not wait again until it succeeds", "jid", s.account.JID, "timeout", d)
	}
}

// refreshRoster re-fetches the roster on a reconnected stream, reconciling
// the cache, storage and attached TUIs with the server, contacts it no longer
// lists included (see staleRosterEntries).
//
// It's also what gets the new stream roster pushes at all: RFC 6121 only
// sends them to resources that asked for the roster (§2.1.6, §2.2).
func refreshRoster(ctx context.Context, srv *ipc.Server, accountIdx int, s *accountSession, client *xmpp.Client) {
	contacts, err := client.Roster(ctx)
	if err != nil {
		slog.Warn("refreshing roster", "jid", s.account.JID, "err", err)
		return
	}
	slog.Debug("roster refreshed", "jid", s.account.JID, "contacts", len(contacts))

	live := make(map[string]string, len(contacts))
	for _, c := range contacts {
		live[c.JID] = c.Name
	}

	var added, removed []string
	s.mutateRoster(func(entries map[string]rosterEntry) {
		for _, c := range contacts {
			e, known := entries[c.JID]
			// Only what the roster is authoritative for, as in
			// connectAccountLive.
			e.Name, e.Subs = c.Name, c.Subscription
			entries[c.JID] = e
			if !known {
				added = append(added, c.JID)
			}
		}
		removed = staleRosterEntries(entries, live)
		for _, jid := range removed {
			delete(entries, jid)
		}
	})

	for _, c := range contacts {
		if err := s.db.UpsertRoster(ctx, storage.UpsertRosterParams{
			AccountJid: s.account.JID, Jid: c.JID, Name: c.Name, Subs: c.Subscription,
		}); err != nil {
			slog.Warn("persisting refreshed roster entry", "jid", c.JID, "err", err)
		}
	}
	for _, jid := range added {
		slog.Debug("roster refresh found a new contact", "jid", s.account.JID, "contact", jid)
		s.broadcastChat(ctx, srv, accountIdx, jid, live[jid])
	}
	healLopsidedSubscriptions(ctx, s, contacts, client.ResubscribeContact)

	for _, jid := range removed {
		slog.Debug("roster refresh dropped a contact the server no longer lists", "jid", s.account.JID, "contact", jid)
		if err := s.db.DeleteRosterByJID(ctx, storage.DeleteRosterByJIDParams{
			AccountJid: s.account.JID, Jid: jid,
		}); err != nil {
			slog.Warn("deleting roster entry after roster refresh", "contact", jid, "err", err)
		}
		forgetChat(ctx, s.db, s.account.JID, jid)
		broadcast(srv, ui.ChatRemovedMsg{AccountIdx: accountIdx, Address: jid})
	}
}

// healLopsidedSubscriptions re-requests presence on every connect from each
// contact at subscription="from" (they see us, we don't see them), in case
// approveAndReciprocate's request got lost. A contact who declined us is at
// "none", so isn't asked again. resubscribe is client.ResubscribeContact.
func healLopsidedSubscriptions(
	ctx context.Context, s *accountSession, contacts []xmpp.Contact,
	resubscribe func(context.Context, string) error,
) {
	for _, c := range contacts {
		if c.Subscription != "from" {
			continue
		}
		if err := resubscribe(ctx, c.JID); err != nil {
			slog.Warn("re-requesting presence for a contact we can't see", "jid", s.account.JID, "contact", c.JID, "err", err)
			continue
		}
		slog.Debug("re-requested presence for a contact stuck at subscription=from", "jid", s.account.JID, "contact", c.JID)
	}
}

// staleRosterEntries is the cached contacts a fetched roster (live: bare JID
// to name) no longer lists, sorted. Never stale:
//
//   - entries with an empty subscription: local-only chats ensureChat made
//     for someone who messaged us, never on the server's roster;
//   - anything, when the fetch was empty: that's more likely a response
//     without the roster than every contact deleted, which the removal
//     pushes report anyway.
func staleRosterEntries(cached map[string]rosterEntry, live map[string]string) []string {
	if len(live) == 0 {
		return nil
	}
	var stale []string
	for jid, e := range cached {
		if _, ok := live[jid]; ok || e.Subs == "" {
			continue
		}
		stale = append(stale, jid)
	}
	sort.Strings(stale)
	return stale
}

// invisibleSupported reports whether sess's server advertised XEP-0186, from
// the cache connectAccountLive warmed, never a round trip.
func invisibleSupported(sess *accountSession) bool {
	client := sess.client.Load()
	if client == nil {
		return false
	}
	supported, _ := client.InvisibleSupportedCached()
	return supported
}
