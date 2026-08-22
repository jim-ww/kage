package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jim-ww/kage/storage"
)

// insertTestMessage inserts one minimal message row for buildExport tests.
func insertTestMessage(t *testing.T, queries *storage.Queries, accountJID, rosterJID, idAttr, body string) {
	t.Helper()
	_, err := queries.InsertMessage(context.Background(), storage.InsertMessageParams{
		AccountJid: accountJID,
		Sent:       true,
		IDAttr:     nullString(idAttr),
		Body:       nullString(body),
		StanzaType: "chat",
		Delay:      int64(0),
		RosterJid:  nullString(rosterJID),
	})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
}

// bodies returns the Body of every message in out, for compact assertions.
func bodies(out exportFile) []string {
	got := make([]string, len(out.Messages))
	for i, m := range out.Messages {
		got[i] = m.Body
	}
	return got
}

func assertBodies(t *testing.T, out exportFile, want ...string) {
	t.Helper()
	got := bodies(out)
	if len(got) != len(want) {
		t.Fatalf("got %d messages %v, want %d %v", len(got), got, len(want), want)
	}
	wantSet := make(map[string]bool, len(want))
	for _, w := range want {
		wantSet[w] = true
	}
	for _, g := range got {
		if !wantSet[g] {
			t.Fatalf("unexpected message %q in export, want only %v", g, want)
		}
	}
}

func TestBuildExportAccountFilter(t *testing.T) {
	_, queries, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}

	insertTestMessage(t, queries, "alice@example.com", "bob@example.com", "m1", "alice-bob")
	insertTestMessage(t, queries, "alice@example.com", "carol@example.com", "m2", "alice-carol")
	insertTestMessage(t, queries, "dave@example.com", "bob@example.com", "m3", "dave-bob")

	// No --account: every account's messages are included.
	out, skipped, err := buildExport(context.Background(), queries, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildExport: %v", err)
	}
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
	assertBodies(t, out, "alice-bob", "alice-carol", "dave-bob")

	// --account alice@example.com: only alice's chats, regardless of peer.
	out, _, err = buildExport(context.Background(), queries, nil, []string{"alice@example.com"}, nil)
	if err != nil {
		t.Fatalf("buildExport: %v", err)
	}
	assertBodies(t, out, "alice-bob", "alice-carol")
}

func TestBuildExportJIDFilter(t *testing.T) {
	_, queries, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}

	insertTestMessage(t, queries, "alice@example.com", "bob@example.com", "m1", "alice-bob")
	insertTestMessage(t, queries, "alice@example.com", "carol@example.com", "m2", "alice-carol")
	insertTestMessage(t, queries, "dave@example.com", "bob@example.com", "m3", "dave-bob")

	// --jid bob@example.com: only chats with bob, across every account.
	out, _, err := buildExport(context.Background(), queries, nil, nil, []string{"bob@example.com"})
	if err != nil {
		t.Fatalf("buildExport: %v", err)
	}
	assertBodies(t, out, "alice-bob", "dave-bob")

	// --jid carol@example.com: only the one chat with carol, nothing else.
	out, _, err = buildExport(context.Background(), queries, nil, nil, []string{"carol@example.com"})
	if err != nil {
		t.Fatalf("buildExport: %v", err)
	}
	assertBodies(t, out, "alice-carol")
}

// TestBuildExportAccountAndJIDFilter checks that --account and --jid combine
// with AND semantics: a chat must match both to be included. In particular,
// a --jid chat under an account not named in --account is excluded (not
// just filtered by --jid alone), while a --jid chat that does belong to a
// named --account is kept.
func TestBuildExportAccountAndJIDFilter(t *testing.T) {
	_, queries, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}

	insertTestMessage(t, queries, "alice@example.com", "bob@example.com", "m1", "alice-bob")
	insertTestMessage(t, queries, "alice@example.com", "carol@example.com", "m2", "alice-carol")
	insertTestMessage(t, queries, "dave@example.com", "bob@example.com", "m3", "dave-bob")

	// --account alice@example.com --jid bob@example.com: dave-bob is
	// excluded even though it matches --jid, because dave isn't in
	// --account; alice-bob matches both and is kept; alice-carol matches
	// --account but not --jid, so it's excluded too.
	out, _, err := buildExport(context.Background(), queries,
		nil, []string{"alice@example.com"}, []string{"bob@example.com"})
	if err != nil {
		t.Fatalf("buildExport: %v", err)
	}
	assertBodies(t, out, "alice-bob")

	// --account dave@example.com --jid bob@example.com: dave's chat with
	// bob matches both and is kept.
	out, _, err = buildExport(context.Background(), queries,
		nil, []string{"dave@example.com"}, []string{"bob@example.com"})
	if err != nil {
		t.Fatalf("buildExport: %v", err)
	}
	assertBodies(t, out, "dave-bob")
}
