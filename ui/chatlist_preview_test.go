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
