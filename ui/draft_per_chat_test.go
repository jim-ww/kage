package ui

import (
	"testing"

	"charm.land/bubbles/v2/list"
)

// TestEnterChatPaneSwapsDraft: focusing the chat pane some way other than
// enter (hovering it, dropping a file) after picking another chat in the
// list loads that chat's draft, and keeps the one typed for the last.
func TestEnterChatPaneSwapsDraft(t *testing.T) {
	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	alice := Chat{Name: "alice", Address: "alice@example.test"}
	bob := Chat{Name: "bob", Address: "bob@example.test", Draft: "for bob"}
	chats := []list.Item{alice, bob}
	m.accounts = []Account{{Name: "me@example.test", Chats: chats, Messages: map[int][]Message{}}}
	m.chats.SetItems(chats)
	m.chats.Select(0)
	next, _ := m.openCurrentChat()
	m = next.(Model)
	m.input.SetValue("for alice")

	m.setSelectedView(viewChats)
	m.chats.Select(1)
	m.enterChatPane()

	if got := m.input.Value(); got != "for bob" {
		t.Errorf("compose box = %q, want bob's draft", got)
	}
	if got := m.accounts[0].Chats[0].(Chat).Draft; got != "for alice" {
		t.Errorf("alice's draft = %q, want what was typed for her", got)
	}
}
