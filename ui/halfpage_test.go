package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestHalfPageJumpsByHalfVisibleMessages guards against a regression where
// ctrl+d/ctrl+u scrolled by half the viewport's raw line height instead of
// half its visible message count — on chats with multi-line messages
// (headers, wrapped bodies, blank separators) that line-based jump covered
// nearly every message on screen, feeling like a full-page jump instead of
// a half one.
func TestHalfPageJumpsByHalfVisibleMessages(t *testing.T) {
	const viewportHeight = 20
	m := newScrollTestModel(t, 100, viewportHeight)
	m.selectedView = viewChat
	m.keys = DefaultKeyMap

	visible := m.visibleMessageCount()
	if visible <= 0 {
		t.Fatalf("visibleMessageCount() = %d, want > 0", visible)
	}

	next, _ := m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	nm := next.(Model)
	wantDown := min(len(nm.currentMessages())-1, visible/2)
	if nm.selectedMsg != wantDown {
		t.Fatalf("ctrl+d: selectedMsg = %d, want %d (half of %d visible)", nm.selectedMsg, wantDown, visible)
	}

	next, _ = nm.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	nm2 := next.(Model)
	wantUp := max(0, wantDown-visible/2)
	if nm2.selectedMsg != wantUp {
		t.Fatalf("ctrl+u: selectedMsg = %d, want %d", nm2.selectedMsg, wantUp)
	}
}
