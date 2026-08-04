package ui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// fakeAccountAdder is a stub AccountAdder for testing the add-account form
// without any real network/config dependency: AddAccount just records the
// last call and returns whatever msg/err was configured.
type fakeAccountAdder struct {
	lastJID, lastPassword, lastGPGKeyID string
	calls                               int
	err                                 error
}

func (f *fakeAccountAdder) AddAccount(jid, password, gpgKeyID string) tea.Msg {
	f.calls++
	f.lastJID, f.lastPassword, f.lastGPGKeyID = jid, password, gpgKeyID
	if f.err != nil {
		return AccountAddErrorMsg{Err: f.err}
	}
	return AccountAddedMsg{Account: Account{Name: jid}}
}

func keyText(s string) tea.KeyMsg {
	return tea.KeyPressMsg{Text: s, Code: rune(s[0])}
}

func keyCode(code rune) tea.KeyMsg {
	return tea.KeyPressMsg{Code: code}
}

func newTestModel(adder AccountAdder) Model {
	return newTestModelWithSender(nil, adder)
}

func newTestModelWithSender(sender MessageSender, adder AccountAdder) Model {
	m := New(nil, 0, DefaultKeyMap, DefaultTheme(), sender, adder, true, 0, false, "", 0, DisplayOptions{TimeOnlyToday: true})
	m.width, m.height = 80, 24
	m.updateSizes()
	return m
}

// TestAddAccountFormOpenAndCancel checks the form opens from the accounts
// panel via the AddAccount binding and esc cancels it without side effects.
func TestAddAccountFormOpenAndCancel(t *testing.T) {
	adder := &fakeAccountAdder{}
	m := newTestModel(adder)
	m.selectedView = viewAccounts

	next, _ := m.Update(keyText("a"))
	m = next.(Model)
	if !m.addingAccount {
		t.Fatal("expected addingAccount to be true after pressing 'a'")
	}

	next, _ = m.Update(keyCode(tea.KeyEscape))
	m = next.(Model)
	if m.addingAccount {
		t.Fatal("expected addingAccount to be false after esc")
	}
	if adder.calls != 0 {
		t.Fatalf("expected no AddAccount calls, got %d", adder.calls)
	}
}

// TestAddAccountFormFieldNavigationAndSubmit walks through typing a JID, tabbing
// to the password and GPG key fields, and submitting — verifying the values
// reach AccountAdder.AddAccount and a successful result appends the account
// and closes the form.
func TestAddAccountFormFieldNavigationAndSubmit(t *testing.T) {
	adder := &fakeAccountAdder{}
	m := newTestModel(adder)
	m.selectedView = viewAccounts

	next, _ := m.Update(keyText("a"))
	m = next.(Model)

	for _, r := range "alice@example.com" {
		next, _ = m.Update(keyText(string(r)))
		m = next.(Model)
	}

	next, _ = m.Update(keyCode(tea.KeyTab))
	m = next.(Model)
	if m.addAccountFocus != 1 {
		t.Fatalf("expected focus on password field (1), got %d", m.addAccountFocus)
	}
	for _, r := range "hunter2" {
		next, _ = m.Update(keyText(string(r)))
		m = next.(Model)
	}

	next, _ = m.Update(keyCode(tea.KeyTab))
	m = next.(Model)
	if m.addAccountFocus != 2 {
		t.Fatalf("expected focus on gpg key field (2), got %d", m.addAccountFocus)
	}
	for _, r := range "ABCD1234" {
		next, _ = m.Update(keyText(string(r)))
		m = next.(Model)
	}

	next, cmd := m.Update(keyCode(tea.KeyEnter))
	m = next.(Model)
	if !m.addAccountBusy {
		t.Fatal("expected addAccountBusy to be true right after submit")
	}
	if cmd == nil {
		t.Fatal("expected a tea.Cmd to run AddAccount")
	}

	msg := cmd()
	next, _ = m.Update(msg)
	m = next.(Model)

	if adder.calls != 1 {
		t.Fatalf("expected exactly 1 AddAccount call, got %d", adder.calls)
	}
	if adder.lastJID != "alice@example.com" || adder.lastPassword != "hunter2" || adder.lastGPGKeyID != "ABCD1234" {
		t.Fatalf("got jid=%q password=%q gpgKeyID=%q", adder.lastJID, adder.lastPassword, adder.lastGPGKeyID)
	}
	if m.addingAccount {
		t.Fatal("expected form to close on success")
	}
	if len(m.accounts) != 1 || m.accounts[0].Name != "alice@example.com" {
		t.Fatalf("expected the new account to be appended, got %+v", m.accounts)
	}
}

// TestAddAccountFormSubmitErrorKeepsFormOpen checks that a failed AddAccount
// call leaves the form open (with the error shown) so the user can retry
// instead of silently losing what they typed.
func TestAddAccountFormSubmitErrorKeepsFormOpen(t *testing.T) {
	adder := &fakeAccountAdder{err: errors.New("connection refused")}
	m := newTestModel(adder)
	m.selectedView = viewAccounts

	next, _ := m.Update(keyText("a"))
	m = next.(Model)
	for _, r := range "bob@example.com" {
		next, _ = m.Update(keyText(string(r)))
		m = next.(Model)
	}

	next, cmd := m.Update(keyCode(tea.KeyEnter))
	m = next.(Model)
	msg := cmd()
	next, _ = m.Update(msg)
	m = next.(Model)

	if !m.addingAccount {
		t.Fatal("expected form to stay open after an error")
	}
	if m.addAccountBusy {
		t.Fatal("expected addAccountBusy to be cleared after the error result")
	}
	if m.addAccountErr == "" {
		t.Fatal("expected addAccountErr to be set")
	}
	if len(m.accounts) != 0 {
		t.Fatalf("expected no account to be appended on error, got %+v", m.accounts)
	}
}

// TestAddAccountFormRequiresJID checks submitting with an empty JID field is
// rejected locally, without ever calling AccountAdder.
func TestAddAccountFormRequiresJID(t *testing.T) {
	adder := &fakeAccountAdder{}
	m := newTestModel(adder)
	m.selectedView = viewAccounts

	next, _ := m.Update(keyText("a"))
	m = next.(Model)

	next, cmd := m.Update(keyCode(tea.KeyEnter))
	m = next.(Model)
	if cmd != nil {
		t.Fatal("expected no cmd when JID is empty")
	}
	if adder.calls != 0 {
		t.Fatalf("expected no AddAccount calls, got %d", adder.calls)
	}
	if m.addAccountErr == "" {
		t.Fatal("expected a validation error to be set")
	}
}

// TestAddAccountFormRendersFullPlaceholders guards against a real bug found
// manually: textinput fields left at their zero Width truncate their
// placeholder to a single character (see textinput.Model.placeholderView's
// width-based truncation), so an unfocused field showed just "(" instead of
// its full placeholder text. newAddAccountForm must give every field an
// explicit width wide enough to show its whole placeholder.
func TestAddAccountFormRendersFullPlaceholders(t *testing.T) {
	adder := &fakeAccountAdder{}
	m := newTestModel(adder)
	m.selectedView = viewAccounts

	next, _ := m.Update(keyText("a"))
	m = next.(Model)

	for i, field := range m.addAccountInputs {
		got := ansi.Strip(field.View())
		want := field.Placeholder
		if want == "" {
			continue
		}
		if !strings.Contains(got, want) {
			t.Fatalf("field %d: rendered view %q (stripped: %q) doesn't contain full placeholder %q", i, field.View(), got, want)
		}
	}
}

// TestAddAccountFormPaste guards against a real bug found manually: bracketed
// paste arrives as tea.PasteMsg, not tea.KeyMsg, so it skipped the form's key
// interception entirely and fell through to the "route remaining events"
// switch, which only knew about m.selectedView (still viewAccounts while the
// form floats on top of it) and silently dropped it.
func TestAddAccountFormPaste(t *testing.T) {
	adder := &fakeAccountAdder{}
	m := newTestModel(adder)
	m.selectedView = viewAccounts

	next, _ := m.Update(keyText("a"))
	m = next.(Model)

	next, _ = m.Update(tea.PasteMsg{Content: "alice@example.com"})
	m = next.(Model)

	if got := m.addAccountInputs[0].Value(); got != "alice@example.com" {
		t.Fatalf("JID field after paste = %q, want %q", got, "alice@example.com")
	}

	next, _ = m.Update(keyCode(tea.KeyTab))
	m = next.(Model)
	next, _ = m.Update(tea.PasteMsg{Content: "hunter2"})
	m = next.(Model)
	if got := m.addAccountInputs[1].Value(); got != "hunter2" {
		t.Fatalf("password field after paste = %q, want %q", got, "hunter2")
	}
}

// TestChatComposePaste is a sanity check that the main chat compose input
// (m.input, used outside the add-account form) already receives paste
// correctly via the pre-existing "route remaining events" switch — added
// alongside the add-account paste fix above to document that this path was
// not affected by the same bug.
func TestChatComposePaste(t *testing.T) {
	m := newTestModel(&fakeAccountAdder{})
	m.selectedView = viewChat
	m.accounts = []Account{{Name: "me", Chats: nil, Messages: map[int][]Message{}}}

	next, _ := m.Update(tea.PasteMsg{Content: "pasted text"})
	m = next.(Model)

	if got := m.input.Value(); got != "pasted text" {
		t.Fatalf("chat input after paste = %q, want %q", got, "pasted text")
	}
}
