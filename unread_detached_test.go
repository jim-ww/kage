package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/daemon"
	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
)

// The daemon counts unread messages itself only while no TUI is attached:
// with one attached, the TUI is the one that knows whether the chat is being
// looked at, and counting here too would double-count every message.
func TestCountUnreadOnlyWhileNoTUIAttached(t *testing.T) {
	dir := t.TempDir()
	dbConn, queries, err := storage.Open(filepath.Join(dir, "kage.db"))
	if err != nil {
		t.Fatalf("opening storage: %v", err)
	}
	t.Cleanup(func() { dbConn.Close() })

	ctx := context.Background()
	const accountJID, peer = "me@example.com", "peer@example.com"
	s := &accountSession{account: config.Account{JID: accountJID}, db: queries}
	srv := ipc.NewServer()
	t.Cleanup(func() { daemon.SetUnread(false) })

	unreadCount := func() int {
		t.Helper()
		rows, err := queries.ListChatUnread(ctx, accountJID)
		if err != nil {
			t.Fatalf("ListChatUnread: %v", err)
		}
		for _, r := range rows {
			if r.Rosterjid == peer {
				return int(r.Count)
			}
		}
		return 0
	}

	// Our own messages and undecryptable ones never count, same as in the UI.
	countUnreadWhileDetached(ctx, srv, s, peer, []ui.Message{
		{Content: "hi"},
		{Content: "me too", IsMe: true},
		{Content: "garbled", DecryptFailed: true},
		{Content: "still there?"},
	})
	if got := unreadCount(); got != 2 {
		t.Fatalf("unread count = %d, want 2", got)
	}
	if !daemon.Unread() {
		t.Fatal("tray badge off after counting unread messages")
	}

	sockPath := filepath.Join(dir, "kage.sock")
	ln, err := ipc.Listen(sockPath)
	if err != nil {
		t.Fatalf("listening on socket: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go srv.Accept(ln, func(ipc.ClientID, string, json.RawMessage) (any, error) { return nil, nil })

	conn, err := ipc.Dial(sockPath, func(ipc.Event) {})
	if err != nil {
		t.Fatalf("dialing socket: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	waitForClients(t, srv, 1)

	countUnreadWhileDetached(ctx, srv, s, peer, []ui.Message{{Content: "and another"}})
	if got := unreadCount(); got != 2 {
		t.Fatalf("unread count = %d after a message with a TUI attached, want 2 (the TUI counts that one)", got)
	}
}

// waitForClients blocks until srv has accepted want connections - Dial
// returns as soon as the socket is connected, which is before Accept has
// registered it.
func waitForClients(t *testing.T, srv *ipc.Server, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.ClientCount() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("server has %d clients attached, want %d", srv.ClientCount(), want)
}
