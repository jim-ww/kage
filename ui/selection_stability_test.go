package ui

import (
	"testing"
	"time"
)

// TestIncomingKeepsSelectionOnPickedMessage covers picking a message to
// reply to, then something arriving before ctrl+r: the selection used to
// jump to the newest message, so the reply went to that instead. It must
// stay on the picked message, shifted by the front trim.
func TestIncomingKeepsSelectionOnPickedMessage(t *testing.T) {
	m := newEditTestModel(t, &fakeFileSender{})
	m.selectedMsg = 1 // "b"

	m, _, _ = m.handleEventMsg(incomingFrom("theirs", time.Now().Add(time.Hour)))

	msgs := m.currentMessages()
	if got := msgs[m.selectedMsg].ID; got != "b" {
		t.Fatalf("selection moved to %q, want %q", got, "b")
	}
	_ = m.actionReplyMessage()
	if got := m.pendingReplyOptions().ReplyToID; got != "b" {
		t.Fatalf("reply target = %q, want %q", got, "b")
	}
}

// TestIncomingKeepsBottomScrollWithoutMovingSelection: a chat scrolled to
// the bottom stays there when a message arrives, while the selection stays
// on the message it was on.
func TestIncomingKeepsBottomScrollWithoutMovingSelection(t *testing.T) {
	m := newLimitTestModel(t, 60, 100)
	m.selectedMsg = 59
	m.viewport.GotoBottom()

	updated, _, _ := m.handleEventMsg(IncomingMessageMsg{
		AccountIdx: 0, From: "bob@example.com",
		Message: Message{ID: "new", Author: "bob", Content: "hi", SentAt: time.Now().Add(time.Hour)},
	})

	if updated.selectedMsg != 59 {
		t.Fatalf("selectedMsg = %d, want 59", updated.selectedMsg)
	}
	if !updated.viewport.AtBottom() {
		t.Fatal("viewport left the bottom after an incoming message")
	}
}
