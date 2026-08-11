package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/crypto/localstore"
	"github.com/jim-ww/kage/storage"
)

func newTestKey(seed byte) []byte {
	key := make([]byte, localstore.KeySize)
	for i := range key {
		key[i] = seed + byte(i)
	}
	return key
}

// TestRotateStorageKeyReencryptsAllRows checks the core safety property this
// was built for: every encrypted message and draft row ends up sealed under
// the new key (and openable with it), plaintext rows are left untouched, and
// the new salt is committed alongside them in the same transaction.
func TestRotateStorageKeyReencryptsAllRows(t *testing.T) {
	dir := t.TempDir()
	db, queries, err := storage.Open(filepath.Join(dir, "kage.db"))
	if err != nil {
		t.Fatalf("opening storage: %v", err)
	}
	ctx := context.Background()

	oldKey := newTestKey(1)
	newKey := newTestKey(99)

	sealedHello, err := localstore.Seal(oldKey, "hello")
	if err != nil {
		t.Fatalf("sealing fixture: %v", err)
	}
	if _, err := queries.InsertMessage(ctx, storage.InsertMessageParams{
		AccountJid: "me@example.com", Sent: true, IDAttr: nullString("id-1"),
		Body: sql.NullString{String: sealedHello, Valid: true}, Encrypted: true,
		StanzaType: "normal", RosterJid: nullString("bob@example.com"),
	}); err != nil {
		t.Fatalf("InsertMessage (encrypted): %v", err)
	}
	if _, err := queries.InsertMessage(ctx, storage.InsertMessageParams{
		AccountJid: "me@example.com", Sent: true, IDAttr: nullString("id-2"),
		Body: sql.NullString{String: "plain body", Valid: true}, Encrypted: false,
		StanzaType: "normal", RosterJid: nullString("bob@example.com"),
	}); err != nil {
		t.Fatalf("InsertMessage (plaintext): %v", err)
	}

	sealedDraft, err := localstore.Seal(oldKey, "unsent draft")
	if err != nil {
		t.Fatalf("sealing draft fixture: %v", err)
	}
	if err := queries.SetChatDraft(ctx, storage.SetChatDraftParams{
		AccountJid: "me@example.com", RosterJid: "bob@example.com", Body: sealedDraft, Encrypted: true,
	}); err != nil {
		t.Fatalf("SetChatDraft: %v", err)
	}

	newSalt := []byte("0123456789abcdef")
	if err := rotateStorageKey(ctx, db, queries, oldKey, newKey, newSalt); err != nil {
		t.Fatalf("rotateStorageKey: %v", err)
	}

	msgs, err := queries.ListMessagesByRoster(ctx, storage.ListMessagesByRosterParams{
		AccountJid: "me@example.com", RosterJid: nullString("bob@example.com"),
	})
	if err != nil {
		t.Fatalf("ListMessagesByRoster: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	for _, m := range msgs {
		switch m.Idattr.String {
		case "id-1":
			if !m.Encrypted {
				t.Fatal("id-1 should still be marked encrypted")
			}
			pt, err := localstore.Open(newKey, m.Body.String)
			if err != nil {
				t.Fatalf("opening id-1 under new key: %v", err)
			}
			if pt != "hello" {
				t.Fatalf("id-1 decrypted = %q, want %q", pt, "hello")
			}
		case "id-2":
			if m.Encrypted || m.Body.String != "plain body" {
				t.Fatalf("id-2 (plaintext) was touched: encrypted=%v body=%q", m.Encrypted, m.Body.String)
			}
		default:
			t.Fatalf("unexpected message idAttr %q", m.Idattr.String)
		}
	}

	drafts, err := queries.ListChatDrafts(ctx, "me@example.com")
	if err != nil {
		t.Fatalf("ListChatDrafts: %v", err)
	}
	if len(drafts) != 1 || !drafts[0].Encrypted {
		t.Fatalf("ListChatDrafts = %+v, want one encrypted row", drafts)
	}
	pt, err := localstore.Open(newKey, drafts[0].Body)
	if err != nil {
		t.Fatalf("opening draft under new key: %v", err)
	}
	if pt != "unsent draft" {
		t.Fatalf("draft decrypted = %q, want %q", pt, "unsent draft")
	}

	salt, err := queries.GetLocalKeySalt(ctx)
	if err != nil {
		t.Fatalf("GetLocalKeySalt: %v", err)
	}
	if string(salt) != string(newSalt) {
		t.Fatalf("stored salt = %q, want %q", salt, newSalt)
	}
}

// TestRotateStorageKeyRollsBackOnFailure checks the atomicity guarantee: if
// any row fails to decrypt partway through the sweep (e.g. a corrupt row, or
// the wrong "old" key), the whole transaction rolls back — nothing is left
// half-migrated, and the old salt/rows are exactly as they were.
func TestRotateStorageKeyRollsBackOnFailure(t *testing.T) {
	dir := t.TempDir()
	db, queries, err := storage.Open(filepath.Join(dir, "kage.db"))
	if err != nil {
		t.Fatalf("opening storage: %v", err)
	}
	ctx := context.Background()

	oldKey := newTestKey(1)
	newKey := newTestKey(99)
	wrongKey := newTestKey(50) // will fail to decrypt oldKey-sealed rows

	sealed, err := localstore.Seal(oldKey, "hello")
	if err != nil {
		t.Fatalf("sealing fixture: %v", err)
	}
	if _, err := queries.InsertMessage(ctx, storage.InsertMessageParams{
		AccountJid: "me@example.com", Sent: true, IDAttr: nullString("id-1"),
		Body: sql.NullString{String: sealed, Valid: true}, Encrypted: true,
		StanzaType: "normal", RosterJid: nullString("bob@example.com"),
	}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}

	originalSalt := []byte("original-salt-16")
	if err := queries.SetLocalKeySalt(ctx, originalSalt); err != nil {
		t.Fatalf("SetLocalKeySalt (seeding): %v", err)
	}

	err = rotateStorageKey(ctx, db, queries, wrongKey, newKey, []byte("shouldnotstick16"))
	if err == nil {
		t.Fatal("expected rotateStorageKey to fail with the wrong old key")
	}

	msgs, err := queries.ListMessagesByRoster(ctx, storage.ListMessagesByRosterParams{
		AccountJid: "me@example.com", RosterJid: nullString("bob@example.com"),
	})
	if err != nil {
		t.Fatalf("ListMessagesByRoster: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Body.String != sealed {
		t.Fatalf("message body changed despite the failed rotation: %+v", msgs)
	}

	saltAfter, err := queries.GetLocalKeySalt(ctx)
	if err != nil {
		t.Fatalf("GetLocalKeySalt (after): %v", err)
	}
	if string(saltAfter) != string(originalSalt) {
		t.Fatal("salt changed despite the failed rotation")
	}
}

// TestChangeStoragePasswordRejectsEmptyPassword checks the empty-password
// guard rejects before touching the database at all.
func TestChangeStoragePasswordRejectsEmptyPassword(t *testing.T) {
	dir := t.TempDir()
	db, queries, err := storage.Open(filepath.Join(dir, "kage.db"))
	if err != nil {
		t.Fatalf("opening storage: %v", err)
	}
	a := &adapter{db: db, queries: queries, localKey: newTestKey(1)}

	if err := a.ChangeStoragePassword(""); err == nil {
		t.Fatal("expected an error for an empty new password")
	}
}

// TestPersistStoragePasswordWritesPlaintextConfig checks the
// useKeyring=false path writes the new password into config.yaml.
func TestPersistStoragePasswordWritesPlaintextConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	if err := persistStoragePassword(cfgPath, false, "new-storage-password"); err != nil {
		t.Fatalf("persistStoragePassword: %v", err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("reloading config: %v", err)
	}
	if cfg.Storage.Password != "new-storage-password" {
		t.Fatalf("config.Storage.Password = %q, want %q", cfg.Storage.Password, "new-storage-password")
	}
}
