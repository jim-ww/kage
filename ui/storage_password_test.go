package ui

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jim-ww/kage/ui/modal"
)

type fakeStoragePasswordChanger struct {
	stubDaemon
	calls []string
	err   error
}

// findMsg runs cmd - a batch, possibly - and returns the first message of
// type T it produces, skipping the idle timer.
func findMsg[T any](cmd tea.Cmd) (T, bool) {
	var zero T
	if cmd == nil || isIdleTimerCmd(cmd) {
		return zero, false
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if t, ok := findMsg[T](c); ok {
				return t, true
			}
		}
		return zero, false
	}
	t, ok := msg.(T)
	return t, ok
}

// formOf is the open form modal with id, or nil.
func formOf(m Model, id string) *modal.Form {
	if md := m.modals.Find(id); md != nil {
		return md.Body.(*modal.Form)
	}
	return nil
}

func (f *fakeStoragePasswordChanger) ChangeStoragePassword(newPassword string) error {
	f.calls = append(f.calls, newPassword)
	return f.err
}

// changer must satisfy both Daemon (New's sender param type) and
// Daemon (asserted out of it) — mirrors fakeDraftSaver in
// draft_history_test.go.
type fakeChangerSender struct {
	fakeStoragePasswordChanger
}

func (f *fakeChangerSender) Send(int, string, string, SendOptions) (string, error) {
	return "msg-id", nil
}
func (f *fakeChangerSender) SetTyping(int, string, bool) error       { return nil }
func (f *fakeChangerSender) MarkRetracted(int, string, string) error { return nil }
func (f *fakeChangerSender) PurgeMessage(int, string, string) error  { return nil }
func (f *fakeChangerSender) DeleteQueued(int, string) error          { return nil }

// TestChangeStoragePasswordOpenValidateSubmit exercises the popup end to
// end: the account menu opens it (it has no keybinding of its own —
// Ctrl+Shift+P was redundant with the menu and gated to the accounts panel
// anyway), empty/mismatched passwords are rejected without calling the
// changer, and a valid new+confirm pair submits exactly once.
func TestChangeStoragePasswordOpenValidateSubmit(t *testing.T) {
	changer := &fakeChangerSender{}
	m := newTestModelWithSender(changer, &fakeAccountAdder{})
	m.accounts = []Account{{Name: "me"}}
	m.currentAccount = 0

	if cmd := m.openAccountMenu(); cmd != nil {
		t.Fatalf("openAccountMenu returned a notification: %v", cmd())
	}
	idx := -1
	for i, item := range menuOf(m).items {
		if item.label == "Storage password" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("the account menu does not offer storage-password rotation")
	}
	menuOf(m).SetCursor(idx)
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	if formOf(m, storagePasswordModalID) == nil {
		t.Fatal("the account menu's storage-password entry did not open the popup")
	}

	// Submitting with nothing typed opens a confirmation popup instead of
	// calling the changer right away.
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	if modalTitle(m) != "Disable local storage encryption?" {
		t.Fatal("expected a confirmation popup for an empty password")
	}
	if len(changer.calls) != 0 {
		t.Fatal("changer should not have been called before confirming")
	}

	// Confirming calls through with an empty password.
	next, disableCmd := m.Update(tea.KeyPressMsg{Code: 'y'})
	m = next.(Model)
	if modalTitle(m) != "Change storage password" {
		t.Fatal("confirmation popup should close after confirming, back to the form")
	}
	if !formOf(m, storagePasswordModalID).Busy() {
		t.Fatal("popup should be marked busy while the change is in flight")
	}
	disableResult, ok := findMsg[StoragePasswordChangedMsg](disableCmd)
	if !ok {
		t.Fatal("confirming didn't start the change")
	}
	if len(changer.calls) != 1 || changer.calls[0] != "" {
		t.Fatalf("changer.calls = %v, want [\"\"]", changer.calls)
	}
	// Deliver the result and reopen the popup for the rest of the test.
	nm, _, _ := m.handleEventMsg(disableResult)
	m = nm
	if formOf(m, storagePasswordModalID) != nil {
		t.Fatal("popup should close on success")
	}
	m.openChangePasswordPopup()

	for _, r := range "newpass" {
		next, _ = m.Update(tea.KeyPressMsg{Text: string(r), Code: r})
		m = next.(Model)
	}
	// Tab to the confirm field and type a mismatched value.
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = next.(Model)
	for _, r := range "different" {
		next, _ = m.Update(tea.KeyPressMsg{Text: string(r), Code: r})
		m = next.(Model)
	}
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	if formOf(m, storagePasswordModalID).Err() == "" {
		t.Fatal("expected an error for mismatched passwords")
	}
	if len(changer.calls) != 1 {
		t.Fatal("changer should not have been called again for mismatched passwords")
	}

	// Fix the confirm field to match, then submit for real.
	for range "different" {
		next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
		m = next.(Model)
	}
	for _, r := range "newpass" {
		next, _ = m.Update(tea.KeyPressMsg{Text: string(r), Code: r})
		m = next.(Model)
	}
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	if !formOf(m, storagePasswordModalID).Busy() {
		t.Fatal("popup should be marked busy while the change is in flight")
	}
	if cmd == nil {
		t.Fatal("submitting a valid new+confirm pair should return a cmd")
	}
	result, ok := findMsg[StoragePasswordChangedMsg](cmd)
	if !ok {
		t.Fatal("submitting didn't start the change")
	}
	if result.Err != nil {
		t.Fatalf("unexpected error from the fake changer: %v", result.Err)
	}
	if len(changer.calls) != 2 || changer.calls[1] != "newpass" {
		t.Fatalf("changer.calls = %v, want second call to be \"newpass\"", changer.calls)
	}

	// Deliver the result message the same way Update() would.
	nm, _, _ = m.handleEventMsg(result)
	if formOf(nm, storagePasswordModalID) != nil {
		t.Fatal("popup should close on success")
	}
}

// TestChangeStoragePasswordFailureKeepsPopupOpen checks a failed change
// surfaces the error and leaves the popup open (so the user can retry or
// read what went wrong) instead of silently closing.
func TestChangeStoragePasswordFailureKeepsPopupOpen(t *testing.T) {
	changer := &fakeChangerSender{}
	changer.fakeStoragePasswordChanger.err = errors.New("boom")
	m := newTestModelWithSender(changer, &fakeAccountAdder{})
	m.selectedView = viewAccounts
	m.openChangePasswordPopup()
	formOf(m, storagePasswordModalID).SetBusy("re-encrypting local storage…")

	got, _, _ := m.handleEventMsg(StoragePasswordChangedMsg{Err: errors.New("boom")})
	form := formOf(got, storagePasswordModalID)
	if form == nil {
		t.Fatal("popup should stay open on failure")
	}
	if form.Busy() {
		t.Fatal("busy should be cleared after the result arrives, success or not")
	}
	if form.Err() != "boom" {
		t.Fatalf("error = %q, want %q", form.Err(), "boom")
	}
}
