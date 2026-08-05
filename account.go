package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
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
	account   config.Account
	client    atomic.Pointer[xmpp.Client]
	tlsConfig *tls.Config      // reused on reconnect; nil means Dial's default verified config
	db        *storage.Queries // shared across every account: one database, rows scoped by account.JID
	gpg       gpg.Encrypter
	useGPG    bool // mirrors config.UseGPG; gates gpg encrypt/decrypt on incoming/outgoing messages
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
	debugf("account %s: connectAccountLocal starting", acct.JID)
	start := time.Now()
	sess, uiAcct, err := connectAccountLocal(ctx, acct, queries, localKey)
	if err != nil {
		debugf("account %s: connectAccountLocal failed after %s: %v", acct.JID, time.Since(start), err)
		p.Send(ui.AccountConnectErrorMsg{Index: idx, Err: err})
		return
	}
	sess.useGPG = a.useGPG
	debugf("account %s: connectAccountLocal done in %s (%d chats)", acct.JID, time.Since(start), len(uiAcct.Chats))
	p.Send(ui.AccountConnectedMsg{Index: idx, Account: uiAcct})

	debugf("account %s: connectAccountLive starting", acct.JID)
	start = time.Now()
	newChats, newMessages, newHistoryMore, err := connectAccountLive(ctx, sess, len(uiAcct.Chats))
	if err != nil {
		debugf("account %s: connectAccountLive failed after %s: %v", acct.JID, time.Since(start), err)
		p.Send(ui.AccountConnectErrorMsg{Index: idx, Err: err})
		return
	}
	debugf("account %s: connectAccountLive done in %s (%d new chats)", acct.JID, time.Since(start), len(newChats))

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

	debugf("account %s: syncArchive starting", acct.JID)
	start = time.Now()
	p.Send(ui.HistorySyncStartedMsg{AccountIdx: idx})
	syncArchive(ctx, p, idx, sess)
	p.Send(ui.HistorySyncFinishedMsg{AccountIdx: idx})
	debugf("account %s: syncArchive done in %s", acct.JID, time.Since(start))
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
	debugf("account %s: local roster has %d contacts", acct.JID, len(rows))

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
		chats = append(chats, ui.Chat{Name: name, Address: r.Jid, EncryptionMode: mode})
		histStart := time.Now()
		hist, hasMore := loadHistoryPage(ctx, sess, r.Jid, name)
		debugf("account %s: loadHistoryPage(%s) done in %s (%d messages, more=%t)", acct.JID, r.Jid, time.Since(histStart), len(hist), hasMore)
		if len(hist) > 0 {
			messages[i] = hist
		}
		historyMore[i] = hasMore
	}
	sess.roster.Store(&entries)

	return sess, ui.Account{Name: acct.JID, Chats: chats, Messages: messages, HistoryMore: historyMore, Connecting: true}, nil
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
func connectAccountLive(ctx context.Context, sess *accountSession, existingChatCount int) ([]list.Item, map[int][]ui.Message, map[int]bool, error) {
	debugf("account %s: resolving password", sess.account.JID)
	start := time.Now()
	password, err := sess.account.ResolvePassword()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("account %s: %w", sess.account.JID, err)
	}
	debugf("account %s: password resolved in %s", sess.account.JID, time.Since(start))

	var tlsConfig *tls.Config // nil: Dial's default verified config; future config.toml option could set a custom RootCAs pool here
	debugf("account %s: dialing", sess.account.JID)
	start = time.Now()
	client, err := xmpp.Dial(ctx, sess.account.JID, password, tlsConfig)
	if err != nil {
		debugf("account %s: dial failed after %s: %v", sess.account.JID, time.Since(start), err)
		return nil, nil, nil, fmt.Errorf("account %s: %w", sess.account.JID, err)
	}
	debugf("account %s: dialed in %s", sess.account.JID, time.Since(start))
	sess.tlsConfig = tlsConfig
	client.Debugf = debugf
	sess.client.Store(client)

	if sess.useGPG && sess.account.GPGKeyID != "" {
		debugf("account %s: publishOwnGPGKey starting", sess.account.JID)
		start = time.Now()
		publishOwnGPGKey(ctx, sess)
		debugf("account %s: publishOwnGPGKey done in %s", sess.account.JID, time.Since(start))
	}

	debugf("account %s: setupOmemo starting", sess.account.JID)
	start = time.Now()
	setupOmemo(ctx, sess)
	debugf("account %s: setupOmemo done in %s", sess.account.JID, time.Since(start))

	debugf("account %s: fetching live roster", sess.account.JID)
	start = time.Now()
	contacts, err := client.Roster(ctx)
	if err != nil {
		debugf("account %s: roster fetch failed after %s: %v", sess.account.JID, time.Since(start), err)
		client.Close()
		return nil, nil, nil, fmt.Errorf("account %s: fetching roster: %w", sess.account.JID, err)
	}
	debugf("account %s: live roster fetched in %s (%d contacts)", sess.account.JID, time.Since(start), len(contacts))

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
			debugf("warning: persisting roster entry %s: %v\n", c.JID, err)
		}
		if known {
			continue
		}
		idx := existingChatCount + len(newChats)
		newChats = append(newChats, ui.Chat{Name: name, Address: c.JID})
		hist, hasMore := loadHistoryPage(ctx, sess, c.JID, name)
		if len(hist) > 0 {
			newMessages[idx] = hist
		}
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
func connectAccount(ctx context.Context, acct config.Account, queries *storage.Queries, localKey []byte, useGPG bool) (*accountSession, ui.Account, error) {
	sess, uiAcct, err := connectAccountLocal(ctx, acct, queries, localKey)
	if err != nil {
		return nil, ui.Account{}, err
	}
	sess.useGPG = useGPG

	newChats, newMessages, newHistoryMore, err := connectAccountLive(ctx, sess, len(uiAcct.Chats))
	if err != nil {
		return nil, ui.Account{}, err
	}

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
		debugf("warning: account %s disconnected (%v); reconnecting...\n", s.account.JID, client.Err())
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

		password, err := s.account.ResolvePassword()
		if err == nil {
			var client *xmpp.Client
			client, err = xmpp.Dial(ctx, s.account.JID, password, s.tlsConfig)
			if err == nil {
				s.client.Store(client)
				debugf("account %s reconnected\n", s.account.JID)
				return
			}
		}

		debugf("warning: reconnecting %s failed: %v; retrying in %s\n", s.account.JID, err, backoff)
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
				debugf("warning: persisting delivery receipt: %v\n", err)
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
					debugf("warning: resyncing omemo(%s) device list for %s after PEP push: %v\n", ev.Protocol, from, err)
				}
			}()
			continue
		}
	}
}

// mapPresence reduces an xmpp.PresenceEvent to the UI's coarse online/away/
// offline distinction.
func mapPresence(ev xmpp.PresenceEvent) ui.Presence {
	if !ev.Available {
		return ui.PresenceOffline
	}
	switch ev.Show {
	case "away", "xa", "dnd":
		return ui.PresenceAway
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
	debugf("account %s: syncArchive: %d contacts to check", s.account.JID, len(*entries))
	for peerJID := range *entries {
		start := time.Now()
		syncArchiveForContact(ctx, p, accountIdx, s, client, peerJID, lastArchiveID[peerJID])
		debugf("account %s: syncArchiveForContact(%s) done in %s", s.account.JID, peerJID, time.Since(start))
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
		debugf("mam %s/%s: page %d fetched in %s (after=%q items=%d complete=%v err=%v)",
			s.account.JID, peerJID, page, time.Since(pageStart), prevAfter, len(items), complete, err)
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
					debugf("warning: persisting mam retraction for %s: %v", peerJID, err)
				}
				p.Send(ui.MessageRetractedMsg{AccountIdx: accountIdx, From: peerJID, RetractID: am.RetractID})
				continue
			}
			if am.ReactionTargetID != "" {
				if err := replaceReactions(ctx, s, peerJID, am.ReactionTargetID, bareJID(am.From), am.Reactions); err != nil {
					debugf("warning: persisting mam reactions for %s: %v", peerJID, err)
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
				} else if decodeErr != nil {
					body = "[message could not be decrypted: " + decodeErr.Error() + "]"
				} else if pt, err := mgr.DecryptMessage(ctx, enc); errors.Is(err, omemolib.ErrOwnDeviceKeyMissing) {
					// Not a real failure - see handleIncomingMessage's live-path
					// comment. Quietly skip instead of storing a noise row.
					debugf("mam: omemo message %s from %s has no key for this device, skipping", am.ArchiveID, am.From)
					continue
				} else if err != nil {
					body = "[message could not be decrypted: " + err.Error() + "]"
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
					debugf("warning: persisting mam correction for %s: %v", peerJID, err)
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
				debugf("warning: persisting mam history for %s: %v", peerJID, err)
				continue
			}

			author := name
			if sent {
				author = "me"
			}
			newMsgs = append(newMsgs, ui.Message{
				ID:          am.ID,
				Author:      author,
				Content:     body,
				SentAt:      am.SentAt,
				IsMe:        sent,
				Encrypted:   e2eEncrypted,
				EncMethod:   e2eeMethod,
				Attachments: attachmentURLs(body),
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
