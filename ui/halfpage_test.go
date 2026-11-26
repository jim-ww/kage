package ui

import (
	"strings"
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
	keys := DefaultKeyMap
	m.keys = &keys

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

// TestHalfPageMovesInMultilineDraft: in a draft of several lines,
// ctrl+u/ctrl+d move its cursor by half the box, and page messages only
// once the cursor is at the draft's top.
func TestHalfPageMovesInMultilineDraft(t *testing.T) {
	m := newTestModelWithManyMessages(50)
	m.input.SetValue(strings.Repeat("line\n", 19) + "line")
	m.updateSizes()
	half := max(1, m.input.Height()/2)
	selected := m.selectedMsg

	m = updated(t, m, tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	if got := m.input.Line(); got != 19-half {
		t.Errorf("ctrl+u: draft line %d, want %d", got, 19-half)
	}
	m = updated(t, m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if got := m.input.Line(); got != 19 {
		t.Errorf("ctrl+d: draft line %d, want 19", got)
	}
	if m.selectedMsg != selected {
		t.Fatalf("moving in the draft selected message %d, was %d", m.selectedMsg, selected)
	}

	m.input.MoveToBegin()
	m = updated(t, m, tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	if m.selectedMsg >= selected {
		t.Errorf("ctrl+u at the draft's top: selected message %d, want above %d", m.selectedMsg, selected)
	}
}
