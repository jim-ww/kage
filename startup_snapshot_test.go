package main

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/crypto/gpg"
	"github.com/jim-ww/kage/storage"
)

// seedAccountHistory fills a store with contacts chats of msgsPerChat
// encrypted messages each, the shape connectAccountLocal has to read and
// decrypt on every TUI attach.
func seedAccountHistory(t *testing.T, jid string, contacts, msgsPerChat int) (*accountSession, *storage.Queries) {
	t.Helper()
	ctx := context.Background()

	db, q, err := storage.Open(filepath.Join(t.TempDir(), "kage.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	key, err := loadLocalKey(config.StorageConfig{Password: "hunter2"}, true, q)
	if err != nil {
		t.Fatalf("loadLocalKey: %v", err)
	}
	sess := &accountSession{
		account:  config.Account{JID: jid},
		db:       q,
		gpg:      gpg.Encrypter{},
		localKey: key,
	}

	for c := range contacts {
		peer := fmt.Sprintf("peer-%03d@example.com", c)
		if err := q.UpsertRoster(ctx, storage.UpsertRosterParams{
			AccountJid: jid, Jid: peer, Subs: "both",
		}); err != nil {
			t.Fatalf("UpsertRoster: %v", err)
		}
		for i := range msgsPerChat {
			body, encrypted := encryptForStorage(sess, fmt.Sprintf("message %d in chat %d, long enough to be representative of a real chat line", i, c))
			if _, err := q.InsertMessage(ctx, storage.InsertMessageParams{
				AccountJid: jid, Sent: i%2 == 0,
				IDAttr: nullString(fmt.Sprintf("m-%03d-%04d", c, i)),
				Body:   body, Encrypted: encrypted,
				StanzaType: "chat", RosterJid: nullString(peer),
			}); err != nil {
				t.Fatalf("InsertMessage: %v", err)
			}
		}
	}
	return sess, q
}

// TestAccountSnapshotCostIsBounded measures the local half of what a starting
// TUI waits on: listAccounts calls connectAccountLocal once per account, and
// the TUI cannot draw its first frame until that RPC returns.
//
// Not a strict performance assertion - the ceiling is deliberately loose, so
// it fails on an order-of-magnitude regression (a per-message query, a
// forgotten index, re-decrypting whole histories) rather than on a slow
// machine. The logged number is the useful part.
func TestAccountSnapshotCostIsBounded(t *testing.T) {
	ctx := context.Background()
	const contacts, msgsPerChat = 20, 200
	sess, q := seedAccountHistory(t, "me@example.com", contacts, msgsPerChat)

	start := time.Now()
	_, uiAcct, err := connectAccountLocal(ctx, sess.account, q, sess.localKey)
	if err != nil {
		t.Fatalf("connectAccountLocal: %v", err)
	}
	elapsed := time.Since(start)

	t.Logf("connectAccountLocal: %v for %d chats x %d messages (%d total)",
		elapsed, contacts, msgsPerChat, contacts*msgsPerChat)

	if len(uiAcct.Chats) != contacts {
		t.Errorf("snapshot has %d chats, want %d", len(uiAcct.Chats), contacts)
	}
	// Only historyPageSize messages per chat are loaded, not the whole
	// history - if that ever regresses to loading everything, this is where
	// it shows up.
	for i := range uiAcct.Chats {
		if got := len(uiAcct.Messages[i]); got > historyPageSize {
			t.Errorf("chat %d loaded %d messages, want at most the page size %d", i, got, historyPageSize)
		}
	}
	if elapsed > 3*time.Second {
		t.Errorf("building one account's snapshot took %v; a TUI blocks on this before it can draw", elapsed)
	}
}
