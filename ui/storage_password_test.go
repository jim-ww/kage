package ui

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
)

type fakeStoragePasswordChanger struct {
	calls []string
	err   error
}

func (f *fakeStoragePasswordChanger) ChangeStoragePassword(newPassword string) error {
	f.calls = append(f.calls, newPassword)
	return f.err
}

// changer must satisfy both MessageSender (New's sender param type) and
// StoragePasswordChanger (asserted out of it) — mirrors fakeDraftSaver in
// draft_history_test.go.
type fakeChangerSender struct {
	fakeStoragePasswordChanger
}

func (f *fakeChangerSender) Send(int, string, string, SendOptions) (string, error) {
	return "msg-id", nil
}
func (f *fakeChangerSender) SetTyping(int, string, bool) error       { return nil }
func (f *fakeChangerSender) MarkRetracted(int, string, string) error { return nil }
func (f *fakeChangerSender) DeleteQueued(int, string) error          { return nil }

// TestChangeStoragePasswordOpenValidateSubmit exercises the popup end to
// end: the keybind opens it (only from viewAccounts), empty/mismatched
// passwords are rejected without calling the changer, and a valid
// new+confirm pair submits exactly once.
func TestChangeStoragePasswordOpenValidateSubmit(t *testing.T) {
	changer := &fakeChangerSender{}
	m := newTestModelWithSender(changer, &fakeAccountAdder{})
	m.selectedView = viewChats // not viewAccounts - the keybind should be a no-op here

	next, _ := m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl | tea.ModShift})
	m = next.(Model)
	if m.changePasswordState != nil {
		t.Fatal("ChangeStoragePassword keybind should be a no-op outside viewAccounts")
	}

	m.selectedView = viewAccounts
	next, _ = m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl | tea.ModShift})
	m = next.(Model)
	if m.changePasswordState == nil {
		t.Fatal("ChangeStoragePassword keybind should open the popup in viewAccounts")
	}

	// Submitting with nothing typed opens a confirmation popup instead of
	// calling the changer right away.
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	if m.confirmTarget != confirmDisableStorageEncryption {
		t.Fatal("expected a confirmation popup for an empty password")
	}
	if len(changer.calls) != 0 {
		t.Fatal("changer should not have been called before confirming")
	}

	// Confirming calls through with an empty password.
	next, disableCmd := m.Update(tea.KeyPressMsg{Code: 'y'})
	m = next.(Model)
	if m.confirmTarget != confirmNone {
		t.Fatal("confirmation popup should close after confirming")
	}
	if !m.changePasswordState.busy {
		t.Fatal("popup should be marked busy while the change is in flight")
	}
	disableMsg := nonIdleCmd(disableCmd)
	disableResult, ok := disableMsg.(StoragePasswordChangedMsg)
	if !ok {
		t.Fatalf("cmd produced %T, want StoragePasswordChangedMsg", disableMsg)
	}
	if len(changer.calls) != 1 || changer.calls[0] != "" {
		t.Fatalf("changer.calls = %v, want [\"\"]", changer.calls)
	}
	// Deliver the result and reopen the popup for the rest of the test.
	nm, _, _ := m.handleEventMsg(disableResult)
	m = nm
	m.changePasswordState = m.newChangePasswordForm()

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
	if m.changePasswordState.err == "" {
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
	if !m.changePasswordState.busy {
		t.Fatal("popup should be marked busy while the change is in flight")
	}
	if cmd == nil {
		t.Fatal("submitting a valid new+confirm pair should return a cmd")
	}
	msg := nonIdleCmd(cmd)
	result, ok := msg.(StoragePasswordChangedMsg)
	if !ok {
		t.Fatalf("cmd produced %T, want StoragePasswordChangedMsg", msg)
	}
	if result.Err != nil {
		t.Fatalf("unexpected error from the fake changer: %v", result.Err)
	}
	if len(changer.calls) != 2 || changer.calls[1] != "newpass" {
		t.Fatalf("changer.calls = %v, want second call to be \"newpass\"", changer.calls)
	}

	// Deliver the result message the same way Update() would.
	nm, _, _ = m.handleEventMsg(result)
	if nm.changePasswordState != nil {
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
	m.changePasswordState = m.newChangePasswordForm()
	m.changePasswordState.inputs[0].SetValue("pw")
	m.changePasswordState.inputs[1].SetValue("pw")
	m.changePasswordState.busy = true

	got, _, _ := m.handleEventMsg(StoragePasswordChangedMsg{Err: errors.New("boom")})
	if got.changePasswordState == nil {
		t.Fatal("popup should stay open on failure")
	}
	if got.changePasswordState.busy {
		t.Fatal("busy should be cleared after the result arrives, success or not")
	}
	if got.changePasswordState.err != "boom" {
		t.Fatalf("changePasswordState.err = %q, want %q", got.changePasswordState.err, "boom")
	}
}
