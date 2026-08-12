package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
)

// TestConnectAndSuperviseAccountRegistersOfflineSession verifies that when
// the very first dial fails (e.g. the app started with no network), the
// account still gets a session in adapter.sessions instead of being left
// permanently unregistered. Before this fix, a.session(idx) stayed "not ok"
// forever, so Send returned "unknown account N" for an account that was
// simply offline - and once the network came back nothing ever reconnected,
// since the supervisor goroutine was never started either.
func TestConnectAndSuperviseAccountRegistersOfflineSession(t *testing.T) {
	_, queries, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}

	acct := config.Account{JID: "alice@localhost", Password: "wrong"}
	a := &adapter{sessions: []*accountSession{nil}}

	// localhost with nothing listening on 5222: dial fails fast
	// (connection refused) instead of hanging on DNS/network timeouts, so
	// this test doesn't need a live server or a long deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connectAndSuperviseAccount(ctx, nil, a, 0, acct, queries, nil)

	sess, ok := a.session(0)
	if !ok {
		t.Fatal("a.session(0) not ok after failed initial connect - account is now unreachable for queuing")
	}

	// With no live client, a plain send must queue rather than erroring
	// with "unknown account".
	id, err := a.send(context.Background(), 0, "bob@localhost", "hello", ui.SendOptions{})
	if !errors.Is(err, ui.ErrQueued) {
		t.Fatalf("send while never-yet-connected: got err %v, want ui.ErrQueued", err)
	}
	if id != "" {
		t.Fatalf("send while never-yet-connected: got id %q, want empty", id)
	}

	queued, err := sess.loadOutbox(context.Background())
	if err != nil {
		t.Fatalf("loadOutbox: %v", err)
	}
	if len(queued) != 1 {
		t.Fatalf("outbox length = %d, want 1", len(queued))
	}
}
