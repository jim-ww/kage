package main

import (
	"context"
	"fmt"
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

// readStoredBody returns row's plaintext body, decrypting it if row.Encrypted
// is set — that fails if no local storage password is configured. A
// plaintext row read while a password *is* now available gets
// opportunistically re-sealed and written back, so it only sits unencrypted
// until the next time it's read.
func readStoredBody(ctx context.Context, s *accountSession, chatAddr string, row storage.ListMessagesByRosterRow) (string, error) {
	if !row.Encrypted {
		if s.localKey != nil {
			sealedBody, encrypted := encryptForStorage(s, row.Body.String)
			if _, err := s.db.UpdateMessageBodyByID(ctx, storage.UpdateMessageBodyByIDParams{
				AccountJid: s.account.JID,
				Body:       sealedBody,
				Encrypted:  encrypted,
				IDAttr:     row.Idattr,
				RosterJid:  nullString(chatAddr),
			}); err != nil {
				debugf("warning: encrypting stored message: %v\n", err)
			}
		}
		return row.Body.String, nil
	}
	if s.localKey == nil {
		return "", fmt.Errorf("message is encrypted but no local storage password is available")
	}
	return localstore.Open(s.localKey, row.Body.String)
}

// loadHistory reads chatAddr's persisted history back from storage,
// decrypting each body with the local storage key (crypto/localstore).
// Rows with no body (encryption failed at write time) are skipped since
// there is nothing to recover.
func loadHistory(ctx context.Context, s *accountSession, chatAddr, chatName string) []ui.Message {
	rows, err := s.db.ListMessagesByRoster(ctx, storage.ListMessagesByRosterParams{
		AccountJid: s.account.JID,
		RosterJid:  nullString(chatAddr),
	})
	if err != nil {
		debugf("warning: loading history for %s: %v\n", chatAddr, err)
		return nil
	}

	msgs := make([]ui.Message, 0, len(rows))
	replyTo := make([]string, 0, len(rows)) // parallel to msgs: each entry's ReplyToIdAttr, resolved to an index below
	for _, row := range rows {
		if !row.Body.Valid {
			continue
		}
		pt, err := readStoredBody(ctx, s, chatAddr, row)
		if err != nil {
			debugf("warning: decrypting history for %s: %v\n", chatAddr, err)
			continue
		}
		author := chatName
		if row.Sent {
			author = "me"
		}
		msgs = append(msgs, ui.Message{
			ID:          row.Idattr.String,
			Author:      author,
			Content:     pt,
			SentAt:      time.Unix(row.Delay, 0),
			IsMe:        row.Sent,
			Retracted:   row.Retracted,
			Encrypted:   row.E2eencrypted,
			Reactions:   loadReactionsForMessage(ctx, s, row.Idattr.String),
			Attachments: attachmentURLs(pt),
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

// replaceReactions fully replaces reactorJID's reaction set on msgID with
// emojis, matching XEP-0444 semantics (a new reaction stanza always replaces
// the sender's previous set, never adds to it).
func replaceReactions(ctx context.Context, s *accountSession, msgID, reactorJID string, emojis []string) error {
	if err := s.db.DeleteReactionsByReactor(ctx, storage.DeleteReactionsByReactorParams{
		AccountJid: s.account.JID, IDAttr: msgID, FromJid: reactorJID,
	}); err != nil {
		return err
	}
	for _, e := range emojis {
		if err := s.db.InsertReaction(ctx, storage.InsertReactionParams{
			AccountJid: s.account.JID, IDAttr: msgID, FromJid: reactorJID, Emoji: e,
		}); err != nil {
			return err
		}
	}
	return nil
}

// loadReactionsForMessage aggregates all reactors' current sets on msgID into
// per-emoji counts, flagging whether our own account is among the reactors.
func loadReactionsForMessage(ctx context.Context, s *accountSession, msgID string) []ui.Reaction {
	rows, err := s.db.ListReactionsForMessage(ctx, storage.ListReactionsForMessageParams{
		AccountJid: s.account.JID, IDAttr: msgID,
	})
	if err != nil {
		debugf("warning: loading reactions for %s: %v\n", msgID, err)
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
