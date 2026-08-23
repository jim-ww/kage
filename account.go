package main

import (
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

// accountSession bundles everything needed to send/receive/persist for one
// configured account. client is swapped out on reconnect, so it's stored
// behind an atomic pointer — Send (called from the Bubble Tea event loop)
// and the reconnect supervisor (its own goroutine) touch it concurrently.
type accountSession struct {
	account    config.Account
	accountIdx int // index into adapter.sessions; set once at connect time, needed to replay queued sends via adapter.send
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

	// omemoMu serializes OMEMO decrypt+persist across the live path
	// (events.go's handleIncomingMessage) and MAM backfill
	// (syncArchiveForContact's processMAMItem): both run as separate
	// goroutines against the same double-ratchet/prekey state in storage,
	// and without this lock a message arriving live while backfill is still
	// catching up on the same conversation can get decrypted twice
	// concurrently - the first decrypt consumes the one-time prekey (or the
	// cached skipped-message-key), and the second then fails with "no rows
	// in result set" or "no message key is cached" instead of finding the
	// row the first decrypt is about to insert.
	omemoMu sync.Mutex

	// callMu guards call, the one voice call this account can have in flight
	// (see callsession.go). One at a time is enough: there's no call waiting.
	callMu sync.Mutex
	call   *callSession

	// ackMu guards ackPending/ackTimer: messages sent since the last
	// confirmPendingAcks flush, waiting on a debounced XEP-0199 ping to
	// confirm the server actually received them - see
	// trackForServerAck/confirmPendingAcks.
	ackMu      sync.Mutex
	ackPending []pendingAck
	ackTimer   *time.Timer
}

// pendingAck is one send queued in accountSession.ackPending, waiting on
// confirmPendingAcks's debounced ping.
type pendingAck struct {
	to, id string
}

// ackDebounce is how long trackForServerAck waits after the most recently
// queued send before actually pinging the server. Sending several messages
// in a burst (e.g. typing quickly) shares a single ping/pong round trip to
// confirm all of them at once rather than paying one per message - the same
// reason real XEP-0198 doesn't require a stream-management ack request
// after every single stanza either.
const ackDebounce = 500 * time.Millisecond

// trackForServerAck queues (to, id) - a message adapter.send just handed
// off successfully - to be confirmed against the server by a debounced ping
// (see ackDebounce and confirmPendingAcks). A local Send() call returning
// without an error only proves the write reached the local socket buffer,
// not that the server ever processed it (see Message.ServerAcked's doc
// comment) - this is what actually closes that gap.
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

// confirmPendingAcks is trackForServerAck's debounced timer callback: pings
// the server once and, only if that round trip actually succeeds, marks
// every message queued since the last flush as ServerAcked together -
// rather than confirming (or guessing) each individually. Runs detached on
// its own goroutine (via time.AfterFunc, same as resolveDeviceName's
// pattern for background PEP work), so it recovers its own panics.
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
		// Already offline by the time the debounce fired - nothing to ping,
		// and this batch stays unacked (no ✓) rather than guessing either
		// way. Whatever eventually reconnects and sends something new starts
		// a fresh batch.
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := client.Ping(ctx); err != nil {
		// The server never actually answered for this batch - exactly the
		// silent-drop scenario a bare "the local write returned no error"
		// can't detect on its own (a half-open TCP connection a NAT/
		// middlebox dropped silently: writes still succeed into the local
		// socket buffer, but nothing ever answers). Two things follow from
		// that, not just leaving the batch unacked as before:
		//
		//  1. Every message in this batch gets flagged Failed rather than
		//     sitting forever with no status glyph at all - the user typed
		//     it, we have positive evidence it never reached the server, so
		//     say so instead of staying silent (see Message.Failed's doc
		//     comment on why an ambiguous non-status is worse than a ✗).
		//  2. Kill the connection so superviseAccount's normal
		//     disconnect-detection path (which otherwise only fires once a
		//     read or write actually errors - which a half-open connection
		//     may never do on its own) notices right away and starts
		//     reconnecting, instead of leaving every subsequent send in the
		//     same boat until something else eventually surfaces the drop.
		slog.Warn("confirming server ack failed; marking batch failed and forcing reconnect", "jid", s.account.JID, "batch", len(pending), "err", err)
		for _, p := range pending {
			if _, err := s.db.MarkMessageSendFailed(ctx, storage.MarkMessageSendFailedParams{
				AccountJid: s.account.JID,
				IDAttr:     nullString(p.id),
				RosterJid:  nullString(p.to),
			}); err != nil {
				slog.Warn("persisting send failure", "jid", s.account.JID, "to", p.to, "err", err)
			}
			broadcast(srv, evMessageSendFailed, ui.MessageSendFailedMsg{
				AccountIdx: accountIdx,
				To:         p.to,
				MessageID:  p.id,
			})
		}
		client.Kill()
		return
	}

	for _, p := range pending {
		if _, err := s.db.MarkMessageServerAcked(ctx, storage.MarkMessageServerAckedParams{
			AccountJid: s.account.JID,
			IDAttr:     nullString(p.id),
			RosterJid:  nullString(p.to),
		}); err != nil {
			slog.Warn("persisting server ack", "jid", s.account.JID, "to", p.to, "err", err)
			continue
		}
		broadcast(srv, evMessageServerAcked, ui.MessageServerAckedMsg{
			AccountIdx: accountIdx,
			To:         p.to,
			MessageID:  p.id,
		})
	}
}

// queuedSend is one send loaded from the outbox table (see its schema doc
// comment) for replay by adapter.flushOutbox. filePath is empty for a plain
// message/reaction/retraction/correction (replayed through adapter.send
// verbatim); non-empty for a staged attachment (replayed through
// adapter.flushOutbox's upload-then-send, body used as the message text).
type queuedSend struct {
	dbID     int64 // storage.Outbox.ID - the row to delete once this entry is actually attempted, not before (see flushOutbox)
	to, body string
	opts     ui.SendOptions
	filePath string
}

// enqueueOutbox persists a send to the outbox table for later replay by
// adapter.flushOutbox — real storage, not an in-memory slice, so a
// crash/restart while offline doesn't silently lose it the way the
// in-memory version this replaced did. Reactions/retractions/corrections
// carry a target ID from an earlier send, so replaying them later is safe
// *unless* that target message was itself still queued (empty ID) when the
// user reacted/retracted/corrected it - see adapter.send's early return for
// queued sends, which is the source of that gap.
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

// markOutboxFailed persists a real (non-queued, non-offline) send failure -
// e.g. adapter.send's "omemo not ready" - as a durable failed=true row,
// exactly like enqueueOutbox does for the offline/queued case. Without this,
// a Failed message only ever lived in whichever TUI process's in-memory
// Model happened to render the ✗ marker: restarting just that process (not
// the daemon) rebuilds its view from storage via listAccounts, which had no
// record of the failure at all, and the message silently vanished. Always a
// fresh row, never an update to an existing one: by the time this runs, any
// outbox row this attempt came from (a flushOutbox replay) has already been
// deleted (see flushOutbox's doc comment), and a live first attempt from
// sendCurrentInput never had one to begin with.
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

// pendingOutboxMessagesByPeer loads every plain new-message or
// staged-attachment send currently sitting in the outbox - both
// still-queued (Pending) and given-up (Failed) - and groups them by
// recipient (toAttr), oldest first, as ui.Messages ready to append onto
// that chat's already-loaded history. The DB-backed outbox's rows are
// otherwise invisible to the chat view until a queued one is actually sent
// (MessageSendResolvedMsg) or the app happens to be running when
// flushOutbox gets to it, and a failed one is never otherwise reported at
// all once the TUI process that saw it live is gone - see
// markOutboxFailed's doc comment. Reactions/retractions/corrections are
// skipped: they don't correspond to a new chat-history row, they target one
// that's already showing - markOutboxFailed never writes one of these
// anyway, only enqueueOutbox does.
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

// loadOutbox returns every send still eligible for an automatic retry in
// this account's outbox (i.e. failed = FALSE - see outbox.failed's doc
// comment), oldest first (replay order), decrypting each body with
// s.localKey the same way loadDraft does for a stored draft. Used by
// flushOutbox, which must never resurrect a send the user already saw
// marked Failed.
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

// deleteOutboxRow removes one outbox entry by its storage id, once
// flushOutbox has actually attempted it (successfully or not) - see
// flushOutbox's doc comment for why this happens before the send attempt,
// not after.
func (s *accountSession) deleteOutboxRow(ctx context.Context, dbID int64) error {
	return s.db.DeleteOutboxEntry(ctx, dbID)
}

// deleteOutboxByLocalID permanently removes a still-queued send by its
// client-generated LocalID without ever sending it - the user explicitly
// discarding a Pending message (Ctrl+Shift+D), as opposed to a normal
// message delete which only flags Retracted. found is false (with a nil
// error) if no matching row existed - e.g. it lost a race with flushOutbox
// actually sending it in the meantime - in which case to is meaningless.
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

// rosterEntry is a contact's cached roster state, refreshed at connect time
// and kept in sync locally by RenameContact.
type rosterEntry struct {
	Name      string
	Subs      string
	Presence  ui.Presence           // last known live presence; zero value (PresenceOffline) until a PresenceEvent arrives
	Resources []ui.ResourcePresence // currently online devices (full-JID resources), sorted by resource name
}

// setRosterPresence records bareJID's live presence into the cached roster
// so a client that attaches (or re-attaches) after this account's initial
// presence burst still sees current status via listAccounts, instead of
// only ever learning it from a live PresenceEvent it happened to be
// connected in time to receive. resource (the full JID's resource part,
// "" if it had none) is folded into Resources the same way ui.Chat's own
// copy is - see Chat.withResource.
func (s *accountSession) setRosterPresence(bareJID, resource string, presence ui.Presence) {
	entries := s.roster.Load()
	updated := make(map[string]rosterEntry, len(derefRoster(entries))+1)
	for k, v := range derefRoster(entries) {
		updated[k] = v
	}
	e := updated[bareJID]
	e.Presence = presence
	e.Resources = withResource(e.Resources, resource, presence)
	updated[bareJID] = e
	s.roster.Store(&updated)
}

// resolveDeviceName looks up (via XEP-0030 disco#info) and caches the
// human-readable client name for one contact's freshly-online resource,
// then broadcasts it to any attached UI once known. Runs detached from the
// PresenceEvent that triggered it, on its own goroutine and a bounded
// timeout: many clients answer disco#info slowly or not at all, and
// presence handling shouldn't wait on it - the UI just shows a
// resourcepart-derived fallback name until (if ever) this arrives.
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

	entries := s.roster.Load()
	updated := make(map[string]rosterEntry, len(derefRoster(entries)))
	for k, v := range derefRoster(entries) {
		updated[k] = v
	}
	e := updated[bareJID]
	for i := range e.Resources {
		if e.Resources[i].Resource == resource {
			e.Resources[i].Name = name
		}
	}
	updated[bareJID] = e
	s.roster.Store(&updated)

	broadcast(srv, evDeviceName, ui.DeviceNameMsg{
		AccountIdx: accountIdx,
		From:       bareJID,
		Resource:   resource,
		Name:       name,
	})
}

// withResource is rosterEntry's version of ui.Chat.withResource - kept
// separate since account.go can't depend on ui's unexported helper, but the
// two must behave identically or the daemon's cache and a freshly-attached
// UI's initial snapshot would disagree about which devices are online.
func withResource(resources []ui.ResourcePresence, resource string, presence ui.Presence) []ui.ResourcePresence {
	if resource == "" {
		return resources
	}
	updated := make([]ui.ResourcePresence, 0, len(resources)+1)
	for _, r := range resources {
		if r.Resource != resource {
			updated = append(updated, r)
		}
	}
	if presence != ui.PresenceOffline {
		updated = append(updated, ui.ResourcePresence{Resource: resource, Presence: presence})
		sort.Slice(updated, func(i, j int) bool { return updated[i].Resource < updated[j].Resource })
	}
	return updated
}

func derefRoster(m *map[string]rosterEntry) map[string]rosterEntry {
	if m == nil {
		return nil
	}
	return *m
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

// ownResources returns the resource parts of this account's other currently
// online devices (e.g. "kage-a1b2", a phone's "Conversations.xyz") - learned
// the same way any contact's resources are, via setRosterPresence, since a
// server sends an account presence about its own other connected resources
// (RFC 6121 §4.4.2) same as for a roster contact's. Used by
// rejectAndNotifyOwnDevices to reach every sibling resource directly, since
// a message addressed only to our bare JID isn't reliably fanned out to all
// of them by every server.
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

// connectAndSuperviseAccount loads one configured account's local
// roster/history from disk first — fast, no network — and reports it to the
// UI immediately so local chats/messages appear on screen right away. Only
// then does it dial and fetch the live roster in the background, backfill
// anything missed via XEP-0313 MAM, and fall into the normal
// event-listen/reconnect supervisor loop.
func connectAndSuperviseAccount(ctx context.Context, srv *ipc.Server, a *adapter, idx int, acct config.Account, queries *storage.Queries, localKey []byte) {
	slog.Debug("connectAccountLocal starting", "jid", acct.JID)
	start := time.Now()
	sess, uiAcct, err := connectAccountLocal(ctx, acct, queries, localKey)
	if err != nil {
		slog.Debug("connectAccountLocal failed", "jid", acct.JID, "elapsed", time.Since(start), "err", err)
		broadcast(srv, evAccountConnectError, wireAccountConnectErrorMsg{Index: idx, Err: err.Error()})
		return
	}
	sess.accountIdx = idx
	sess.useGPG = a.useGPG
	sess.useKeyring = a.useKeyring
	slog.Debug("connectAccountLocal done", "jid", acct.JID, "elapsed", time.Since(start), "chats", len(uiAcct.Chats))

	// Register the session as soon as local (no-network) state is loaded,
	// not after connectAccountLive resolves below - that dial can sit for a
	// long time waiting on a DNS/connect timeout while genuinely offline,
	// and a.session(idx) must already succeed during that whole window so
	// a send attempted meanwhile queues onto sess.outbox instead of failing
	// with "unknown account" (there was nothing to look up yet).
	a.mu.Lock()
	a.sessions[idx] = sess
	a.mu.Unlock()

	if uiAcct.Status == ui.PresenceOffline {
		// Configured offline: never dial at all - not even to fetch a live
		// roster - so nothing about this account touches the network until
		// AccountStatusSetter switches it back on.
		uiAcct.Connecting = false
		broadcast(srv, evAccountConnected, wireAccountConnectedMsg{Index: idx, Account: toWireAccount(uiAcct)})
		return
	}
	broadcast(srv, evAccountConnected, wireAccountConnectedMsg{Index: idx, Account: toWireAccount(uiAcct)})

	slog.Debug("connectAccountLive starting", "jid", acct.JID)
	start = time.Now()
	newChats, newMessages, newHistoryMore, err := connectAccountLive(ctx, sess, len(uiAcct.Chats), presenceShow(uiAcct.Status))
	if err != nil {
		slog.Debug("connectAccountLive failed", "jid", acct.JID, "elapsed", time.Since(start), "err", err)
		broadcast(srv, evAccountConnectError, wireAccountConnectErrorMsg{Index: idx, Err: err.Error()})
		go retryInitialConnect(ctx, srv, a, idx, sess, len(uiAcct.Chats), presenceShow(uiAcct.Status))
		return
	}
	slog.Debug("connectAccountLive done", "jid", acct.JID, "elapsed", time.Since(start), "new_chats", len(newChats))

	// Replay anything left in the DB-backed outbox from a previous run that
	// crashed/was killed before it got a chance to reconnect and flush -
	// otherwise a message queued while offline, then the whole app closed
	// before coming back online, would just sit in storage forever, never
	// retried, with the local UI (once restarted) having no idea it never
	// went out.
	a.flushOutbox(ctx, sess)

	broadcast(srv, evAccountLive, wireAccountLiveMsg{Index: idx, NewChats: chatsToWire(newChats), NewMessages: newMessages, NewHistoryMore: newHistoryMore})

	// Start listening (and reconnecting on drop) right away, concurrently
	// with syncArchive below - not after it. c.serve() has been reading the
	// stream since Dial regardless (SendIQ needs it to unblock), so presence/
	// messages arriving during the MAM backfill were already being queued up
	// on Client.events; running listen sequentially after syncArchive just
	// left them sitting there unprocessed (roster presence looked stuck
	// offline) until the backfill finished, sometimes tens of seconds later.
	go superviseAccount(ctx, srv, a, idx, sess)

	slog.Debug("syncArchive starting", "jid", acct.JID)
	start = time.Now()
	broadcast(srv, evHistorySyncStarted, ui.HistorySyncStartedMsg{AccountIdx: idx})
	syncArchive(ctx, srv, idx, sess)
	broadcast(srv, evHistorySyncFinished, ui.HistorySyncFinishedMsg{AccountIdx: idx})
	slog.Debug("syncArchive done", "jid", acct.JID, "elapsed", time.Since(start))
}

// retryInitialConnect keeps retrying connectAccountLive with exponential
// backoff (capped at 60s) after the very first attempt failed - e.g. the app
// started with no network yet, or the user flipped an offline account back
// online while still offline. Unlike reconnectWithBackoff, which only
// redials a client that was live before, this repeats the full first-time
// setup (publish key, OMEMO, roster diff) since none of it ran yet. Once it
// succeeds, this mirrors the rest of connectAndSuperviseAccount: broadcast
// the new chats, flush anything queued via the outbox while offline, start
// the normal supervisor, and backfill via MAM.
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
		broadcast(srv, evAccountLive, wireAccountLiveMsg{Index: idx, NewChats: chatsToWire(newChats), NewMessages: newMessages, NewHistoryMore: newHistoryMore})
		go superviseAccount(ctx, srv, a, idx, sess)
		a.flushOutbox(ctx, sess)

		broadcast(srv, evHistorySyncStarted, ui.HistorySyncStartedMsg{AccountIdx: idx})
		syncArchive(ctx, srv, idx, sess)
		broadcast(srv, evHistorySyncFinished, ui.HistorySyncFinishedMsg{AccountIdx: idx})
		return
	}
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
		hist, hasMore, _ := loadHistoryWindow(ctx, sess, r.Jid, name, nil, historyPageSize)
		slog.Debug("loadHistoryWindow done", "jid", acct.JID, "peer", r.Jid, "elapsed", time.Since(histStart), "messages", len(hist), "more", hasMore)
		// Still-queued sends left over from before the app last closed (see
		// pendingOutboxMessagesByPeer's doc comment) belong at the tail,
		// after everything already delivered - flushOutbox will actually
		// attempt them again once this account reconnects.
		hist = append(hist, pendingByPeer[r.Jid]...)
		chat := ui.Chat{Name: name, Address: r.Jid, EncryptionMode: mode, Unread: unread[r.Jid], Draft: drafts[r.Jid]}
		if len(hist) > 0 {
			messages[i] = hist
			chat.LastMessage = ui.MessagePreviewContent(hist[len(hist)-1])
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

	var tlsConfig *tls.Config // nil: Dial's default verified config; future config.yaml option could set a custom RootCAs pool here
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
		name := c.Name
		if name == "" {
			name = c.JID
		}
		prior, known := merged[c.JID]
		merged[c.JID] = rosterEntry{Name: c.Name, Subs: c.Subscription, Presence: prior.Presence, Resources: prior.Resources}
		if err := sess.db.UpsertRoster(ctx, storage.UpsertRosterParams{
			AccountJid: sess.account.JID, Jid: c.JID, Name: c.Name, Subs: c.Subscription,
		}); err != nil {
			slog.Warn("persisting roster entry", "jid", c.JID, "err", err)
		}
		if known {
			continue
		}
		idx := existingChatCount + len(newChats)
		hist, hasMore, _ := loadHistoryWindow(ctx, sess, c.JID, name, nil, historyPageSize)
		chat := ui.Chat{Name: name, Address: c.JID, Draft: drafts[c.JID], Presence: prior.Presence, Resources: prior.Resources}
		if len(hist) > 0 {
			newMessages[idx] = hist
			chat.LastMessage = ui.MessagePreviewContent(hist[len(hist)-1])
		}
		newChatIdx[c.JID] = len(newChats)
		newChats = append(newChats, chat)
		newHistoryMore[idx] = hasMore
	}
	// The roster IQ fetch above (and loadHistoryWindow's DB round-trips) can
	// take long enough for this account's own event-loop goroutine to
	// process a live PresenceEvent for one of these contacts concurrently,
	// via setRosterPresence - which updates sess.roster in place. Blindly
	// storing merged (built from a Load() taken before the fetch started)
	// would silently discard that update, leaving the contact stuck at
	// whatever stale/zero Presence merged captured until its presence
	// happens to change again. Reconcile with the latest snapshot first.
	if latest := sess.roster.Load(); latest != nil {
		for jid, entry := range *latest {
			m, ok := merged[jid]
			if !ok || (m.Presence == entry.Presence && slices.Equal(m.Resources, entry.Resources)) {
				continue
			}
			m.Presence = entry.Presence
			m.Resources = entry.Resources
			merged[jid] = m
			if idx, ok := newChatIdx[jid]; ok {
				chat := newChats[idx].(ui.Chat)
				chat.Presence = entry.Presence
				chat.Resources = entry.Resources
				newChats[idx] = chat
			}
		}
	}
	sess.roster.Store(&merged)

	resyncPeerDeviceLists(ctx, sess, merged)
	probeRosterPresence(ctx, client, merged)

	return newChats, newMessages, newHistoryMore, nil
}

// probeRosterPresence explicitly re-requests presence for every roster
// contact on connect instead of trusting the server's initial-presence-burst
// push to have reached us - see Client.ProbePresence. Best-effort: a probe
// failure just leaves that contact at its last-known (possibly stale)
// presence, same as before this existed.
func probeRosterPresence(ctx context.Context, client *xmpp.Client, roster map[string]rosterEntry) {
	for peerJID := range roster {
		if err := client.ProbePresence(ctx, peerJID); err != nil {
			slog.Debug("probing presence on connect", "peer", peerJID, "err", err)
		}
	}
}

// resyncPeerDeviceLists re-fetches every roster contact's OMEMO device list
// (both protocol versions) on every connect, rather than trusting whatever
// was last cached in storage. Manager.devicesFor only self-refreshes a
// cached list when it's completely empty - otherwise it relies solely on a
// contact's DeviceListChangedEvent (XEP-0163 PEP push) ever reaching us, and
// that push can be missed (e.g. both sides reconnecting around the same
// time). A contact who rotated devices - most commonly by wiping their own
// local database and getting a fresh device ID - would otherwise stay
// silently unreachable until something happens to empty our cache of them.
// Best-effort and run once per connect, same cost class as setupOmemo's own
// startup fetch; failures are logged, not fatal.
func resyncPeerDeviceLists(ctx context.Context, s *accountSession, roster map[string]rosterEntry) {
	for peerJID := range roster {
		if s.omemoMgrV2 != nil {
			if err := s.omemoMgrV2.SyncDevices(ctx, peerJID); err != nil {
				slog.Debug("resyncing omemo-v2 device list on connect", "peer", peerJID, "jid", s.account.JID, "err", err)
			}
		}
		if s.omemoMgrV1 != nil {
			if err := s.omemoMgrV1.SyncDevices(ctx, peerJID); err != nil {
				slog.Debug("resyncing omemo-v1 device list on connect failed", "peer", peerJID, "jid", s.account.JID, "err", err)
			} else {
				slog.Debug("resynced omemo-v1 device list on connect", "peer", peerJID, "jid", s.account.JID)
			}
		}
	}
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
func superviseAccount(ctx context.Context, srv *ipc.Server, a *adapter, accountIdx int, s *accountSession) {
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

		// Anything sent/received by us or a peer while this account was
		// disconnected never arrived over the (dead) stream, so it has to
		// be picked up from the archive - same as the initial connect does -
		// or it silently never shows up until the whole app is restarted.
		broadcast(srv, evHistorySyncStarted, ui.HistorySyncStartedMsg{AccountIdx: accountIdx})
		syncArchive(ctx, srv, accountIdx, s)
		broadcast(srv, evHistorySyncFinished, ui.HistorySyncFinishedMsg{AccountIdx: accountIdx})
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
				// setupOmemo builds s.omemoMgrV1/V2 with a Transport closure bound
				// to whatever *xmpp.Client was live at the time - and that client
				// is now dead (this is a reconnect after the previous one broke).
				// Without rebuilding them here, every OMEMO device-list/bundle
				// fetch or publish (including the resync below, and any later
				// DeviceListChangedEvent handling) silently keeps trying to write
				// to the closed session and failing, so a peer whose device list
				// changed during the outage - or was never learned because the
				// outage swallowed the one sync that would have caught it - stays
				// invisible until the app is fully restarted.
				setupOmemo(ctx, s)
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

// listen bridges one account's xmpp events into the Bubble Tea program.
// Returns when the current client's Events channel closes (client swapped
// out from under it, e.g. by a reconnect, or the client was closed).
func listen(ctx context.Context, srv *ipc.Server, accountIdx int, s *accountSession) {
	for ev := range s.client.Load().Events() {
		dispatchEvent(ctx, srv, accountIdx, s, ev)
	}
}

// dispatchEvent handles one xmpp.Event under a recover, so a bug in any one
// handler (e.g. the call/Jingle plumbing) logs a full stack trace and drops
// that one event instead of taking down the whole daemon - listen's for
// range loop has no other panic boundary, and an unrecovered panic in any
// goroutine kills the entire process.
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
		s.setRosterPresence(from, resource, presence)
		broadcast(srv, evPresence, ui.PresenceMsg{
			AccountIdx: accountIdx,
			From:       from,
			Presence:   presence,
			Resource:   resource,
		})
		if presence != ui.PresenceOffline && resource != "" {
			go s.resolveDeviceName(ctx, srv, accountIdx, from, resource)
		}
	case xmpp.SubscriptionRequestEvent:
		from := bareJID(ev.From)
		if err := s.client.Load().ApproveSubscription(ctx, from); err != nil {
			slog.Warn("approving subscription request", "from", from, "jid", s.account.JID, "err", err)
		}
	case xmpp.MessageEvent:
		handleIncomingMessage(ctx, srv, accountIdx, s, ev)
	case xmpp.JingleMessageEvent:
		s.handleJingleMessage(ctx, srv, accountIdx, ev)
	case xmpp.JingleEvent:
		s.handleJingle(ctx, srv, accountIdx, ev)
	case xmpp.ChatStateEvent:
		broadcast(srv, evTyping, ui.TypingMsg{
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
		broadcast(srv, evMessageDelivered, ui.MessageDeliveredMsg{
			AccountIdx: accountIdx,
			From:       from,
			MessageID:  ev.ID,
		})
	case xmpp.DeviceListChangedEvent:
		from := bareJID(ev.From)
		// An empty from (some servers omit the attribute on self-pushes)
		// can't be resolved to any JID at all, so there's nothing to
		// resync. But from == our own bare JID must NOT be skipped: this
		// same push fires on every connected resource of the account, not
		// just the one that just published, so another one of our own
		// clients (a second kage instance, a reinstalled Conversations,
		// ...) adding or rotating a device shows up as a self-JID push
		// here too. Skipping it left our cached self-device list stale,
		// so messages silently never got a key for that device - it
		// would never decrypt on that client, with no error anywhere.
		slog.Debug("received omemo device-list PEP push", "protocol", ev.Protocol, "from", from, "jid", s.account.JID)
		if from == "" {
			return
		}
		mgr := s.omemoMgrV2
		if ev.Protocol == omemolib.ProtocolV1 {
			mgr = s.omemoMgrV1
		}
		if mgr == nil {
			return
		}
		// SyncDevices does a network round-trip (an IQ fetch) - run it
		// off to the side rather than blocking this loop, which is the
		// same serial dispatch path every other event for this account
		// (chat messages included) goes through. A slow or hanging
		// fetch here must never stall message delivery.
		go func() {
			if err := mgr.SyncDevices(ctx, from); err != nil {
				slog.Warn("resyncing omemo device list after PEP push", "protocol", ev.Protocol, "from", from, "err", err)
				return
			}
			slog.Debug("resynced omemo device list after PEP push", "protocol", ev.Protocol, "from", from, "jid", s.account.JID)
		}()
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
func syncArchive(ctx context.Context, srv *ipc.Server, accountIdx int, s *accountSession) {
	client := s.client.Load()

	lastArchiveID := make(map[string]string)
	if cursors, err := s.db.ListMamSyncCursors(ctx, s.account.JID); err == nil {
		for _, row := range cursors {
			lastArchiveID[row.Rosterjid] = row.Archiveid
		}
	}

	entries := s.roster.Load()
	if entries == nil {
		return
	}
	slog.Debug("syncArchive: contacts to check", "jid", s.account.JID, "contacts", len(*entries))
	for peerJID := range *entries {
		start := time.Now()
		syncArchiveForContact(ctx, srv, accountIdx, s, client, peerJID, lastArchiveID[peerJID])
		slog.Debug("syncArchiveForContact done", "jid", s.account.JID, "peer", peerJID, "elapsed", time.Since(start))
	}
}

// syncArchiveForContact pages through peerJID's MAM archive strictly newer
// than afterArchiveID until the server reports the page set complete,
// persisting and forwarding each message to the UI as it's fetched.
func syncArchiveForContact(ctx context.Context, srv *ipc.Server, accountIdx int, s *accountSession, client *xmpp.Client, peerJID, afterArchiveID string) {
	const pageSize = 50
	const maxPages = 200 // guards against a misbehaving server never reporting complete

	ownBare := bareJID(s.account.JID)
	name := s.rosterName(peerJID)

	// A broken session surfaces as one failure per backfilled message from
	// the same sender device (the PreKeyMessage that exposed it, then every
	// ratchet message that followed it before we noticed) - healing on
	// every single one would fire that many redundant reset+key-transport
	// round trips for what's really one broken session. healed remembers
	// which sender devices this sync already healed, across every page of
	// this contact's backfill, so only the first failure per device
	// triggers it.
	healed := make(map[omemolib.Device]bool)

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
			// afterArchiveID itself may have stopped resolving to anything on
			// the server despite the archive genuinely holding newer messages
			// for this peer (observed live: no <item-not-found/>, just a
			// permanently empty page) - try once from the same point via a
			// <start> date filter instead of the RSM <after> id before giving
			// up on this contact. Only worth trying on a cursor we actually
			// had (empty afterArchiveID here legitimately means "archive is
			// empty", nothing to recover).
			if afterArchiveID == "" {
				break
			}
			recovered, recoveredComplete, ok := s.recoverMAMCursor(ctx, client, peerJID, afterArchiveID, pageSize)
			if !ok {
				break
			}
			items, complete = recovered, recoveredComplete
		}

		stop := false
		for _, am := range items {
			afterArchiveID = am.ArchiveID

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
			broadcast(srv, evHistorySynced, ui.HistorySyncedMsg{AccountIdx: accountIdx, From: peerJID, Messages: newMsgs})
		}
		// Advance the cursor even when nothing in this page got a messages
		// row (e.g. every item was ErrOwnDeviceKeyMissing) - otherwise a
		// permanently-undecryptable item is re-fetched and re-attempted on
		// every single future sync, forever (see mamSyncCursor's schema
		// comment).
		if afterArchiveID != prevAfter {
			if err := s.db.UpsertMamSyncCursor(ctx, storage.UpsertMamSyncCursorParams{
				AccountJid: s.account.JID,
				RosterJid:  peerJID,
				ArchiveID:  afterArchiveID,
			}); err != nil {
				slog.Warn("persisting mam sync cursor", "peer", peerJID, "err", err)
			}
		}
		if stop || complete || afterArchiveID == prevAfter {
			break
		}
	}
}

// recoverMAMCursor re-fetches from the exact point afterArchiveID
// represents, using an XEP-0313 <start> date filter instead of RSM <after>,
// for when the normal <after> query above came back empty despite the
// server having accepted the id (no <item-not-found/> — see FetchArchive's
// caller). start is set to the exact SentAt of the message afterArchiveID
// points to (looked up locally, since we stored it when that message was
// first synced), so this covers exactly the range the broken <after> query
// was supposed to and can't skip anything between the old cursor and now.
// ok is false if there's nothing to recover (no local record of
// afterArchiveID's timestamp, the request failed, or it came back empty too
// - meaning the original empty page was legitimate after all).
func (s *accountSession) recoverMAMCursor(ctx context.Context, client *xmpp.Client, peerJID, afterArchiveID string, max uint64) (items []xmpp.ArchivedMessage, complete bool, ok bool) {
	delay, err := s.db.GetMessageDelayByArchiveID(ctx, storage.GetMessageDelayByArchiveIDParams{
		AccountJid: s.account.JID,
		ArchiveID:  sql.NullString{String: afterArchiveID, Valid: true},
	})
	if err != nil {
		return nil, false, false
	}

	pageCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	since := time.Unix(delay, 0)
	items, complete, err = client.FetchArchiveSince(pageCtx, peerJID, since, max)
	slog.Debug("mam start-date fallback fetched", "jid", s.account.JID, "peer", peerJID, "since", since, "items", len(items), "complete", complete, "err", err)
	if err != nil || len(items) == 0 {
		return nil, false, false
	}
	return items, complete, true
}

// mamItemOutcome reports what syncArchiveForContact's caller should do with
// one processed archive item: append msg to the page's batch of new
// messages, and/or stop paging (stopSync, on an archiveID conflict meaning
// this item was already stored by an earlier run).
type mamItemOutcome struct {
	msg      *ui.Message
	stopSync bool
}

// processMAMItem decrypts, persists, and reports the outcome for a single
// MAM archive item. Held under s.omemoMu for its whole body — see that
// field's doc comment — so it can't decrypt the same OMEMO ciphertext
// concurrently with the live path in handleIncomingMessage (events.go).
func (s *accountSession) processMAMItem(ctx context.Context, srv *ipc.Server, accountIdx int, peerJID, ownBare, name string, am xmpp.ArchivedMessage, healed map[omemolib.Device]bool) mamItemOutcome {
	s.omemoMu.Lock()
	defer s.omemoMu.Unlock()

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
		return mamItemOutcome{}
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
			return mamItemOutcome{}
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
		broadcast(srv, evMessageRetracted, ui.MessageRetractedMsg{AccountIdx: accountIdx, From: peerJID, RetractID: am.RetractID})
		return mamItemOutcome{}
	}
	if am.ReactionTargetID != "" {
		if err := replaceReactions(ctx, s, peerJID, am.ReactionTargetID, bareJID(am.From), am.Reactions); err != nil {
			slog.Warn("persisting mam reactions", "peer", peerJID, "err", err)
		}
		broadcast(srv, evMessageReactions, ui.MessageReactionsMsg{
			AccountIdx: accountIdx,
			From:       peerJID,
			MessageID:  am.ReactionTargetID,
			Reactions:  loadReactionsForMessage(ctx, s, peerJID, am.ReactionTargetID),
		})
		return mamItemOutcome{}
	}

	// Belt-and-suspenders alongside dispatchArchiveResult's own
	// filtering (xmpp/mam.go) - a plain message with neither body nor
	// an OMEMO payload has nothing to show, matching
	// handleIncomingMessage's live-path guard (events.go).
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
			return mamItemOutcome{}
		} else if err != nil {
			body = "[message could not be decrypted: " + err.Error() + "]"
			decryptFailed = true
			if (errors.Is(err, omemolib.ErrUnknownSession) || errors.Is(err, omemolib.ErrPreKeyNotFound)) && !healed[enc.Sender] {
				// Same healing as handleIncomingMessage's live path
				// (events.go) - and just as needed here, arguably more so:
				// this is exactly the path a peer's messages take while we
				// were offline, so without this the broken session would
				// otherwise only ever get fixed by chance, on whatever the
				// next *live* failure from the same sender happens to be.
				// A whole backfill batch from one broken sender device
				// fails the same way message after message though - healed
				// caps it at one reset+key-transport round trip per device
				// per sync instead of one per failed message.
				healed[enc.Sender] = true
				healBrokenSession(ctx, s, mgr, enc.Sender, bareJID(am.From))
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
	// MAM doesn't currently surface the <reply/> element (see
	// ArchivedMessage), so unlike handleIncomingMessage this can't
	// gate on ReplyToID - stripReplyQuote is a no-op on a body that
	// doesn't start with a quote block, so applying it unconditionally
	// is safe and still fixes attachment identification for
	// backfilled replies.
	body = stripReplyQuote(body)
	if len(oobURLs) == 0 {
		oobURLs = aesgcmURLsInBody(body)
	}
	sent := bareJID(am.From) == ownBare
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
		}); err != nil {
			slog.Warn("persisting mam correction", "peer", peerJID, "err", err)
		}
		broadcast(srv, evMessageCorrected, ui.MessageCorrectedMsg{
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
		OobUrls:      joinOOBURLs(oobURLs),
	})
	if err != nil {
		if strings.Contains(err.Error(), "archiveID") {
			// Already stored, and results arrive chronologically —
			// everything after it is too.
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
	}}
}

// bareJID strips the resource part (after "/") from a full JID, matching the
// bare-JID form roster entries and stored messages are keyed by.
func bareJID(addr string) string {
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		return addr[:i]
	}
	return addr
}

// resourcePart returns the resource part (after "/") of a full JID, or ""
// if addr has none.
func resourcePart(addr string) string {
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		return addr[i+1:]
	}
	return ""
}
