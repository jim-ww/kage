package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// clickZone renders m, waits for zone id to be recorded and clicks it.
func clickZone(t *testing.T, m Model, id string) (Model, tea.Cmd) {
	t.Helper()
	_ = m.View()
	z := awaitZone(t, m.zone, id)
	next, cmd := m.Update(tea.MouseClickMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseLeft})
	return next.(Model), cmd
}

// TestConfirmPopupClickable checks the delete confirmation's yes/no can be
// clicked, doing exactly what their keys do.
func TestConfirmPopupClickable(t *testing.T) {
	m := pasteTestModel(t)
	m.confirmTarget = confirmPurgeMessage
	m.selectedMsg = 0
	if view := m.View().Content; !strings.Contains(view, "[y] yes") || !strings.Contains(view, "[n] no") {
		t.Fatal("confirmation has no yes/no buttons")
	}

	m, _ = clickZone(t, m, zoneConfirmNo)
	if m.confirmTarget != confirmNone || len(m.currentMessages()) != 1 {
		t.Fatalf("no: target=%v messages=%d, want closed and nothing erased", m.confirmTarget, len(m.currentMessages()))
	}

	m.confirmTarget = confirmPurgeMessage
	m, _ = clickZone(t, m, zoneConfirmYes)
	if m.confirmTarget != confirmNone || len(m.currentMessages()) != 0 {
		t.Fatalf("yes: target=%v messages=%d, want closed and the message erased", m.confirmTarget, len(m.currentMessages()))
	}
}

// TestConfirmPopupIgnoresOtherClicks checks a click elsewhere neither
// confirms nor reaches what's under the popup.
func TestConfirmPopupIgnoresOtherClicks(t *testing.T) {
	m := pasteTestModel(t)
	m.confirmTarget = confirmQuit
	_ = m.View()
	next, cmd := m.Update(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	if next.(Model).confirmTarget != confirmQuit || nonIdleCmd(cmd) != nil {
		t.Fatal("a click outside the buttons acted on the confirmation")
	}
}

// TestContactRemoveConfirmClickable covers the contact manager's own
// confirmation, which shares the buttons.
func TestContactRemoveConfirmClickable(t *testing.T) {
	m := pasteTestModel(t)
	m.contactManagerState = &contactManagerState{pendingRemove: "bob@example.test"}
	m, _ = clickZone(t, m, zoneConfirmNo)
	if m.contactManagerState.pendingRemove != "" {
		t.Fatal("no didn't cancel the contact removal")
	}
}
