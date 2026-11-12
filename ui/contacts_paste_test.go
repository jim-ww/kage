package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// fakeContactManager records contact changes and answers them at once.
type fakeContactManager struct {
	stubDaemon
	removed []string
}

func (f *fakeContactManager) AddContact(accountIdx int, address string) tea.Msg {
	return ContactAddedMsg{AccountIdx: accountIdx, Address: address}
}

func (f *fakeContactManager) RemoveContact(accountIdx int, address string) tea.Msg {
	f.removed = append(f.removed, address)
	return ContactRemovedMsg{AccountIdx: accountIdx, Address: address}
}

func (f *fakeContactManager) ResubscribeContact(accountIdx int, address string) tea.Msg {
	return ContactResubscribedMsg{AccountIdx: accountIdx, Address: address}
}

// openContacts opens the contact manager on account 0.
func openContacts(t *testing.T, m Model) Model {
	t.Helper()
	if _, ok := m.daemon.(*fakeContactManager); !ok {
		m.daemon = &fakeContactManager{}
	}
	m.currentAccount = 0
	m.openContactManager()
	if m.contactsOpen() == nil {
		t.Fatal("the contact manager didn't open")
	}
	return m
}

// TestAddContactPaste guards the same bug the add-account form had: bracketed
// paste arrives as tea.PasteMsg, not tea.KeyMsg, so it skipped the popup's key
// interception and fell through to the view switch, never reaching the field.
func TestAddContactPaste(t *testing.T) {
	m := newTestModel(nil)
	m.accounts = []Account{{}}
	m = openContacts(t, m)

	next, _ := m.Update(keyText("a"))
	m = next.(Model)
	if modalTitle(m) != "Add contact" {
		t.Fatal("expected the add-contact input to open on 'a'")
	}

	next, _ = m.Update(tea.PasteMsg{Content: "alice@example.com"})
	m = next.(Model)
	if got := formOf(m, addContactModalID).Values()[0]; got != "alice@example.com" {
		t.Fatalf("add-contact field after paste = %q, want %q", got, "alice@example.com")
	}
}

// A paste while the contact list (not the add field) is showing must not leak
// into the compose box behind the popup.
func TestContactManagerPasteDoesNotReachCompose(t *testing.T) {
	m := newTestModel(nil)
	m.accounts = []Account{{}}
	m.selectedView = viewChat
	m = openContacts(t, m)

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
	m.openChangePasswordPopup()

	next, _ := m.Update(tea.PasteMsg{Content: "hunter2"})
	m = next.(Model)
	if got := formOf(m, storagePasswordModalID).Values()[0]; got != "hunter2" {
		t.Fatalf("new-password field after paste = %q, want %q", got, "hunter2")
	}
}
