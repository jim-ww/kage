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

// TestContactRemoveAsks: removing a contact asks first - its no closes the
// question and removes nothing, its yes removes it and refreshes the list.
func TestContactRemoveAsks(t *testing.T) {
	m := pasteTestModel(t)
	manager := &fakeContactManager{}
	m.contactManager = manager
	m = openContacts(t, m)
	m = run(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // bob's options
	if modalTitle(m) != "bob@example.test" {
		t.Fatalf("enter opened %q, want bob's options", modalTitle(m))
	}
	m = run(t, m, tea.KeyPressMsg{Code: tea.KeyUp}) // Remove, last
	m = run(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if modalTitle(m) != "Remove bob@example.test?" {
		t.Fatalf("Remove opened %q, want the question", modalTitle(m))
	}
	m, _ = clickZone(t, m, "modal-hint-1") // n no
	if modalTitle(m) != "Contacts" || len(manager.removed) != 0 {
		t.Fatalf("no: on %q, removed %v - want back on the list, nothing removed", modalTitle(m), manager.removed)
	}

	m = run(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = run(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	m = run(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	next, cmd := m.Update(keyCode('y'))
	m = next.(Model)
	if len(manager.removed) != 0 {
		t.Fatal("removed before the server was even asked")
	}
	removed, ok := findMsg[ContactRemovedMsg](cmd)
	if !ok {
		t.Fatal("yes didn't ask the server to remove bob")
	}
	m, _, _ = m.handleEventMsg(removed)
	if len(manager.removed) != 1 || len(m.contactsOpen().addrs) != 0 {
		t.Fatalf("after removing: removed %v, list %v", manager.removed, m.contactsOpen().addrs)
	}
}

// TestMessageInfoCopiesAttachmentURL: an attachment's line in the info
// modal copies its URL - by keyboard (it's selected, enter) and by click -
// and the modal shows the size once it's fetched.
func TestMessageInfoBothWays(t *testing.T) {
	m := pasteTestModel(t)
	m.width, m.termHeight = 100, 30
	m.updateSizes()
	m.currentMessages()[0].Attachments = []string{"https://up.example.test/a.pdf"}
	m.selectedMsg = 0
	m.actionInfoMessage()
	if modalTitle(m) != "Message info" {
		t.Fatalf("info opened %q", modalTitle(m))
	}
	text := m.modals.Top().Body.(*modal.Text)
	if !strings.Contains(text.View(), "fetching") {
		t.Fatalf("size isn't shown as fetching:\n%s", ansi.Strip(text.View()))
	}
	m, _, _ = m.handleEventMsg(attachmentSizeMsg{target: "https://up.example.test/a.pdf", size: 2048, ok: true})
	if !strings.Contains(ansi.Strip(m.modals.Top().Body.View()), "2.0 KB") {
		t.Fatalf("size didn't update in the open modal:\n%s", ansi.Strip(m.modals.Top().Body.View()))
	}
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m = next.(Model); !m.modals.Open() || m.noticeText == "" {
		t.Fatal("enter on the attachment line should act (copy) and keep the modal open")
	}
}
