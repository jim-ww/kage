package ui

import (
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

// hoverSelectModel is a chat with two messages, the newest selected, and
// the pointer over the older one.
func hoverSelectModel(t *testing.T, disabled bool) Model {
	t.Helper()
	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	m.mouseEnabled = true
	m.hoverSelectDisabled = disabled

	items := []list.Item{Chat{Name: "bob", Address: "bob@example.com"}}
	msgs := []Message{
		{ID: "0", Author: "bob", Content: "first", SentAt: time.Now()},
		{ID: "1", Author: "bob", Content: "second", SentAt: time.Now()},
	}
	m.accounts = []Account{{Chats: items, Messages: map[int][]Message{0: msgs}}}
	m.chats.SetItems(items)
	m.chats.Select(0)
	m.selectedView = viewChat
	m.selectedMsg = 1
	m.width, m.termHeight = 80, 20
	m.updateSizes()
	m.refreshViewport()

	_ = m.View()
	awaitZone(t, m.zone, zonePaneViewport)
	z := awaitZone(t, m.zone, zoneMessage(0))
	next, _ := m.handleMouseMotion(tea.MouseMotionMsg(tea.Mouse{X: z.StartX + 1, Y: z.StartY}))
	return next.(Model)
}

func TestHoverSelectsMessageByDefault(t *testing.T) {
	if m := hoverSelectModel(t, false); m.selectedMsg != 0 {
		t.Fatalf("selectedMsg = %d, want 0: hovering a message selects it by default", m.selectedMsg)
	}
}

func TestHoverSelectDisabled(t *testing.T) {
	m := hoverSelectModel(t, true)
	if m.selectedMsg != 1 {
		t.Fatalf("selectedMsg = %d, want 1: hover must not select with hover_select_disabled", m.selectedMsg)
	}
	if !m.isHovered(zoneMessage(0)) {
		t.Fatalf("hover = %q, want the message under the pointer to read as hovered", m.hover.id)
	}

	// A right-click still acts on the message under the pointer.
	z := m.zone.Get(zoneMessage(0))
	next, _ := m.Update(tea.MouseClickMsg(tea.Mouse{X: z.StartX + 1, Y: z.StartY, Button: tea.MouseRight}))
	m = next.(Model)
	if m.selectedMsg != 0 || menuOf(m) == nil {
		t.Fatalf("right-click: selectedMsg=%d menu=%v, want the hovered message and its menu", m.selectedMsg, menuOf(m) != nil)
	}
}
