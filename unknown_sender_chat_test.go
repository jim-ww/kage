package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/xmpp"
)

// attachedClient wires a real ipc.Server to a real connected client over a
// Unix socket and collects every event the daemon broadcasts, so a test can
// assert on what an attached TUI would actually receive rather than on
// daemon-internal state.
func attachedClient(t *testing.T) (*ipc.Server, chan ipc.Event) {
	t.Helper()

	srv := ipc.NewServer()
	sockPath := filepath.Join(t.TempDir(), "kage.sock")
	ln, err := ipc.Listen(sockPath)
	if err != nil {
		t.Fatalf("listening on socket: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go srv.Accept(ln, func(ipc.ClientID, string, json.RawMessage) (any, error) { return nil, nil })

	events := make(chan ipc.Event, 32)
	conn, err := ipc.Dial(sockPath, func(ev ipc.Event) {
		select {
		case events <- ev:
		default:
		}
	})
	if err != nil {
		t.Fatalf("dialing socket: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	// The broadcast only reaches a connection the server has actually
	// accepted, which happens on its own goroutine.
	deadline := time.Now().Add(2 * time.Second)
	for srv.ClientCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if srv.ClientCount() == 0 {
		t.Fatal("server never accepted the client connection")
	}
	return srv, events
}

// TestUnknownSenderChatReachesAttachedClient is the end-to-end daemon-side
// regression for "new message from an unknown account doesn't show up in
// chats until a full restart".
//
// The chat list is built from the roster, so a message from someone not in
// it had no row for any attached TUI to append to: the daemon stored the
// message and fired a desktop notification, and the UI dropped the
// IncomingMessageMsg on the floor. Asserting on the broadcast (not just on
// the roster row the daemon creates) is the point - the roster row alone
// would still leave the chat invisible until the next snapshot.
func TestUnknownSenderChatReachesAttachedClient(t *testing.T) {
	ctx := context.Background()
	srv, events := attachedClient(t)

	_, q, err := storage.Open(filepath.Join(t.TempDir(), "kage.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	s := &accountSession{account: config.Account{JID: "me@example.com"}, db: q}

	handleIncomingMessage(ctx, srv, 0, s, xmpp.MessageEvent{
		ID:     "m1",
		From:   "stranger@example.com/phone",
		Body:   "first contact",
		SentAt: time.Now(),
	})

	var added ui.ChatAddedMsg
	awaitEventOn(t, events, evChatAdded, &added, 5*time.Second)
	if added.Chat.Address != "stranger@example.com" {
		t.Errorf("ChatAdded for %q, want the stranger's bare JID", added.Chat.Address)
	}
	if added.AccountIdx != 0 {
		t.Errorf("ChatAdded AccountIdx = %d, want 0", added.AccountIdx)
	}

	// The message itself follows as an ordinary incoming message, which the
	// UI can now append because the row exists.
	var incoming ui.IncomingMessageMsg
	awaitEventOn(t, events, evIncomingMessage, &incoming, 5*time.Second)
	if incoming.From != "stranger@example.com" {
		t.Errorf("IncomingMessage From = %q", incoming.From)
	}
	if incoming.Message.Content != "first contact" {
		t.Errorf("IncomingMessage content = %q", incoming.Message.Content)
	}

	// ChatAdded carries history as of before the insert, so the message
	// arrives exactly once rather than in both events.
	if len(added.Messages) != 0 {
		t.Errorf("ChatAdded carried %d messages; the incoming message would be shown twice", len(added.Messages))
	}

	// Persisted too, or the chat is gone again on the next restart - which
	// is the half that made this look like it needed one.
	rows, err := q.ListRoster(ctx, "me@example.com")
	if err != nil {
		t.Fatalf("ListRoster: %v", err)
	}
	if len(rows) != 1 || rows[0].Jid != "stranger@example.com" {
		t.Errorf("stored roster = %+v, want one row for the stranger", rows)
	}
}

// A second message from the same stranger must not announce the chat again.
func TestUnknownSenderChatAnnouncedOnce(t *testing.T) {
	ctx := context.Background()
	srv, events := attachedClient(t)

	_, q, err := storage.Open(filepath.Join(t.TempDir(), "kage.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	s := &accountSession{account: config.Account{JID: "me@example.com"}, db: q}

	for _, id := range []string{"m1", "m2"} {
		handleIncomingMessage(ctx, srv, 0, s, xmpp.MessageEvent{
			ID: id, From: "stranger@example.com/phone", Body: "msg " + id, SentAt: time.Now(),
		})
	}

	// Drain long enough for both messages' events to have arrived.
	var added ui.ChatAddedMsg
	awaitEventOn(t, events, evChatAdded, &added, 5*time.Second)
	var second ui.IncomingMessageMsg
	awaitEventOn(t, events, evIncomingMessage, &second, 5*time.Second)
	awaitEventOn(t, events, evIncomingMessage, &second, 5*time.Second)

	for {
		select {
		case ev := <-events:
			if ev.Kind == evChatAdded {
				t.Fatal("chat was announced twice; the second message re-created it")
			}
		default:
			return
		}
	}
}

// awaitEventOn is awaitEvent without the integration build tag (see
// multi_instance_live_test.go), so the plain unit suite can use it too.
func awaitEventOn(t *testing.T, events chan ipc.Event, kind string, out any, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-events:
			if ev.Kind != kind {
				continue
			}
			if err := json.Unmarshal(ev.Data, out); err != nil {
				t.Fatalf("unmarshaling %s event: %v", kind, err)
			}
			return
		case <-deadline:
			t.Fatalf("timed out waiting for a %s event", kind)
		}
	}
}
