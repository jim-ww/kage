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

// The daemon counts unread messages for every attached TUI at once: a chat
// counts as read only while some client is focused on it, whichever client
// that is. Each TUI counting for itself let one in a background terminal
// count the conversation being read in another.
func TestCountUnreadSkipsChatAnyClientIsReading(t *testing.T) {
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
	resetUnreadBadge(t)

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

	sockPath := filepath.Join(dir, "kage.sock")
	ln, err := ipc.Listen(sockPath)
	if err != nil {
		t.Fatalf("listening on socket: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go srv.Accept(ln, func(ipc.ClientID, string, json.RawMessage) (any, error) { return nil, nil })

	events := make(chan ui.ChatUnreadMsg, 8)
	conn, err := ipc.Dial(sockPath, func(ev ipc.Event) {
		if ev.Kind != evChatUnread {
			return
		}
		var m ui.ChatUnreadMsg
		if err := json.Unmarshal(ev.Data, &m); err != nil {
			t.Errorf("unmarshaling ChatUnread event: %v", err)
			return
		}
		events <- m
	})
	if err != nil {
		t.Fatalf("dialing socket: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	waitForClients(t, srv, 1)

	// Our own messages and undecryptable ones never count.
	countUnread(ctx, srv, s, peer, []ui.Message{
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
	select {
	case ev := <-events:
		if ev.AccountJID != accountJID || ev.ChatAddress != peer || ev.Count != 2 {
			t.Fatalf("ChatUnread event = %+v, want %s/%s count 2", ev, accountJID, peer)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no ChatUnread event pushed to the attached client")
	}

	countUnread(ctx, srv, s, peer, []ui.Message{{Content: "only mine", IsMe: true}})
	if got := unreadCount(); got != 2 {
		t.Fatalf("unread count = %d after an own message, want 2", got)
	}

	// One client focused elsewhere, another reading the chat: not unread.
	const elsewhere, reading ipc.ClientID = 1001, 1002
	t.Cleanup(func() {
		clientFocus.forget(elsewhere)
		clientFocus.forget(reading)
	})
	clientFocus.set(elsewhere, focusState{focused: true, chatKey: focusedChatKey(accountJID, "other@example.com")})
	clientFocus.set(reading, focusState{focused: true, chatKey: focusedChatKey(accountJID, peer)})
	countUnread(ctx, srv, s, peer, []ui.Message{{Content: "seen"}})
	if got := unreadCount(); got != 2 {
		t.Fatalf("unread count = %d after a message for a chat being read, want 2", got)
	}

	// The reader's terminal loses focus: counted again.
	clientFocus.set(reading, focusState{focused: false, chatKey: focusedChatKey(accountJID, peer)})
	countUnread(ctx, srv, s, peer, []ui.Message{{Content: "unseen"}})
	if got := unreadCount(); got != 3 {
		t.Fatalf("unread count = %d after a message nobody is reading, want 3", got)
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
