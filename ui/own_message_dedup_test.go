package ui

import (
	"testing"

	"charm.land/bubbles/v2/list"
)

// TestIncomingMessageSkipsDuplicateOwnEcho guards the client-side half of the
// multi-instance-daemon fix: adapter.go's send() now broadcasts an
// IncomingMessageMsg (IsMe: true) to every attached TUI client so siblings
// learn about a message sent from another instance - but that broadcast
// reaches the SENDING client too (ipc.Server.Broadcast has no "except the
// caller" mode), which already rendered its own local optimistic echo the
// instant the Send RPC returned (see ui/message_actions.go's
// sendCurrentInput). Without this dedup, the sender would see every message
// it sends appear twice.
//
// The dedup key is the message ID: the optimistic echo's ID is patched to
// the real one on a successful send, matching exactly what the broadcast
// carries, so a second IncomingMessageMsg for the same ID must be dropped.
func TestIncomingMessageSkipsDuplicateOwnEcho(t *testing.T) {
	m := newTestModel(nil)
	chat := Chat{Address: "bob@example.test"}
	m.accounts = []Account{{
		Chats: []list.Item{chat},
		Messages: map[int][]Message{
			0: {{ID: "abc123", Author: "me", Content: "hello from instance A", IsMe: true}},
		},
	}}
	m.currentAccount = 0
	m.chats.SetItems(m.accounts[0].Chats)
	m.chats.Select(0)
	m.selectedView = viewChat

	next, _ := m.Update(IncomingMessageMsg{
		AccountIdx: 0,
		From:       "bob@example.test",
		Message:    Message{ID: "abc123", Author: "me", Content: "hello from instance A", IsMe: true},
	})
	m = next.(Model)

	if got := len(m.accounts[0].Messages[0]); got != 1 {
		t.Fatalf("Messages after duplicate broadcast = %d, want 1 (deduped by ID)", got)
	}
}

// TestIncomingMessageFromOtherInstanceIsAppended is the positive control for
// the above: a client that never sent the message (no local optimistic echo
// with that ID already present) must still append it normally - the dedup
// must not swallow every own-message broadcast, only ones already rendered.
func TestIncomingMessageFromOtherInstanceIsAppended(t *testing.T) {
	m := newTestModel(nil)
	chat := Chat{Address: "bob@example.test"}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	m.currentAccount = 0
	m.chats.SetItems(m.accounts[0].Chats)
	m.chats.Select(0)
	m.selectedView = viewChat

	next, _ := m.Update(IncomingMessageMsg{
		AccountIdx: 0,
		From:       "bob@example.test",
		Message:    Message{ID: "abc123", Author: "me", Content: "hello from instance A", IsMe: true},
	})
	m = next.(Model)

	msgs := m.accounts[0].Messages[0]
	if len(msgs) != 1 || msgs[0].ID != "abc123" || msgs[0].Content != "hello from instance A" {
		t.Fatalf("Messages = %+v, want one message with the broadcast's content", msgs)
	}
}
