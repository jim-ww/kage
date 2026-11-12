package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jim-ww/kage/ui/modal"
)

// clickZone renders m, waits for zone id to be recorded and clicks it.
func clickZone(t *testing.T, m Model, id string) (Model, tea.Cmd) {
	t.Helper()
	_ = m.View()
	z := awaitZone(t, m.zone, id)
	next, cmd := m.Update(tea.MouseClickMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseLeft})
	return next.(Model), cmd
}

// modalInput is the text field in the modal on top - its body, or its
// showing tab's.
func modalInput(t *testing.T, m Model) *modal.Input {
	t.Helper()
	body := m.modals.Top().Body
	if tabs, ok := body.(*modal.Tabs); ok {
		body = tabs.Tabs[tabs.Active()].Body
	}
	in, ok := body.(*modal.Input)
	if !ok {
		t.Fatalf("modal body is %T, want a text field", body)
	}
	return in
}

// modalTitle is the title of the modal on top, or "" if none is open.
func modalTitle(m Model) string {
	if top := m.modals.Top(); top != nil {
		return top.Title
	}
	return ""
}

// TestConfirmClickable checks the delete confirmation's yes/no can be
// clicked, doing exactly what their keys do.
func TestConfirmClickable(t *testing.T) {
	m := pasteTestModel(t)
	m.selectedMsg = 0
	m.currentMessages()[0].Retracted = true
	m.actionDeleteMessage()
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "y yes") || !strings.Contains(view, "n no") {
		t.Fatal("confirmation has no yes/no buttons")
	}

	m, _ = clickZone(t, m, "modal-hint-1")
	if m.modals.Open() || len(m.currentMessages()) != 1 {
		t.Fatalf("no: open=%v messages=%d, want closed and nothing erased", m.modals.Open(), len(m.currentMessages()))
	}

	m.actionDeleteMessage()
	m, _ = clickZone(t, m, "modal-hint-0")
	if m.modals.Open() || len(m.currentMessages()) != 0 {
		t.Fatalf("yes: open=%v messages=%d, want closed and the message erased", m.modals.Open(), len(m.currentMessages()))
	}
}

// TestConfirmClickOutsideCancels checks a click elsewhere closes the
// confirmation without acting on it, and reaches nothing under it.
func TestConfirmClickOutsideCancels(t *testing.T) {
	m := pasteTestModel(t)
	m.selectedView = viewChats
	m = run(t, m, keyText("q"))
	if modalTitle(m) != "Quit kage?" {
		t.Fatalf("q opened %q, want the quit confirmation", modalTitle(m))
	}
	_ = m.View()
	awaitZone(t, m.zone, "modal-frame")
	next, cmd := m.Update(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	if next.(Model).modals.Open() || nonIdleCmd(cmd) != nil {
		t.Fatal("a click outside should close the confirmation without acting")
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
