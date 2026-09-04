package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

// newChatPaneModel builds a model showing one chat with one existing message,
// sized so the viewport has real height and its rendered output can be
// asserted on. view is the pane holding keyboard focus — the chat pane itself
// is on screen either way, since View renders it unconditionally (see
// renderChatArea).
func newChatPaneModel(t *testing.T, view selectedView, chats ...string) Model {
	t.Helper()
	if len(chats) == 0 {
		chats = []string{"bob@example.test"}
	}
	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	items := make([]list.Item, 0, len(chats))
	for _, addr := range chats {
		items = append(items, Chat{Address: addr})
	}
	m.accounts = []Account{{
		Chats:    items,
		Messages: map[int][]Message{0: {{ID: "m0", Content: "first", Author: "bob"}}},
	}}
	m.currentAccount = 0
	if cmd := m.chats.SetItems(m.accounts[0].Chats); cmd != nil {
		cmd()
	}
	m.chats.Select(0)
	m.selectedMsg = 0
	m.selectedView = view

	// Sizes the viewport (the bare test model has height 0, which renders as
	// an empty string) and establishes the pre-arrival baseline render.
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = next.(Model)
	if !strings.Contains(m.viewport.View(), "first") {
		t.Fatalf("baseline: existing message missing from rendered pane")
	}
	return m
}

// TestIncomingMessageRefreshesViewportWhileChatListFocused is the regression
// guard for messages that arrive while the chat list (not the compose box)
// holds focus. The chat pane is on screen the whole time — View renders it
// from m.viewport.View() no matter which pane is focused — so an arriving
// message has to be re-rendered into the viewport even though selectedView
// isn't viewChat. It previously wasn't: the append happened, the chat-list
// preview updated, a desktop notification fired, and the message simply never
// showed in the visible chat pane until something else (clicking the chat
// name, moving the list cursor) happened to call refreshViewport.
func TestIncomingMessageRefreshesViewportWhileChatListFocused(t *testing.T) {
	for _, view := range []selectedView{viewChats, viewAccounts, viewChat} {
		t.Run(viewName(view), func(t *testing.T) {
			m := newChatPaneModel(t, view)

			next, _ := m.Update(IncomingMessageMsg{
				AccountIdx: 0,
				From:       "bob@example.test",
				Message:    Message{ID: "m1", Content: "SENTINEL", Author: "bob"},
			})
			m = next.(Model)

			if got := len(m.accounts[0].Messages[0]); got != 2 {
				t.Fatalf("message not appended to model: have %d messages, want 2", got)
			}
			if !strings.Contains(m.viewport.View(), "SENTINEL") {
				t.Fatalf("incoming message missing from rendered chat pane while %s is focused", viewName(view))
			}
		})
	}
}

// TestIncomingMessageUnreadTracksActualViewing pins the other half of the
// split the fix introduces: re-rendering the pane must NOT be taken as "the
// user has read this". Only actually viewing the chat (selectedView ==
// viewChat) suppresses the unread bump.
func TestIncomingMessageUnreadTracksActualViewing(t *testing.T) {
	tests := []struct {
		view       selectedView
		wantUnread int
	}{
		{viewChats, 1},
		{viewAccounts, 1},
		{viewChat, 0},
	}
	for _, tt := range tests {
		t.Run(viewName(tt.view), func(t *testing.T) {
			m := newChatPaneModel(t, tt.view)

			next, cmd := m.Update(IncomingMessageMsg{
				AccountIdx: 0,
				From:       "bob@example.test",
				Message:    Message{ID: "m1", Content: "SENTINEL", Author: "bob"},
			})
			m = next.(Model)
			runCmd(cmd)

			if got := m.accounts[0].Chats[0].(Chat).Unread; got != tt.wantUnread {
				t.Fatalf("Unread = %d, want %d", got, tt.wantUnread)
			}
		})
	}
}

// TestHistorySyncedRefreshesViewportWhileChatListFocused covers the same split
// for MAM catch-up, which delivers messages that arrived while offline through
// a different handler carrying the same isChatFocused condition.
func TestHistorySyncedRefreshesViewportWhileChatListFocused(t *testing.T) {
	m := newChatPaneModel(t, viewChats)

	next, cmd := m.Update(HistorySyncedMsg{
		AccountIdx: 0,
		From:       "bob@example.test",
		Messages:   []Message{{ID: "m1", Content: "SENTINEL", Author: "bob"}},
	})
	m = next.(Model)
	runCmd(cmd)

	if !strings.Contains(m.viewport.View(), "SENTINEL") {
		t.Fatal("MAM-synced message missing from rendered chat pane while the chat list is focused")
	}
	if got := m.accounts[0].Chats[0].(Chat).Unread; got != 1 {
		t.Fatalf("Unread = %d, want 1", got)
	}
}

// TestIncomingMessageForOtherChatLeavesViewportAlone guards against the fix
// over-reaching: a message for a chat that isn't the one on screen must not
// re-render (let alone scroll) the visible pane.
func TestIncomingMessageForOtherChatLeavesViewportAlone(t *testing.T) {
	m := newChatPaneModel(t, viewChats, "bob@example.test", "carol@example.test")

	next, _ := m.Update(IncomingMessageMsg{
		AccountIdx: 0,
		From:       "carol@example.test",
		Message:    Message{ID: "c1", Content: "SENTINEL", Author: "carol"},
	})
	m = next.(Model)

	if strings.Contains(m.viewport.View(), "SENTINEL") {
		t.Fatal("message for a different chat leaked into the visible pane")
	}
}

func viewName(v selectedView) string {
	switch v {
	case viewChats:
		return "viewChats"
	case viewAccounts:
		return "viewAccounts"
	case viewChat:
		return "viewChat"
	}
	return "unknown"
}
