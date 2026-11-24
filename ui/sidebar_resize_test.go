package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestSidebarBorderGrabbedBesideIt: the sidebar's one-column border is
// grabbed from the column right of it too, and dragging resizes the list.
func TestSidebarBorderGrabbedBesideIt(t *testing.T) {
	m := newTestModelWithManyMessages(5)
	m.width, m.termHeight = 100, 30
	m.updateSizes()
	_ = m.View()
	z := m.zone.Get(zonePaneSidebar)
	if z.IsZero() {
		t.Fatal("test setup: no sidebar zone")
	}
	before := m.sidebarWidth()

	m = updated(t, m, tea.MouseClickMsg{X: z.EndX + 1, Y: 10, Button: tea.MouseLeft})
	if !m.resizingSidebar {
		t.Fatal("click right of the border didn't grab it")
	}
	m = updated(t, m, tea.MouseMotionMsg{X: z.EndX + 6, Y: 10, Button: tea.MouseLeft})
	if m.sidebarWidth() <= before {
		t.Errorf("sidebar width %d after dragging right, want more than %d", m.sidebarWidth(), before)
	}
}
