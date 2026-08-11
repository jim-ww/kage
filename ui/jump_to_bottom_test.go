package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
)

// newTestModelWithManyMessages builds a model with a single open chat
// containing enough messages to overflow a small viewport, for tests that
// need to scroll away from the bottom.
func newTestModelWithManyMessages(n int) Model {
	msgs := make([]Message, n)
	for i := range msgs {
		msgs[i] = Message{Content: "message body"}
	}
	m := newTestModelWithSender(nil, nil)
	m.width, m.termHeight = 80, 20
	m.updateSizes()
	chat := Chat{Address: "bob@example.test"}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{0: msgs}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.chats.Select(0)
	m.selectedView = viewChat
	m.selectedMsg = n - 1
	m.refreshViewport()
	m.viewport.GotoBottom()
	return m
}

// TestJumpToBottomButtonShowsOnlyPastFirstPage guards the floating button's
// show/hide condition: hidden at the bottom, still hidden after scrolling up
// by less than one full screen (the first page the user would see anyway if
// they just scrolled the rest of the way down), and shown once scrolled up
// by more than that.
func TestJumpToBottomButtonShowsOnlyPastFirstPage(t *testing.T) {
	m := newTestModelWithManyMessages(50)

	atBottom := m.renderChatArea(m.styles.colors)
	if strings.Contains(atBottom, "jump to latest") {
		t.Fatal("jump-to-latest button shown while already at the bottom")
	}

	height := m.viewport.Height()
	m.viewport.SetYOffset(m.viewport.TotalLineCount() - height - 1)
	withinFirstPage := m.renderChatArea(m.styles.colors)
	if strings.Contains(withinFirstPage, "jump to latest") {
		t.Fatal("jump-to-latest button shown while still within the first page scrolled up")
	}

	m.viewport.GotoTop()
	pastFirstPage := m.renderChatArea(m.styles.colors)
	if !strings.Contains(pastFirstPage, "jump to latest") {
		t.Fatal("jump-to-latest button not shown after scrolling past the first page")
	}
}

// TestJumpToLatestMessage guards jumpToLatestMessage: it selects the newest
// message and scrolls the viewport back to the bottom.
func TestJumpToLatestMessage(t *testing.T) {
	m := newTestModelWithManyMessages(50)
	m.viewport.GotoTop()
	m.selectedMsg = 0

	m.jumpToLatestMessage()

	if !m.viewport.AtBottom() {
		t.Fatal("viewport not at bottom after jumpToLatestMessage")
	}
	if m.selectedMsg != 49 {
		t.Fatalf("selectedMsg = %d, want 49 (the newest message)", m.selectedMsg)
	}
}
