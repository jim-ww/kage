package ui

import "testing"

// TestReplyHintKeepsTailFollowed: replying to the newest message takes a
// row for the reply hint; the viewport stays at the bottom, so a message
// arriving next is shown instead of hidden behind the hint.
func TestReplyHintKeepsTailFollowed(t *testing.T) {
	m := newTestModelWithManyMessages(50)
	for i := range m.accounts[0].Messages[0] {
		m.accounts[0].Messages[0][i].ID = string(rune('a' + i%26))
	}
	m.refreshViewport()
	m.viewport.GotoBottom()

	m.actionReplyMessage()
	if !m.viewport.AtBottom() {
		t.Fatal("viewport left the bottom when the reply hint appeared")
	}

	m = updated(t, m, IncomingMessageMsg{AccountIdx: 0, From: "bob@example.test", Message: Message{ID: "new", Content: "new one"}})
	if !m.viewport.AtBottom() {
		t.Error("message arriving while replying isn't shown: viewport not at the bottom")
	}
}
