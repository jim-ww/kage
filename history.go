package main

import (
	"context"
	"log/slog"
	"math"
	"slices"
	"time"

	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
)

// meReactorJID is the fromJID value used for our own account's rows in the
// messageReactions table — reactions are always local to one account's
// storage, so there's no ambiguity in using a fixed sentinel rather than the
// account's real JID.
const meReactorJID = "me"

// readStoredBody returns row's plaintext body, decrypting it if row.Encrypted
// is set — that fails if no local storage password is configured. A
// plaintext row read while a password *is* now available gets
// opportunistically re-sealed and written back, so it only sits unencrypted
// until the next time it's read.
func readStoredBody(ctx context.Context, s *accountSession, chatAddr string, row storage.Message) (string, error) {
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
				Sent:         row.Sent,
			}); err != nil {
				slog.Warn("encrypting stored message", "err", err)
			}
		}
		return row.Body.String, nil
	}
	return openBody(s.localKey, row.Body.String, true)
}

// buildMessages decrypts and converts rows (already in chronological, oldest
// first, order) into ui.Message. Rows with no body (encryption failed at
// write time) are skipped since there is nothing to recover.
func buildMessages(ctx context.Context, s *accountSession, chatAddr, chatName string, rows []storage.Message) []ui.Message {
	msgs := make([]ui.Message, 0, len(rows))
	replyTo := make([]string, 0, len(rows)) // parallel to msgs: each entry's ReplyToIdAttr
	for _, row := range rows {
		if row.Stanzatype == "call" {
			msgs = append(msgs, ui.Message{
				ID:      row.Idattr.String,
				StoreID: row.ID,
				Author:  chatName,
				SentAt:  time.Unix(row.Delay, 0),
				IsMe:    row.Sent,
				CallLog: &ui.CallLogInfo{
					Direction: row.Calldirection.String,
					Outcome:   row.Calloutcome.String,
					Duration:  time.Duration(row.Calldurationsecs.Int64) * time.Second,
				},
			})
			replyTo = append(replyTo, "")
			continue
		}
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
			ID:            row.Idattr.String,
			StoreID:       row.ID,
			Author:        author,
			Content:       pt,
			SentAt:        time.Unix(row.Delay, 0),
			IsMe:          row.Sent,
			Retracted:     row.Retracted,
			Edited:        row.Edited,
			Delivered:     row.Delivered,
			ServerAcked:   row.Serveracked,
			Failed:        row.Sendfailed,
			Encrypted:     row.E2eencrypted,
			EncMethod:     row.E2eemethod.String,
			Reactions:     loadReactionsForMessage(ctx, s, chatAddr, row.Idattr.String),
			Attachments:   attachments,
			QuotedAuthor:  row.Replyquoteauthor.String,
			QuotedPreview: row.Replyquotepreview.String,
		})
		replyTo = append(replyTo, row.Replytoidattr.String)
	}

	for i, id := range replyTo {
		msgs[i].ReplyToID = id
	}

	// Resolve the quote snapshot for replies that don't carry one: every row
	// persisted before replyQuoteAuthor/replyQuotePreview existed, plus any
	// whose target couldn't be resolved back when it was written. Skipped
	// when the target is in this window anyway - the index lookup in
	// ui/render_message.go already has everything it needs then. Bounded by
	// the number of unresolved replies in the window and done once per load
	// rather than per render, so a chat whose history is far larger than one
	// window still only pays for what it actually shows.
	for i := range msgs {
		if msgs[i].ReplyToID == "" || msgs[i].QuotedPreview != "" {
			continue
		}
		if messageIndexByIDs(msgs, msgs[i].ReplyToID) >= 0 {
			continue
		}
		msgs[i].QuotedAuthor, msgs[i].QuotedPreview = resolveReplyQuote(ctx, s, chatAddr, chatName, msgs[i].ReplyToID)
	}
	return msgs
}

// loadHistory reads chatAddr's entire persisted history back from storage,
// decrypting each body with the local storage key (crypto/localstore). Used
// where the full history is genuinely needed (tests, export, and search —
// SearchHistory has to scan every message's content); interactive chat
// loading uses the windowed loadHistoryWindow instead so very long
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

	return buildMessages(ctx, s, chatAddr, chatName, rows)
}

// historyPageSize messages are fetched at a time by loadHistoryWindow; set
// from config.Config.HistoryPageSize by main before any account connects.
var historyPageSize = 200

// loadHistoryWindow loads a fresh window of chatAddr's history: up to limit
// messages, entirely replacing whatever the caller had loaded before (see
// ui.HistoryWindowMsg's doc comment) — there is no merging or pagination
// state carried between calls, every call is self-contained.
//
// anchor == nil returns the chat's most recent (live tail) window —
// hasNewer is always false in that case, there's nothing newer than latest.
// Otherwise the window is built around anchor: up to half strictly older
// than it, half at-or-newer, so anchor's own message always ends up inside
// the returned window (used to restore the selection after the load — see
// Model.pendingWindowAnchor). hasOlder/hasNewer are a cheap heuristic (did
// that half come back full) rather than an extra existence check — normal
// keyset-pagination practice, worst case a later fetch just comes back
// empty.
func loadHistoryWindow(ctx context.Context, s *accountSession, chatAddr, chatName string, anchor *ui.HistoryAnchor, limit int) (msgs []ui.Message, hasOlder, hasNewer bool) {
	if anchor == nil {
		rows, err := s.db.ListMessagesByRosterBefore(ctx, storage.ListMessagesByRosterBeforeParams{
			AccountJid: s.account.JID, RosterJid: nullString(chatAddr),
			BeforeDelay: math.MaxInt64, BeforeID: math.MaxInt64, PageLimit: int64(limit),
		})
		if err != nil {
			slog.Warn("loading history window", "chat", chatAddr, "err", err)
			return nil, false, false
		}
		slices.Reverse(rows) // rows arrive newest-first
		return buildMessages(ctx, s, chatAddr, chatName, rows), len(rows) == limit, false
	}

	newerHalf := max(1, limit/2) // >=1 so the anchor itself (always matched by AtOrAfter) has a slot
	olderHalf := max(0, limit-newerHalf)

	olderRows, err := s.db.ListMessagesByRosterBefore(ctx, storage.ListMessagesByRosterBeforeParams{
		AccountJid: s.account.JID, RosterJid: nullString(chatAddr),
		BeforeDelay: anchor.Delay, BeforeID: anchor.StoreID, PageLimit: int64(olderHalf),
	})
	if err != nil {
		slog.Warn("loading history window (older half)", "chat", chatAddr, "err", err)
		return nil, false, false
	}
	newerRows, err := s.db.ListMessagesByRosterAtOrAfter(ctx, storage.ListMessagesByRosterAtOrAfterParams{
		AccountJid: s.account.JID, RosterJid: nullString(chatAddr),
		AfterDelay: anchor.Delay, AfterID: anchor.StoreID, PageLimit: int64(newerHalf),
	})
	if err != nil {
		slog.Warn("loading history window (newer half)", "chat", chatAddr, "err", err)
		return nil, false, false
	}

	slices.Reverse(olderRows) // olderRows arrive newest-first, newerRows oldest-first
	rows := slices.Concat(olderRows, newerRows)
	return buildMessages(ctx, s, chatAddr, chatName, rows), len(olderRows) == olderHalf && olderHalf > 0, len(newerRows) == newerHalf
}

// resolveReplyQuote looks up the message replyToID refers to in chatAddr's
// local history and returns a snapshot of its author ("me", matching
// Message.Author's own convention, or chatName) and preview text, to persist
// alongside a new reply so its indicator still has something to show even if
// the target later falls outside whatever window of history the UI has
// currently loaded (see QuotedAuthor/QuotedPreview's doc comment). Only
// needed on the receiving side (live or MAM) — the sender already has the
// target in its own open UI state and passes its own snapshot through
// SendOptions.QuotedAuthor/QuotedBody instead. Best-effort: any miss
// (message not in our storage, decrypt failure) returns ("", ""), same as a
// reply to a message we never learned about at all.
func resolveReplyQuote(ctx context.Context, s *accountSession, chatAddr, chatName, replyToID string) (author, preview string) {
	if replyToID == "" {
		return "", ""
	}
	row, err := s.db.GetMessageSnippetByIDAttr(ctx, storage.GetMessageSnippetByIDAttrParams{
		AccountJid: s.account.JID,
		RosterJid:  nullString(chatAddr),
		IDAttr:     nullString(replyToID),
	})
	if err != nil {
		return "", ""
	}
	pt, err := readStoredBody(ctx, s, chatAddr, storage.Message{
		ID: row.ID, Sent: row.Sent, Idattr: row.Idattr, Body: row.Body, Encrypted: row.Encrypted,
	})
	if err != nil {
		return "", ""
	}
	pt = stripReplyQuote(pt)
	author = chatName
	if row.Sent {
		author = "me"
	}
	return author, ui.MessagePreviewContent(ui.Message{Content: pt, IsMe: row.Sent, Author: author})
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
