package ui

import (
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

// TestChatListNavigationDebouncesMessageRefresh guards the fix for a slow
// chat-list cursor: refreshViewport() re-renders every loaded message
// (ansi-wrapping/styling, up to maxMessagesPerChat of them) and used to run
// synchronously on every single chat-list index change. Holding the cursor
// key re-ran it once per chat skipped through on the way to wherever the
// key repeat landed, visibly lagging the list itself. Moving the selection
// must no longer refresh immediately — only once a chatSwitchSettledMsg
// with a still-current generation actually arrives.
func TestChatListNavigationDebouncesMessageRefresh(t *testing.T) {
	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	bob := Chat{Address: "bob@example.test", Name: "bob"}
	alice := Chat{Address: "alice@example.test", Name: "alice"}
	m.accounts = []Account{{
		Chats: []list.Item{bob, alice},
		Messages: map[int][]Message{
			0: {{ID: "b1"}},
			1: {{ID: "a1"}, {ID: "a2"}},
		},
	}}
	m.currentAccount = 0
	m.chats.SetItems(m.accounts[0].Chats)
	m.chats.Select(0)
	m.selectedView = viewChats
	m.selectedMsg = 0
	m.refreshViewport()
	genBefore := m.chatSwitchGen

	// cmd itself is discarded rather than invoked: it's a real tea.Tick,
	// which blocks for its full duration when called (see commands.go's
	// Tick) — and this Update call also arms the 10-minute idle timer in
	// the same batch, since a KeyMsg counts as activity. What's asserted
	// here is just that Update scheduled some command (the debounce timer)
	// instead of refreshing synchronously; chatSwitchSettledMsg's own
	// gen-check behavior is exercised directly via a hand-built message.
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("expected a command to be scheduled (the debounce timer)")
	}
	if m.chats.Index() != 1 {
		t.Fatalf("chats.Index() = %d, want 1", m.chats.Index())
	}
	if m.chatSwitchGen == genBefore {
		t.Fatal("chatSwitchGen should have advanced on a selection change")
	}
	if m.selectedMsg != 0 {
		t.Fatalf("selectedMsg should not have jumped to the newly selected chat's tail yet, got %d", m.selectedMsg)
	}

	msg := chatSwitchSettledMsg{gen: m.chatSwitchGen}
	next, _ = m.Update(msg)
	m = next.(Model)
	if m.selectedMsg != 1 {
		t.Fatalf("after settling, selectedMsg = %d, want 1 (alice's last message)", m.selectedMsg)
	}

	// A stale settle (superseded by a later move) must be ignored.
	m.chatSwitchGen++
	stale := chatSwitchSettledMsg{gen: msg.gen}
	m.selectedMsg = 0
	next, _ = m.Update(stale)
	m = next.(Model)
	if m.selectedMsg != 0 {
		t.Fatalf("stale chatSwitchSettledMsg should have been ignored, selectedMsg = %d", m.selectedMsg)
	}
}
