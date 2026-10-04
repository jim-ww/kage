package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestAddContactPaste guards the same bug the add-account form had: bracketed
// paste arrives as tea.PasteMsg, not tea.KeyMsg, so it skipped the popup's key
// interception and fell through to the view switch, never reaching the field.
func TestAddContactPaste(t *testing.T) {
	m := newTestModel(nil)
	m.accounts = []Account{{}}
	m.contactManagerState = &contactManagerState{accountIdx: 0}

	next, _ := m.Update(keyText("a"))
	m = next.(Model)
	if !m.contactManagerState.adding {
		t.Fatal("expected the add-contact input to open on 'a'")
	}

	next, _ = m.Update(tea.PasteMsg{Content: "alice@example.com"})
	m = next.(Model)
	if got := m.contactManagerState.addInput.Value(); got != "alice@example.com" {
		t.Fatalf("add-contact field after paste = %q, want %q", got, "alice@example.com")
	}
}

// A paste while the contact list (not the add field) is showing must not leak
// into the compose box behind the popup.
func TestContactManagerPasteDoesNotReachCompose(t *testing.T) {
	m := newTestModel(nil)
	m.accounts = []Account{{}}
	m.selectedView = viewChat
	m.contactManagerState = &contactManagerState{accountIdx: 0}

	next, _ := m.Update(tea.PasteMsg{Content: "leaked"})
	m = next.(Model)
	if got := m.input.Value(); got != "" {
		t.Fatalf("compose box = %q, want the popup to swallow the paste", got)
	}
}

// TestChangePasswordPaste covers the same routing gap for the change-storage-
// password popup.
func TestChangePasswordPaste(t *testing.T) {
	m := newTestModel(nil)
	m.changePasswordState = m.newChangePasswordForm()

	next, _ := m.Update(tea.PasteMsg{Content: "hunter2"})
	m = next.(Model)
	if got := m.changePasswordState.inputs[0].Value(); got != "hunter2" {
		t.Fatalf("new-password field after paste = %q, want %q", got, "hunter2")
	}
}
