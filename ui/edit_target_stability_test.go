package ui

import (
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
)

// newEditTestModel builds a chat of three messages - two from Bob, then one
// of ours - already at the message cap, so the next arrival trims the oldest
// off the front and renumbers everything left.
func newEditTestModel(t *testing.T, sender MessageSender) Model {
	t.Helper()
	m := newTestModelWithSender(sender, nil)
	m.maxMessagesPerChat = 3
	chat := Chat{Name: "Bob", Address: "bob@example.test"}
	base := time.Now()
	m.accounts = []Account{{
		Chats: []list.Item{chat},
		Messages: map[int][]Message{
			0: {
				{ID: "a", Author: "Bob", Content: "one", SentAt: base},
				{ID: "b", Author: "Bob", Content: "two", SentAt: base.Add(time.Second)},
				{ID: "mine", Author: "me", IsMe: true, Content: "helo", SentAt: base.Add(2 * time.Second)},
			},
		},
	}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.selectedView = viewChat
	m.selectedMsg = 2
	return m
}

func incomingFrom(bob string, at time.Time) IncomingMessageMsg {
	return IncomingMessageMsg{
		AccountIdx: 0, From: "bob@example.test",
		Message: Message{ID: bob, Author: "Bob", Content: "hi", SentAt: at},
	}
}

// TestEditSavesToTheRightMessageAfterIncomingTrim covers the reported flow:
// start editing your last message, the other side sends one while you type,
// hit save. The arrival trims the oldest message off the front of a chat
// that's at maxMessagesPerChat, so every remaining message shifts down one -
// and an edit target remembered as an index then names the message that slid
// into its place. It used to rewrite that neighbour (here, the peer's
// just-arrived message) and send a XEP-0308 correction carrying the peer's
// own stanza ID, leaving the message actually being edited untouched.
func TestEditSavesToTheRightMessageAfterIncomingTrim(t *testing.T) {
	sender := &fakeFileSender{}
	m := newEditTestModel(t, sender)

	_ = m.actionEditMessage()
	if m.editingMsg.empty() {
		t.Fatal("actionEditMessage did not start an edit")
	}

	updated, _, handled := m.handleEventMsg(incomingFrom("theirs", time.Now().Add(time.Hour)))
	if !handled {
		t.Fatal("IncomingMessageMsg was not handled")
	}
	m = updated

	m.input.SetValue("hello")
	_ = m.sendCurrentInput()

	msgs := m.accounts[0].Messages[0]
	if len(msgs) != 3 || msgs[0].ID != "b" { // "a" trimmed off the front
		t.Fatalf("unexpected messages after trim: %+v", msgs)
	}
	mine := msgs[messageIndexByID(msgs, "mine")]
	if mine.Content != "hello" || !mine.Edited {
		t.Fatalf("edit was not applied to our own message: content=%q edited=%v", mine.Content, mine.Edited)
	}
	theirs := msgs[messageIndexByID(msgs, "theirs")]
	if theirs.Content != "hi" || theirs.Edited {
		t.Fatalf("edit leaked onto the peer's message: content=%q edited=%v", theirs.Content, theirs.Edited)
	}
	if len(sender.sendCalls) != 1 {
		t.Fatalf("want exactly one Send call for the correction, got %#v", sender.sendCalls)
	}
	if got := sender.sendCalls[0].opts.ReplaceID; got != "mine" {
		t.Fatalf("correction ReplaceID = %q, want %q", got, "mine")
	}
	if !m.editingMsg.empty() {
		t.Fatal("edit state not cleared after saving")
	}
}

// TestEditOfMessageGoneFromWindowIsNotSavedElsewhere covers the other way the
// target can move: it's no longer in the loaded slice at all. The edit has
// nowhere to go, and must not be written onto whatever occupies its old
// position.
func TestEditOfMessageGoneFromWindowIsNotSavedElsewhere(t *testing.T) {
	sender := &fakeFileSender{}
	m := newEditTestModel(t, sender)

	_ = m.actionEditMessage()

	// A history window reload lands a window that no longer holds our message.
	base := time.Now()
	updated, _, handled := m.handleEventMsg(HistoryWindowMsg{
		AccountIdx: 0, From: "bob@example.test",
		Messages: []Message{
			{ID: "x", Author: "Bob", Content: "older one", SentAt: base.Add(-2 * time.Hour)},
			{ID: "y", Author: "Bob", Content: "older two", SentAt: base.Add(-time.Hour)},
		},
		HasNewer: true,
	})
	if !handled {
		t.Fatal("HistoryWindowMsg was not handled")
	}
	m = updated

	m.input.SetValue("hello")
	_ = m.sendCurrentInput()

	for _, mm := range m.accounts[0].Messages[0] {
		if mm.Content == "hello" || mm.Edited {
			t.Fatalf("edit was written onto an unrelated message: %+v", mm)
		}
	}
	if len(sender.sendCalls) != 0 {
		t.Fatalf("no correction should have been sent, got %#v", sender.sendCalls)
	}
	if !m.editingMsg.empty() {
		t.Fatal("edit state not cleared")
	}
}
