package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/jim-ww/kage/crypto/localstore"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
)

// meReactorJID is the fromJID value used for our own account's rows in the
// messageReactions table — reactions are always local to one account's
// storage, so there's no ambiguity in using a fixed sentinel rather than the
// account's real JID.
const meReactorJID = "me"

// historyRow is the subset of a messages row loadHistory/loadHistoryPage
// need, shared between ListMessagesByRoster's and ListMessagesByRosterBefore's
// (differently-shaped, sqlc-generated) row types so the decrypt/build logic
// below isn't duplicated per query.
type historyRow struct {
	ID            int64
	Sent          bool
	Idattr        sql.NullString
	Body          sql.NullString
	Encrypted     bool
	E2eencrypted  bool
	E2eemethod    sql.NullString
	Delay         int64
	Replytoidattr sql.NullString
	Retracted     bool
	Edited        bool
	Delivered     bool
	Ooburls       sql.NullString
}

// readStoredBody returns row's plaintext body, decrypting it if row.Encrypted
// is set — that fails if no local storage password is configured. A
// plaintext row read while a password *is* now available gets
// opportunistically re-sealed and written back, so it only sits unencrypted
// until the next time it's read.
func readStoredBody(ctx context.Context, s *accountSession, chatAddr string, row historyRow) (string, error) {
	if !row.Encrypted {
		if s.localKey != nil {
			sealedBody, encrypted := encryptForStorage(s, row.Body.String)
			if _, err := s.db.UpdateMessageBodyByID(ctx, storage.UpdateMessageBodyByIDParams{
				AccountJid:   s.account.JID,
				Body:         sealedBody,
				Encrypted:    encrypted,
				E2eEncrypted: row.E2eencrypted,
				E2eeMethod:   row.E2eemethod,
				Edited:       row.Edited,
				IDAttr:       row.Idattr,
				RosterJid:    nullString(chatAddr),
			}); err != nil {
				slog.Warn("encrypting stored message", "err", err)
			}
		}
		return row.Body.String, nil
	}
	if s.localKey == nil {
		return "", fmt.Errorf("message is encrypted but no local storage password is available")
	}
	return localstore.Open(s.localKey, row.Body.String)
}

// buildMessages decrypts and converts rows (already in chronological, oldest
// first, order) into ui.Message, resolving reply targets to indices within
// the returned slice. Rows with no body (encryption failed at write time)
// are skipped since there is nothing to recover.
func buildMessages(ctx context.Context, s *accountSession, chatAddr, chatName string, rows []historyRow) []ui.Message {
	msgs := make([]ui.Message, 0, len(rows))
	replyTo := make([]string, 0, len(rows)) // parallel to msgs: each entry's ReplyToIdAttr, resolved to an index below
	for _, row := range rows {
		if !row.Body.Valid {
			continue
		}
		pt, err := readStoredBody(ctx, s, chatAddr, row)
		if err != nil {
			slog.Warn("decrypting history", "chat", chatAddr, "err", err)
			continue
		}
		// Rows persisted before the in-band reply quote was stripped at
		// receive time (see handleIncomingMessage) still carry a leading
		// "> ..." block in their stored body - strip it here too so old
		// history doesn't show mangled multi-line text where a file
		// attachment (or a plain reply) should render normally. Applied
		// unconditionally (not gated on Replytoidattr) since some peers'
		// in-band quotes don't round-trip through our <reply/> parsing, and
		// stripReplyQuote is a no-op on a body with no leading quote block.
		pt = stripReplyQuote(pt)
		author := chatName
		if row.Sent {
			author = "me"
		}
		// Rows persisted before the oobURLs column existed (or before a
		// given message was re-synced since) have it empty even for a real
		// aesgcm:// attachment - fall back to recognizing the scheme
		// directly from the stored body, same as the live/MAM paths.
		attachments := splitOOBURLs(row.Ooburls)
		if len(attachments) == 0 {
			attachments = aesgcmURLsInBody(pt)
		}
		msgs = append(msgs, ui.Message{
			ID:          row.Idattr.String,
			Author:      author,
			Content:     pt,
			SentAt:      time.Unix(row.Delay, 0),
			IsMe:        row.Sent,
			Retracted:   row.Retracted,
			Edited:      row.Edited,
			Delivered:   row.Delivered,
			Encrypted:   row.E2eencrypted,
			EncMethod:   row.E2eemethod.String,
			Reactions:   loadReactionsForMessage(ctx, s, chatAddr, row.Idattr.String),
			Attachments: attachments,
		})
		replyTo = append(replyTo, row.Replytoidattr.String)
	}

	// Resolve stored reply-target IDs into local indices now that the whole
	// slice (and thus every message's position) is known.
	for i, id := range replyTo {
		if idx := messageIndexByIDs(msgs, id); idx >= 0 {
			msgs[i].ReplyTo = &idx
		}
	}
	return msgs
}

// loadHistory reads chatAddr's entire persisted history back from storage,
// decrypting each body with the local storage key (crypto/localstore). Used
// where the full history is genuinely needed (tests, export); interactive
// chat loading uses the paginated loadHistoryPage instead so very long
// histories don't get decrypted/rendered all at once.
func loadHistory(ctx context.Context, s *accountSession, chatAddr, chatName string) []ui.Message {
	rows, err := s.db.ListMessagesByRoster(ctx, storage.ListMessagesByRosterParams{
		AccountJid: s.account.JID,
		RosterJid:  nullString(chatAddr),
	})
	if err != nil {
		slog.Warn("loading history", "chat", chatAddr, "err", err)
		return nil
	}

	hrows := make([]historyRow, len(rows))
	for i, r := range rows {
		hrows[i] = historyRow{
			Sent: r.Sent, Idattr: r.Idattr, Body: r.Body, Encrypted: r.Encrypted,
			E2eencrypted: r.E2eencrypted, E2eemethod: r.E2eemethod, Delay: r.Delay, Replytoidattr: r.Replytoidattr, Retracted: r.Retracted,
			Edited: r.Edited, Delivered: r.Delivered, Ooburls: r.Ooburls,
		}
	}
	return buildMessages(ctx, s, chatAddr, chatName, hrows)
}

// historyPageSize messages are fetched at a time by loadHistoryPage; set
// from config.Config.HistoryPageSize by main before any account connects.
var historyPageSize = 200

// historyCursor is a chat's "load older" keyset pagination position: the
// (delay, id) of the oldest message loaded so far. The zero value means
// "nothing loaded yet" and fetches the most recent page.
type historyCursor struct {
	delay, id int64
	exhausted bool // true once a page came back shorter than requested — no older rows remain
}

// loadHistoryPage loads the next (older) page of chatAddr's history,
// starting from s's cursor for chatAddr (or the most recent page, if none is
// set yet), and advances that cursor past what it returned. Returned
// messages are in chronological order, ready to prepend to whatever's
// already loaded. hasMore is false once the oldest stored message has been
// reached.
func loadHistoryPage(ctx context.Context, s *accountSession, chatAddr, chatName string) (msgs []ui.Message, hasMore bool) {
	s.histMu.Lock()
	cur, ok := s.histPos[chatAddr]
	s.histMu.Unlock()
	if ok && cur.exhausted {
		return nil, false
	}

	beforeDelay, beforeID := int64(math.MaxInt64), int64(math.MaxInt64)
	if ok {
		beforeDelay, beforeID = cur.delay, cur.id
	}

	rows, err := s.db.ListMessagesByRosterBefore(ctx, storage.ListMessagesByRosterBeforeParams{
		AccountJid:  s.account.JID,
		RosterJid:   nullString(chatAddr),
		BeforeDelay: beforeDelay,
		BeforeID:    beforeID,
		PageLimit:   int64(historyPageSize),
	})
	if err != nil {
		slog.Warn("loading history page", "chat", chatAddr, "err", err)
		return nil, false
	}

	next := historyCursor{exhausted: len(rows) < historyPageSize}
	// rows arrive newest-first; reverse into chronological order and build
	// historyRow in the same pass.
	hrows := make([]historyRow, len(rows))
	for i, r := range rows {
		hrows[len(rows)-1-i] = historyRow{
			ID: r.ID, Sent: r.Sent, Idattr: r.Idattr, Body: r.Body, Encrypted: r.Encrypted,
			E2eencrypted: r.E2eencrypted, E2eemethod: r.E2eemethod, Delay: r.Delay, Replytoidattr: r.Replytoidattr, Retracted: r.Retracted,
			Edited: r.Edited, Delivered: r.Delivered, Ooburls: r.Ooburls,
		}
	}
	if len(rows) > 0 {
		oldest := rows[len(rows)-1]
		next.delay, next.id = oldest.Delay, oldest.ID
	} else if ok {
		next.delay, next.id = cur.delay, cur.id
	}

	s.histMu.Lock()
	if s.histPos == nil {
		s.histPos = make(map[string]historyCursor)
	}
	s.histPos[chatAddr] = next
	s.histMu.Unlock()

	return buildMessages(ctx, s, chatAddr, chatName, hrows), !next.exhausted
}

// replaceReactions fully replaces reactorJID's reaction set on msgID (scoped
// to chatAddr, the conversation the target message belongs to - a stanza id
// is only unique within one conversation) with emojis, matching XEP-0444
// semantics (a new reaction stanza always replaces the sender's previous
// set, never adds to it).
func replaceReactions(ctx context.Context, s *accountSession, chatAddr, msgID, reactorJID string, emojis []string) error {
	if err := s.db.DeleteReactionsByReactor(ctx, storage.DeleteReactionsByReactorParams{
		AccountJid: s.account.JID, RosterJid: chatAddr, IDAttr: msgID, FromJid: reactorJID,
	}); err != nil {
		return err
	}
	for _, e := range emojis {
		if err := s.db.InsertReaction(ctx, storage.InsertReactionParams{
			AccountJid: s.account.JID, RosterJid: chatAddr, IDAttr: msgID, FromJid: reactorJID, Emoji: e,
		}); err != nil {
			return err
		}
	}
	return nil
}

// loadReactionsForMessage aggregates all reactors' current sets on msgID
// (within chatAddr) into per-emoji counts, flagging whether our own account
// is among the reactors.
func loadReactionsForMessage(ctx context.Context, s *accountSession, chatAddr, msgID string) []ui.Reaction {
	rows, err := s.db.ListReactionsForMessage(ctx, storage.ListReactionsForMessageParams{
		AccountJid: s.account.JID, RosterJid: chatAddr, IDAttr: msgID,
	})
	if err != nil {
		slog.Warn("loading reactions", "message_id", msgID, "err", err)
		return nil
	}

	order := make([]string, 0, len(rows))
	counts := make(map[string]int, len(rows))
	mine := make(map[string]bool, len(rows))
	for _, row := range rows {
		if counts[row.Emoji] == 0 {
			order = append(order, row.Emoji)
		}
		counts[row.Emoji]++
		if row.Fromjid == meReactorJID {
			mine[row.Emoji] = true
		}
	}

	reactions := make([]ui.Reaction, len(order))
	for i, emoji := range order {
		reactions[i] = ui.Reaction{Emoji: emoji, Count: counts[emoji], Mine: mine[emoji]}
	}
	return reactions
}

// messageIndexByIDs returns the index of the message with the given stanza
// ID within msgs, or -1 if none matches (or id is empty). Same idea as
// ui.messageIndexByID, duplicated here since it's an unexported ui helper.
func messageIndexByIDs(msgs []ui.Message, id string) int {
	if id == "" {
		return -1
	}
	for i, msg := range msgs {
		if msg.ID == id {
			return i
		}
	}
	return -1
}
