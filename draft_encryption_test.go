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
// is configured, and that decryptDraft correctly opens both the encrypted
// and the legacy-plaintext (encrypted=false) row shapes.
func TestSaveDraftSealsUnderLocalKey(t *testing.T) {
	dir := t.TempDir()
	_, queries, err := storage.Open(filepath.Join(dir, "kage.db"))
	if err != nil {
		t.Fatalf("opening storage: %v", err)
	}

	key := make([]byte, localstore.KeySize)
	for i := range key {
		key[i] = byte(i + 1)
	}
	a := &adapter{queries: queries, localKey: key}

	if err := a.SaveDraft("me@example.com", "you@example.com", "sealed draft"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	rows, err := queries.ListChatDrafts(context.Background(), "me@example.com")
	if err != nil {
		t.Fatalf("ListChatDrafts: %v", err)
	}
	if len(rows) != 1 || !rows[0].Encrypted {
		t.Fatalf("ListChatDrafts = %+v, want one encrypted row", rows)
	}
	if rows[0].Body == "sealed draft" {
		t.Fatal("stored draft body should be sealed ciphertext, not the plaintext")
	}
	if got := decryptDraft(key, rows[0].Body, rows[0].Encrypted); got != "sealed draft" {
		t.Fatalf("decryptDraft with the right key = %q, want %q", got, "sealed draft")
	}
	if got := decryptDraft(nil, rows[0].Body, rows[0].Encrypted); got != "" {
		t.Fatalf("decryptDraft with no key = %q, want empty (best-effort)", got)
	}

	// A pre-existing plaintext row (saved before a storage password was ever
	// configured, or by an adapter with no localKey) must still round-trip.
	if err := queries.SetChatDraft(context.Background(), storage.SetChatDraftParams{
		AccountJid: "me@example.com", RosterJid: "plain@example.com", Body: "plain draft", Encrypted: false,
	}); err != nil {
		t.Fatalf("SetChatDraft (plaintext): %v", err)
	}
	rows, err = queries.ListChatDrafts(context.Background(), "me@example.com")
	if err != nil {
		t.Fatalf("ListChatDrafts (2): %v", err)
	}
	var plain storage.ListChatDraftsRow
	for _, r := range rows {
		if r.Rosterjid == "plain@example.com" {
			plain = r
		}
	}
	if got := decryptDraft(key, plain.Body, plain.Encrypted); got != "plain draft" {
		t.Fatalf("decryptDraft on a plaintext row = %q, want %q", got, "plain draft")
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
