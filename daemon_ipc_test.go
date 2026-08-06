package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/storage"
)

// TestDaemonIPCRoundTrip is a regression guard on daemon_server.go's
// dispatch table: it spins up the same adapter+daemonServer+ipc.Server the
// real --background mode builds (minus any XMPP dialing, which these two
// RPCs don't touch), talks to it over a real Unix socket via ipcClient
// exactly like the TUI does, and checks a write (persists to config.toml)
// and a read (persists to storage) both round-trip correctly. If a future
// change to daemon_server.go's dispatch table or ipc_client.go's RPC
// wrappers breaks the wire format for either, this fails.
func TestDaemonIPCRoundTrip(t *testing.T) {
	dir := t.TempDir()

	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, nil, 0o600); err != nil {
		t.Fatalf("seeding config file: %v", err)
	}

	dbConn, queries, err := storage.Open(filepath.Join(dir, "kage.db"))
	if err != nil {
		t.Fatalf("opening storage: %v", err)
	}
	t.Cleanup(func() { dbConn.Close() })

	srv := ipc.NewServer()
	a := &adapter{
		sessions: nil,
		cfgPath:  cfgPath,
		queries:  queries,
		srv:      srv,
	}
	ds := &daemonServer{a: a, srv: srv}

	sockPath := filepath.Join(dir, "kage.sock")
	ln, err := ipc.Listen(sockPath)
	if err != nil {
		t.Fatalf("listening on socket: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go srv.Accept(ln, ds.handle)

	client := &ipcClient{}
	conn, err := ipc.Dial(sockPath, client.handleEvent)
	if err != nil {
		t.Fatalf("dialing socket: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	client.conn = conn

	if err := client.SetSidebarWidth(42); err != nil {
		t.Fatalf("SetSidebarWidth: %v", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("reloading config: %v", err)
	}
	if cfg.UI.SidebarWidth != 42 {
		t.Errorf("SidebarWidth = %d, want 42", cfg.UI.SidebarWidth)
	}

	if err := client.IncrementChatUnread("me@example.com", "you@example.com", 3); err != nil {
		t.Fatalf("IncrementChatUnread: %v", err)
	}
	counts, err := client.ChatUnreadCounts("me@example.com")
	if err != nil {
		t.Fatalf("ChatUnreadCounts: %v", err)
	}
	if counts["you@example.com"] != 3 {
		t.Errorf("ChatUnreadCounts[you@example.com] = %d, want 3", counts["you@example.com"])
	}
}
