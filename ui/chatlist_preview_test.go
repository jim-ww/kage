package ui

import (
	"testing"

	"charm.land/bubbles/v2/list"
)

func TestChatListPreviewUpdatesOnUpdate(t *testing.T) {
	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	chat := Chat{Address: "bob@example.test"}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	m.currentAccount = 0
	m.chats.SetItems(m.accounts[0].Chats)

	next, _ := m.Update(IncomingMessageMsg{AccountIdx: 0, From: "bob@example.test", Message: Message{ID: "m1", Content: "hello"}})
	m = next.(Model)
	got := m.accounts[0].Chats[0].(Chat).LastMessage
	if got != "hello" {
		t.Fatalf("after incoming: LastMessage = %q, want %q", got, "hello")
	}
	gotList := m.chats.Items()[0].(Chat).LastMessage
	if gotList != "hello" {
		t.Fatalf("after incoming: list item LastMessage = %q, want %q", gotList, "hello")
	}

	next, _ = m.Update(MessageCorrectedMsg{AccountIdx: 0, From: "bob@example.test", ReplaceID: "m1", NewContent: "edited"})
	m = next.(Model)
	if got := m.chats.Items()[0].(Chat).LastMessage; got != "edited" {
		t.Fatalf("after correction: LastMessage = %q, want %q", got, "edited")
	}

	next, _ = m.Update(MessageReactionsMsg{AccountIdx: 0, From: "bob@example.test", MessageID: "m1", Reactions: []Reaction{{Emoji: "👍", Count: 1}}})
	m = next.(Model)
	if got := m.chats.Items()[0].(Chat).LastMessage; got == "edited" {
		t.Fatalf("after reaction: LastMessage unchanged, still %q", got)
	}

	next, _ = m.Update(MessageRetractedMsg{AccountIdx: 0, From: "bob@example.test", RetractID: "m1"})
	m = next.(Model)
	if got := m.chats.Items()[0].(Chat).LastMessage; got != "message deleted" {
		t.Fatalf("after retraction: LastMessage = %q, want %q", got, "message deleted")
	}
}

// TestChatListPreviewUpdatesOnLocalSendAndDelete guards the local/optimistic
// echo paths in message_actions.go, which mutate the current chat's messages
// directly rather than going through Update's IncomingMessageMsg-style
// handlers - a distinct code path that silently never touched
// Chat.LastMessage until this was added.
func TestChatListPreviewUpdatesOnLocalSendAndDelete(t *testing.T) {
	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	chat := Chat{Address: "bob@example.test"}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.chats.Select(0)

	m.input.SetValue("hello")
	m.sendCurrentInput()
	if got := m.chats.Items()[0].(Chat).LastMessage; got != "hello" {
		t.Fatalf("after local send: LastMessage = %q, want %q", got, "hello")
	}

	m.input.SetValue("world")
	m.sendCurrentInput()
	if got := m.chats.Items()[0].(Chat).LastMessage; got != "world" {
		t.Fatalf("after second local send: LastMessage = %q, want %q", got, "world")
	}

	m.selectedMsg = len(m.currentMessages()) - 1
	m.retractSelectedMsg()
	if got := m.chats.Items()[0].(Chat).LastMessage; got != "message deleted" {
		t.Fatalf("after deleting last message: LastMessage = %q, want %q", got, "message deleted")
	}
}
