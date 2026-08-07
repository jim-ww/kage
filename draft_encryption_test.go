package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jim-ww/kage/crypto/localstore"
	"github.com/jim-ww/kage/storage"
)

// TestSaveDraftSealsUnderLocalKey checks that SaveDraft seals the draft body
// with crypto/localstore (same as message bodies) when a local storage key
// is configured, and that loadDraft correctly opens both the encrypted
// and the legacy-plaintext (encrypted=false) row shapes.
func TestSaveDraftSealsUnderLocalKey(t *testing.T) {
	dir := t.TempDir()
	_, queries, err := storage.Open(filepath.Join(dir, "kage.db"))
	if err != nil {
		t.Fatalf("opening storage: %v", err)
	}
	ctx := context.Background()

	key := make([]byte, localstore.KeySize)
	for i := range key {
		key[i] = byte(i + 1)
	}
	a := &adapter{queries: queries, localKey: key}

	if err := a.SaveDraft("me@example.com", "you@example.com", "sealed draft"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	rows, err := queries.ListChatDrafts(ctx, "me@example.com")
	if err != nil {
		t.Fatalf("ListChatDrafts: %v", err)
	}
	if len(rows) != 1 || !rows[0].Encrypted {
		t.Fatalf("ListChatDrafts = %+v, want one encrypted row", rows)
	}
	if rows[0].Body == "sealed draft" {
		t.Fatal("stored draft body should be sealed ciphertext, not the plaintext")
	}
	if got := loadDraft(ctx, queries, "me@example.com", rows[0], key); got != "sealed draft" {
		t.Fatalf("loadDraft with the right key = %q, want %q", got, "sealed draft")
	}
	if got := loadDraft(ctx, queries, "me@example.com", rows[0], nil); got != "" {
		t.Fatalf("loadDraft with no key = %q, want empty (best-effort)", got)
	}
}

// TestAdapterSaveDraftNoLocalKeyStoresPlaintext checks the no-storage-password
// case: SaveDraft falls back to storing the body as-is.
func TestAdapterSaveDraftNoLocalKeyStoresPlaintext(t *testing.T) {
	dir := t.TempDir()
	_, queries, err := storage.Open(filepath.Join(dir, "kage.db"))
	if err != nil {
		t.Fatalf("opening storage: %v", err)
	}
	a := &adapter{queries: queries}

	if err := a.SaveDraft("me@example.com", "you@example.com", "unsealed draft"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	rows, err := queries.ListChatDrafts(context.Background(), "me@example.com")
	if err != nil {
		t.Fatalf("ListChatDrafts: %v", err)
	}
	if len(rows) != 1 || rows[0].Encrypted || rows[0].Body != "unsealed draft" {
		t.Fatalf("ListChatDrafts = %+v, want one plaintext row = %q", rows, "unsealed draft")
	}
}

// TestLoadDraftMigratesPlaintextOnceKeyAvailable checks the case this
// question was about: a draft saved before a storage password existed (or
// by an adapter/session with no localKey yet) is plaintext in the db;
// loadDraft, once a key becomes available, both returns the correct text
// AND opportunistically re-seals the row in place — mirroring
// readStoredBody's migration behavior for message bodies — so it doesn't
// sit in plaintext forever once encryption becomes available.
func TestLoadDraftMigratesPlaintextOnceKeyAvailable(t *testing.T) {
	dir := t.TempDir()
	_, queries, err := storage.Open(filepath.Join(dir, "kage.db"))
	if err != nil {
		t.Fatalf("opening storage: %v", err)
	}
	ctx := context.Background()

	// Saved with no key configured (mirrors SaveDraft's own no-localKey path).
	if err := queries.SetChatDraft(ctx, storage.SetChatDraftParams{
		AccountJid: "me@example.com", RosterJid: "you@example.com", Body: "typed before password set", Encrypted: false,
	}); err != nil {
		t.Fatalf("SetChatDraft: %v", err)
	}

	key := make([]byte, localstore.KeySize)
	for i := range key {
		key[i] = byte(i + 7)
	}

	rows, err := queries.ListChatDrafts(ctx, "me@example.com")
	if err != nil {
		t.Fatalf("ListChatDrafts: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListChatDrafts = %+v, want one row", rows)
	}

	got := loadDraft(ctx, queries, "me@example.com", rows[0], key)
	if got != "typed before password set" {
		t.Fatalf("loadDraft returned %q, want %q", got, "typed before password set")
	}

	migrated, err := queries.ListChatDrafts(ctx, "me@example.com")
	if err != nil {
		t.Fatalf("ListChatDrafts after migration: %v", err)
	}
	if len(migrated) != 1 || !migrated[0].Encrypted {
		t.Fatalf("ListChatDrafts after loadDraft migration = %+v, want the row re-sealed (encrypted=true)", migrated)
	}
	if migrated[0].Body == "typed before password set" {
		t.Fatal("migrated row's body should now be sealed ciphertext, not the original plaintext")
	}
	if got := loadDraft(ctx, queries, "me@example.com", migrated[0], key); got != "typed before password set" {
		t.Fatalf("loadDraft on the migrated row = %q, want %q", got, "typed before password set")
	}
}
