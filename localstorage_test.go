package main

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/crypto/gpg"
	"github.com/jim-ww/kage/crypto/localstore"
	"github.com/jim-ww/kage/storage"
)

func TestLoadLocalKeyDeterministicAcrossOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kage.db")
	storageCfg := config.StorageConfig{Password: "hunter2"}

	db1, q1, err := storage.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	key1, err := loadLocalKey(storageCfg, true, q1)
	if err != nil {
		t.Fatalf("loadLocalKey (mint): %v", err)
	}
	db1.Close()

	db2, q2, err := storage.Open(path)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer db2.Close()
	key2, err := loadLocalKey(storageCfg, true, q2)
	if err != nil {
		t.Fatalf("loadLocalKey (reload): %v", err)
	}

	if string(key1) != string(key2) {
		t.Fatal("loadLocalKey derived a different key across opens with the same password")
	}
}

func TestLoadLocalKeyDifferentPasswordDifferentKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kage.db")
	db, q, err := storage.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	key1, err := loadLocalKey(config.StorageConfig{Password: "hunter2"}, true, q)
	if err != nil {
		t.Fatalf("loadLocalKey: %v", err)
	}
	key2, err := loadLocalKey(config.StorageConfig{Password: "different"}, true, q)
	if err != nil {
		t.Fatalf("loadLocalKey: %v", err)
	}
	if string(key1) == string(key2) {
		t.Fatal("loadLocalKey produced the same key for two different passwords")
	}
}

// TestSharedDBAccountIsolation verifies that two accounts using the same
// database file (single shared db, distinguished by accountJID) never see
// each other's history, even when both happen to have a contact with the
// same JID.
func TestSharedDBAccountIsolation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kage.db")
	db, q, err := storage.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	key, err := loadLocalKey(config.StorageConfig{Password: "hunter2"}, true, q)
	if err != nil {
		t.Fatalf("loadLocalKey: %v", err)
	}

	alice := &accountSession{account: config.Account{JID: "alice@example.com"}, db: q, gpg: gpg.Encrypter{}, localKey: key}
	bob := &accountSession{account: config.Account{JID: "bob@example.com"}, db: q, gpg: gpg.Encrypter{}, localKey: key}

	const sharedContact = "shared@example.com"
	aliceBody, aliceEncrypted := encryptForStorage(alice, "alice's message")
	if _, err := q.InsertMessage(ctx, storage.InsertMessageParams{
		AccountJid: alice.account.JID, Sent: true, IDAttr: nullString("m1"),
		Body: aliceBody, Encrypted: aliceEncrypted, StanzaType: "chat", RosterJid: nullString(sharedContact),
	}); err != nil {
		t.Fatalf("InsertMessage(alice): %v", err)
	}
	bobBody, bobEncrypted := encryptForStorage(bob, "bob's message")
	if _, err := q.InsertMessage(ctx, storage.InsertMessageParams{
		AccountJid: bob.account.JID, Sent: true, IDAttr: nullString("m1"),
		Body: bobBody, Encrypted: bobEncrypted, StanzaType: "chat", RosterJid: nullString(sharedContact),
	}); err != nil {
		t.Fatalf("InsertMessage(bob): %v", err)
	}

	aliceHist := loadHistory(ctx, alice, sharedContact, sharedContact)
	if len(aliceHist) != 1 || aliceHist[0].Content != "alice's message" {
		t.Fatalf("alice's history = %+v, want exactly her own message", aliceHist)
	}
	bobHist := loadHistory(ctx, bob, sharedContact, sharedContact)
	if len(bobHist) != 1 || bobHist[0].Content != "bob's message" {
		t.Fatalf("bob's history = %+v, want exactly his own message", bobHist)
	}
}

func TestEncryptForStorageUsesAESNotGPG(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kage.db")
	db, q, err := storage.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	key, err := loadLocalKey(config.StorageConfig{Password: "hunter2"}, true, q)
	if err != nil {
		t.Fatalf("loadLocalKey: %v", err)
	}
	s := &accountSession{account: config.Account{JID: "me@example.com"}, db: q, gpg: gpg.Encrypter{}, localKey: key}

	sealed, encrypted := encryptForStorage(s, "hello")
	if !sealed.Valid {
		t.Fatal("encryptForStorage returned invalid")
	}
	if !encrypted {
		t.Fatal("encryptForStorage returned encrypted=false with a key configured")
	}
	if gpg.Looks(sealed.String) {
		t.Fatal("encryptForStorage produced GPG-armored output, want AES-sealed")
	}
	got, err := localstore.Open(key, sealed.String)
	if err != nil {
		t.Fatalf("localstore.Open: %v", err)
	}
	if got != "hello" {
		t.Fatalf("got %q, want %q", got, "hello")
	}
}

// TestLoadLocalKeyNotConfiguredIsNotAnError verifies that leaving the local
// storage password entirely unset is a deliberate, error-free choice — it
// means messages are stored in plain text, not that startup should fail.
func TestLoadLocalKeyNotConfiguredIsNotAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kage.db")
	db, q, err := storage.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	key, err := loadLocalKey(config.StorageConfig{}, true, q)
	if err != nil {
		t.Fatalf("loadLocalKey with no password configured returned an error: %v", err)
	}
	if key != nil {
		t.Fatalf("loadLocalKey with no password configured returned a key: %v", key)
	}
}

// TestEncryptForStorageNoKeyStoresPlaintext verifies encryptForStorage's
// fallback when no local storage password is configured: the body is stored
// as-is, flagged encrypted=false.
func TestEncryptForStorageNoKeyStoresPlaintext(t *testing.T) {
	s := &accountSession{account: config.Account{JID: "me@example.com"}, gpg: gpg.Encrypter{}, localKey: nil}

	body, encrypted := encryptForStorage(s, "plain hello")
	if encrypted {
		t.Fatal("encryptForStorage returned encrypted=true with no key configured")
	}
	if !body.Valid || body.String != "plain hello" {
		t.Fatalf("body = %+v, want valid plaintext", body)
	}
}

// TestLoadHistoryHandlesPlaintextAndEncryptedRows verifies loadHistory reads
// both a plaintext row (encrypted=false, stored before any password was
// configured) and an AES-sealed row (encrypted=true) correctly in the same
// conversation, and that reading the plaintext row while a key IS available
// re-seals it in place — so it's read as plaintext at most once.
func TestLoadHistoryHandlesPlaintextAndEncryptedRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kage.db")
	db, q, err := storage.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	key, err := loadLocalKey(config.StorageConfig{Password: "hunter2"}, true, q)
	if err != nil {
		t.Fatalf("loadLocalKey: %v", err)
	}
	s := &accountSession{account: config.Account{JID: "me@example.com"}, db: q, gpg: gpg.Encrypter{}, localKey: key}
	const chatAddr = "peer@example.com"

	// A row written back when no password was configured yet.
	if _, err := q.InsertMessage(ctx, storage.InsertMessageParams{
		AccountJid: s.account.JID, Sent: true, IDAttr: nullString("plain1"),
		Body: nullString("stored in the clear"), Encrypted: false,
		StanzaType: "chat", RosterJid: nullString(chatAddr),
	}); err != nil {
		t.Fatalf("InsertMessage (plaintext): %v", err)
	}
	// A row written with encryption.
	sealedBody, encrypted := encryptForStorage(s, "stored sealed")
	if _, err := q.InsertMessage(ctx, storage.InsertMessageParams{
		AccountJid: s.account.JID, Sent: true, IDAttr: nullString("enc1"),
		Body: sealedBody, Encrypted: encrypted,
		StanzaType: "chat", RosterJid: nullString(chatAddr),
	}); err != nil {
		t.Fatalf("InsertMessage (encrypted): %v", err)
	}

	msgs := loadHistory(ctx, s, chatAddr, chatAddr)
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	byID := map[string]string{}
	for _, m := range msgs {
		byID[m.ID] = m.Content
	}
	if byID["plain1"] != "stored in the clear" || byID["enc1"] != "stored sealed" {
		t.Fatalf("unexpected content: %+v", byID)
	}

	// The plaintext row should now be sealed in storage.
	rows, err := q.ListMessagesByRoster(ctx, storage.ListMessagesByRosterParams{
		AccountJid: s.account.JID, RosterJid: nullString(chatAddr),
	})
	if err != nil {
		t.Fatalf("ListMessagesByRoster: %v", err)
	}
	for _, row := range rows {
		if row.Idattr.String == "plain1" && !row.Encrypted {
			t.Fatal("plaintext row was not re-sealed after being read with a key available")
		}
	}
}

// TestLoadHistoryPagePaginatesOldestFirst verifies loadHistoryPage's keyset
// pagination: each call returns the next older page in chronological order,
// hasMore stays true until the oldest stored message is reached, and the
// last page is exactly what's left.
func TestLoadHistoryPagePaginatesOldestFirst(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kage.db")
	db, q, err := storage.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	s := &accountSession{account: config.Account{JID: "me@example.com"}, db: q, gpg: gpg.Encrypter{}}
	const chatAddr = "peer@example.com"

	// 5 messages, insertion order == chronological order (increasing delay).
	// InsertMessage's delay defaults to "now" (second granularity), so a
	// fixed sleep isn't used — id (insertion order) breaks same-second ties,
	// which is exactly what the id tiebreaker in the query is for.
	for i := range 5 {
		if _, err := q.InsertMessage(ctx, storage.InsertMessageParams{
			AccountJid: s.account.JID, Sent: true, IDAttr: nullString(fmt.Sprintf("m%d", i)),
			Body: nullString(fmt.Sprintf("body %d", i)), Encrypted: false,
			StanzaType: "chat", RosterJid: nullString(chatAddr),
		}); err != nil {
			t.Fatalf("InsertMessage %d: %v", i, err)
		}
	}

	origPageSize := historyPageSize
	historyPageSize = 2
	defer func() { historyPageSize = origPageSize }()

	// Each successive page is *older* than the last (newest page first) —
	// mirrors how the UI prepends each "load older" fetch to what's already
	// displayed, to reconstruct the full chronological list.
	var got []string
	for page := 0; ; page++ {
		msgs, hasMore := loadHistoryPage(ctx, s, chatAddr, chatAddr)
		if page == 0 && len(msgs) != 2 {
			t.Fatalf("page 0: got %d messages, want 2", len(msgs))
		}
		ids := make([]string, len(msgs))
		for i, m := range msgs {
			ids[i] = m.ID
		}
		got = append(ids, got...)
		if !hasMore {
			break
		}
		if page > 5 {
			t.Fatal("loadHistoryPage never reported hasMore=false — pagination stuck in a loop")
		}
	}

	want := []string{"m0", "m1", "m2", "m3", "m4"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// TestReadStoredBodyErrorsWithoutKey verifies that an encrypted row can't be
// silently misread as plaintext — with no local storage key available, it's
// a hard error, not garbage content.
func TestReadStoredBodyErrorsWithoutKey(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kage.db")
	db, q, err := storage.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	keyed := &accountSession{account: config.Account{JID: "me@example.com"}, db: q, gpg: gpg.Encrypter{}}
	keyed.localKey, err = loadLocalKey(config.StorageConfig{Password: "hunter2"}, true, q)
	if err != nil {
		t.Fatalf("loadLocalKey: %v", err)
	}
	const chatAddr = "peer@example.com"
	sealedBody, encrypted := encryptForStorage(keyed, "secret")
	if _, err := q.InsertMessage(ctx, storage.InsertMessageParams{
		AccountJid: keyed.account.JID, Sent: true, IDAttr: nullString("enc1"),
		Body: sealedBody, Encrypted: encrypted, StanzaType: "chat", RosterJid: nullString(chatAddr),
	}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}

	unkeyed := &accountSession{account: keyed.account, db: q, gpg: gpg.Encrypter{}, localKey: nil}
	rows, err := q.ListMessagesByRoster(ctx, storage.ListMessagesByRosterParams{
		AccountJid: unkeyed.account.JID, RosterJid: nullString(chatAddr),
	})
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListMessagesByRoster: %d rows, err %v", len(rows), err)
	}
	r := rows[0]
	hrow := historyRow{Sent: r.Sent, Idattr: r.Idattr, Body: r.Body, Encrypted: r.Encrypted, E2eencrypted: r.E2eencrypted, Delay: r.Delay, Replytoidattr: r.Replytoidattr, Retracted: r.Retracted}
	if _, err := readStoredBody(ctx, unkeyed, chatAddr, hrow); err == nil {
		t.Fatal("expected readStoredBody to fail decrypting an encrypted row with no key available")
	}
}
