package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/xmpp"
)

// newTestAccountSession builds an accountSession backed by a real (tempdir)
// sqlite database, for tests exercising the DB-backed outbox - a plain
// in-memory accountSession{} with db left nil would panic the moment
// enqueueOutbox/loadOutbox tries to use it.
func newTestAccountSession(t *testing.T) *accountSession {
	t.Helper()
	_, queries, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	return &accountSession{account: config.Account{JID: "alice@example.com"}, db: queries}
}

// TestSendQueuesWhileOffline verifies that any send attempted while an
// account has no live client - a plain message, a reaction, a retraction, or
// a correction - is persisted to the outbox table instead of failing
// outright, and that adapter.flushOutbox leaves every row untouched (not
// dropped) when the account is still offline by the time it runs.
func TestSendQueuesWhileOffline(t *testing.T) {
	sess := newTestAccountSession(t)
	a := &adapter{sessions: []*accountSession{sess}}

	cases := []struct {
		name string
		opts ui.SendOptions
	}{
		{"plain message", ui.SendOptions{}},
		{"reaction", ui.SendOptions{ReactionTargetID: "msg1", Reactions: []string{"👍"}}},
		{"retraction", ui.SendOptions{RetractID: "msg1"}},
		{"correction", ui.SendOptions{ReplaceID: "msg1"}},
	}

	for i, c := range cases {
		id, err := a.send(context.Background(), 0, "bob@example.com", "hello", c.opts)
		if !errors.Is(err, ui.ErrQueued) {
			t.Fatalf("%s while offline: got err %v, want ui.ErrQueued", c.name, err)
		}
		if id != "" {
			t.Fatalf("%s while offline: got id %q, want empty (nothing sent yet)", c.name, id)
		}

		queued, err := sess.loadOutbox(context.Background())
		if err != nil {
			t.Fatalf("loadOutbox: %v", err)
		}
		if want := i + 1; len(queued) != want {
			t.Fatalf("outbox length after %s = %d, want %d", c.name, len(queued), want)
		}
	}

	// flushOutbox with still no live client must leave every row alone -
	// nothing lost, nothing sent early.
	a.flushOutbox(context.Background(), sess)

	queued, err := sess.loadOutbox(context.Background())
	if err != nil {
		t.Fatalf("loadOutbox: %v", err)
	}
	if want := len(cases); len(queued) != want {
		t.Fatalf("outbox length after flush while still offline = %d, want %d (untouched, not dropped)", len(queued), want)
	}
}

// TestOutboxSurvivesRestart verifies that a queued send is real durable
// storage, not an in-memory slice - a fresh accountSession opened against
// the same database (simulating a process restart) must still see it.
func TestOutboxSurvivesRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	_, queries, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	sess := &accountSession{account: config.Account{JID: "alice@example.com"}, db: queries}
	a := &adapter{sessions: []*accountSession{sess}}

	if _, err := a.send(context.Background(), 0, "bob@example.com", "hello", ui.SendOptions{LocalID: "local-1"}); !errors.Is(err, ui.ErrQueued) {
		t.Fatalf("send while offline: got err %v, want ui.ErrQueued", err)
	}

	// Reopen against the same file, as a fresh process would after a
	// restart - a brand new *storage.Queries, sharing no in-memory state
	// with sess/queries above.
	_, reopened, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("storage.Open (reopen): %v", err)
	}
	restarted := &accountSession{account: config.Account{JID: "alice@example.com"}, db: reopened}

	queued, err := restarted.loadOutbox(context.Background())
	if err != nil {
		t.Fatalf("loadOutbox after restart: %v", err)
	}
	if len(queued) != 1 {
		t.Fatalf("outbox length after restart = %d, want 1", len(queued))
	}
	if queued[0].opts.LocalID != "local-1" {
		t.Errorf("LocalID after restart = %q, want %q", queued[0].opts.LocalID, "local-1")
	}
	if queued[0].to != "bob@example.com" {
		t.Errorf("to after restart = %q, want %q", queued[0].to, "bob@example.com")
	}
	if queued[0].body != "hello" {
		t.Errorf("body after restart = %q, want %q", queued[0].body, "hello")
	}
}

// TestDeleteOutboxByLocalID verifies a user-initiated permanent delete of a
// still-pending send removes it from storage without ever sending it, and
// reports false (not an error) for an already-gone LocalID.
func TestDeleteOutboxByLocalID(t *testing.T) {
	sess := newTestAccountSession(t)

	if err := sess.enqueueOutbox(context.Background(), "bob@example.com", "hello", ui.SendOptions{LocalID: "local-1"}); err != nil {
		t.Fatalf("enqueueOutbox: %v", err)
	}

	to, deleted, err := sess.deleteOutboxByLocalID(context.Background(), "local-1")
	if err != nil {
		t.Fatalf("deleteOutboxByLocalID: %v", err)
	}
	if !deleted {
		t.Error("deleteOutboxByLocalID found = false, want true")
	}
	if to != "bob@example.com" {
		t.Errorf("deleteOutboxByLocalID to = %q, want %q", to, "bob@example.com")
	}

	queued, err := sess.loadOutbox(context.Background())
	if err != nil {
		t.Fatalf("loadOutbox: %v", err)
	}
	if len(queued) != 0 {
		t.Fatalf("outbox length after delete = %d, want 0", len(queued))
	}

	_, deleted, err = sess.deleteOutboxByLocalID(context.Background(), "local-1")
	if err != nil {
		t.Fatalf("deleteOutboxByLocalID (already gone): %v", err)
	}
	if deleted {
		t.Error("deleteOutboxByLocalID (already gone) found = true, want false")
	}
}

// TestUploadFileQueuesWhileOffline verifies that a staged attachment's
// upload+send is queued as one unit when the account is offline (instead of
// erroring), and that flushOutbox leaves it in place if still offline
// rather than losing it.
func TestUploadFileQueuesWhileOffline(t *testing.T) {
	sess := newTestAccountSession(t)
	a := &adapter{sessions: []*accountSession{sess}}

	msg := a.UploadFile(0, "bob@example.com", "/tmp/report.pdf", "here you go", ui.SendOptions{ReplyToID: "msg1"})
	result, ok := msg.(ui.FileUploadResultMsg)
	if !ok {
		t.Fatalf("UploadFile returned %T, want ui.FileUploadResultMsg", msg)
	}
	if !result.Queued {
		t.Fatal("UploadFile while offline: Queued = false, want true")
	}
	if result.Err != nil {
		t.Fatalf("UploadFile while offline: Err = %v, want nil", result.Err)
	}

	queued, err := sess.loadOutbox(context.Background())
	if err != nil {
		t.Fatalf("loadOutbox: %v", err)
	}
	if len(queued) != 1 {
		t.Fatalf("outbox length = %d, want 1", len(queued))
	}

	a.flushOutbox(context.Background(), sess)

	queued, err = sess.loadOutbox(context.Background())
	if err != nil {
		t.Fatalf("loadOutbox: %v", err)
	}
	if len(queued) != 1 {
		t.Fatalf("outbox length after flush while still offline = %d, want 1 (untouched, not dropped)", len(queued))
	}
}

// TestPendingOutboxMessagesByPeer verifies that only plain new-message sends
// - not reactions/retractions/corrections (which target an already-shown
// message, not a new chat row) or staged attachments (their own
// optimistic-echo flow is handled separately) - surface as Pending chat
// history, grouped by recipient, so a queued send left over from before a
// restart is still visible in the chat rather than invisible until it's
// actually attempted.
func TestPendingOutboxMessagesByPeer(t *testing.T) {
	_, queries, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	sess := &accountSession{account: config.Account{JID: "alice@example.com"}, db: queries}
	acct := sess.account

	cases := []struct {
		name string
		to   string
		opts ui.SendOptions
	}{
		{"plain to bob", "bob@example.com", ui.SendOptions{LocalID: "local-1"}},
		{"plain to carol", "carol@example.com", ui.SendOptions{LocalID: "local-2"}},
		{"reaction to bob", "bob@example.com", ui.SendOptions{ReactionTargetID: "msg1", Reactions: []string{"👍"}}},
		{"retraction to bob", "bob@example.com", ui.SendOptions{RetractID: "msg1"}},
		{"correction to bob", "bob@example.com", ui.SendOptions{ReplaceID: "msg1"}},
	}
	for _, c := range cases {
		if err := sess.enqueueOutbox(context.Background(), c.to, "hello", c.opts); err != nil {
			t.Fatalf("enqueueOutbox(%s): %v", c.name, err)
		}
	}
	if err := sess.enqueueOutboxFile(context.Background(), "bob@example.com", "caption", "/tmp/report.pdf", ui.SendOptions{LocalID: "local-3"}); err != nil {
		t.Fatalf("enqueueOutboxFile: %v", err)
	}

	byPeer, err := pendingOutboxMessagesByPeer(context.Background(), queries, acct, nil)
	if err != nil {
		t.Fatalf("pendingOutboxMessagesByPeer: %v", err)
	}

	if got := len(byPeer["bob@example.com"]); got != 2 {
		t.Fatalf("bob@example.com pending count = %d, want 2 (plain send + staged attachment; reaction/retraction/correction excluded)", got)
	}
	if got := byPeer["bob@example.com"][0]; got.LocalID != "local-1" || !got.Pending || !got.IsMe || got.Content != "hello" {
		t.Errorf("bob@example.com[0] = %+v, want LocalID=local-1 Pending=true IsMe=true Content=hello", got)
	}
	if got := byPeer["bob@example.com"][1]; got.LocalID != "local-3" || !got.Pending || !got.IsMe || got.Content != "caption\n[queued: report.pdf]" {
		t.Errorf("bob@example.com[1] = %+v, want LocalID=local-3 Pending=true IsMe=true Content=%q", got, "caption\n[queued: report.pdf]")
	}
	if got := len(byPeer["carol@example.com"]); got != 1 {
		t.Fatalf("carol@example.com pending count = %d, want 1", got)
	}
}

// TestRealSendFailureSurvivesTUIRestart reproduces the reported bug: a send
// that fails for a real (non-offline) reason - here, "omemo not ready"
// because s.omemoMgrV1 was never set up, the same failure mode a live
// account whose OMEMO setup hasn't finished yet would hit - used to only
// ever be reflected in whichever TUI process's in-memory Model rendered the
// ✗ marker. Restarting just that process (not the daemon) rebuilds its view
// via a fresh connectAccountLocal-style read from storage, which had no
// record of the failure at all, and the message silently disappeared. It
// must now show up as Failed after a simulated restart (a second
// accountSession/pendingOutboxMessagesByPeer call against the same
// database), the same way a queued send already did.
func TestRealSendFailureSurvivesTUIRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	_, queries, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	acct := config.Account{JID: "alice@example.com"}
	sess := &accountSession{account: acct, db: queries}
	// A live (Closed() == false), zero-value client is enough to get past
	// adapter.send's liveClient() check into the OMEMO branch below without
	// any real network I/O - s.omemoMgrV1/V2 are deliberately left nil, the
	// same as a real account whose setupOmemo hasn't completed yet.
	sess.client.Store(&xmpp.Client{})
	a := &adapter{sessions: []*accountSession{sess}}

	_, err = a.send(context.Background(), 0, "bob@example.com", "hello", ui.SendOptions{LocalID: "local-1"})
	if err == nil || errors.Is(err, ui.ErrQueued) {
		t.Fatalf("send with no omemo manager: got err %v, want a real (non-queued) failure", err)
	}

	// Simulate a TUI-only restart: nothing above touched sess again, so this
	// reload goes entirely through storage, exactly like a fresh process's
	// listAccounts -> connectAccountLocal would.
	byPeer, err := pendingOutboxMessagesByPeer(context.Background(), queries, acct, nil)
	if err != nil {
		t.Fatalf("pendingOutboxMessagesByPeer: %v", err)
	}
	msgs := byPeer["bob@example.com"]
	if len(msgs) != 1 {
		t.Fatalf("bob@example.com pending/failed count after restart = %d, want 1", len(msgs))
	}
	got := msgs[0]
	if !got.Failed {
		t.Error("Failed = false after restart, want true")
	}
	if got.Pending {
		t.Error("Pending = true after restart, want false (it's Failed, not still queued)")
	}
	if got.Content != "hello" {
		t.Errorf("Content after restart = %q, want %q", got.Content, "hello")
	}
	if got.LocalID != "local-1" {
		t.Errorf("LocalID after restart = %q, want %q", got.LocalID, "local-1")
	}
}
