package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
)

// TestDaemonIPCRoundTrip spins up the same adapter+daemonServer+ipc.Server
// the real daemon builds (minus any XMPP dialing, which these calls don't
// touch), talks to it over a real Unix socket via ipcClient exactly like
// the TUI does, and checks writes, reads, errors and events all make it
// across.
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
	go srv.Accept(ln, ds.serve)

	events := make(chan any, 8)
	client := &ipcClient{events: events}
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
	if cfg.State.SidebarWidth != 42 {
		t.Errorf("SidebarWidth = %d, want 42", cfg.State.SidebarWidth)
	}

	if _, err := queries.BumpChatUnread(context.Background(), storage.BumpChatUnreadParams{
		AccountJid: "me@example.com", RosterJid: "you@example.com", Delta: 3,
	}); err != nil {
		t.Fatalf("BumpChatUnread: %v", err)
	}
	counts, err := client.ChatUnreadCounts("me@example.com")
	if err != nil {
		t.Fatalf("ChatUnreadCounts: %v", err)
	}
	if counts["you@example.com"] != 3 {
		t.Errorf("ChatUnreadCounts[you@example.com] = %d, want 3", counts["you@example.com"])
	}
	if err := client.ResetChatUnread("me@example.com", "you@example.com"); err != nil {
		t.Fatalf("ResetChatUnread: %v", err)
	}
	if counts, err = client.ChatUnreadCounts("me@example.com"); err != nil {
		t.Fatalf("ChatUnreadCounts: %v", err)
	}
	if counts["you@example.com"] != 0 {
		t.Errorf("ChatUnreadCounts[you@example.com] after reset = %d, want 0", counts["you@example.com"])
	}

	if err := client.SaveDraft("me@example.com", "you@example.com", "hello there"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	drafts, err := queries.ListChatDrafts(context.Background(), "me@example.com")
	if err != nil {
		t.Fatalf("ListChatDrafts: %v", err)
	}
	if len(drafts) != 1 || drafts[0].Rosterjid != "you@example.com" || drafts[0].Body != "hello there" || drafts[0].Encrypted {
		t.Errorf("ListChatDrafts = %+v, want one plaintext row for you@example.com = %q", drafts, "hello there")
	}

	// Saving an empty draft deletes the row rather than storing "".
	if err := client.SaveDraft("me@example.com", "you@example.com", ""); err != nil {
		t.Fatalf("SaveDraft (clear): %v", err)
	}
	drafts, err = queries.ListChatDrafts(context.Background(), "me@example.com")
	if err != nil {
		t.Fatalf("ListChatDrafts after clear: %v", err)
	}
	if len(drafts) != 0 {
		t.Errorf("ListChatDrafts after clearing = %+v, want none", drafts)

	}

	// A failed call comes back as an error, with the connection still usable.
	if msg := client.AddContact(7, "you@example.com").(ui.ContactAddedMsg); msg.Err == nil || msg.Address != "you@example.com" {
		t.Errorf("AddContact on an unknown account = %+v, want an error", msg)
	}

	// Events arrive as the ui messages they were sent as, chats and errors
	// included.
	waitForClients(t, srv, 1)
	broadcast(srv, ui.AccountConnectedMsg{Index: 1, Account: ui.Account{Name: "me@example.com", Chats: []list.Item{ui.Chat{Address: "you@example.com"}}}})
	broadcast(srv, ui.AccountConnectErrorMsg{Index: 1, Err: wireError("no route")})
	var connected ui.AccountConnectedMsg
	awaitEvent(t, events, &connected, 2*time.Second)
	if chats := connected.Account.Chats; len(chats) != 1 || chats[0].(ui.Chat).Address != "you@example.com" {
		t.Errorf("AccountConnectedMsg chats = %+v", chats)
	}
	var failed ui.AccountConnectErrorMsg
	awaitEvent(t, events, &failed, 2*time.Second)
	if failed.Err == nil || failed.Err.Error() != "no route" {
		t.Errorf("AccountConnectErrorMsg.Err = %v, want no route", failed.Err)
	}
}
