package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestInputBorderGrabbedAboveIt: the input box's one-row top border is
// grabbed from the blank row above it too, and dragging it up grows the
// box.
func TestInputBorderGrabbedAboveIt(t *testing.T) {
	m := newTestModelWithManyMessages(5)
	m.width, m.termHeight = 100, 30
	m.updateSizes()
	_ = m.View()
	z := m.zone.Get(zonePaneInput)
	if z.IsZero() {
		t.Fatal("test setup: no input zone")
	}
	before := m.input.Height()

	m = updated(t, m, tea.MouseClickMsg{X: z.StartX + 5, Y: z.StartY - 1, Button: tea.MouseLeft})
	if !m.resizingInput {
		t.Fatal("click above the border didn't grab it")
	}
	m = updated(t, m, tea.MouseMotionMsg{X: z.StartX + 5, Y: z.StartY - 4, Button: tea.MouseLeft})
	if m.input.Height() <= before {
		t.Errorf("input height %d after dragging up, want more than %d", m.input.Height(), before)
	}
}

// TestInputBorderNotGrabbedFromSidebar: a click in the chat list on the
// border's row doesn't grab it.
func TestInputBorderNotGrabbedFromSidebar(t *testing.T) {
	m := newTestModelWithManyMessages(5)
	m.width, m.termHeight = 100, 30
	m.updateSizes()
	_ = m.View()
	z := m.zone.Get(zonePaneInput)
	if z.IsZero() || z.StartX < 2 {
		t.Fatal("test setup: no input zone right of the chat list")
	}
	m = updated(t, m, tea.MouseClickMsg{X: 1, Y: z.StartY, Button: tea.MouseLeft})
	if m.resizingInput {
		t.Error("click in the chat list grabbed the input border")
	}
}
