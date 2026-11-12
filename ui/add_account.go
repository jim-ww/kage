package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jim-ww/kage/ui/modal"
)

// addAccountModalID finds the open add-account form, to show how the
// account it started adding is doing.
const addAccountModalID = "add-account"

// Notes heading the add-account form in each of its modes.
const (
	addAccountLoginNote    = "Log in to an existing account."
	addAccountRegisterNote = "Register a new account on the server."
)

// addAccountBody is the add-account form: JID, password, confirm password
// (shown only when registering) and own GPG key. ctrl+r switches between
// logging in and registering.
type addAccountBody struct {
	*modal.Form
	register bool
}

var ctrlR = tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}

func (b *addAccountBody) Update(msg tea.Msg) (tea.Cmd, modal.Outcome) {
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == ctrlR.String() && !b.Busy() {
		b.register = !b.register
		b.SetError("")
		b.Note = addAccountLoginNote
		if b.register {
			b.Note = addAccountRegisterNote
		}
		return b.SetHidden(2, !b.register), modal.Continue
	}
	return b.Form.Update(msg)
}

func (b *addAccountBody) Hints() []modal.Hint {
	label := "register instead"
	if b.register {
		label = "log in instead"
	}
	hints := b.Form.Hints()
	if b.Busy() {
		return hints
	}
	return append(hints, modal.KeyHint(ctrlR, label))
}

// openAddAccount opens the add-account form, logging in by default.
func (m *Model) openAddAccount() tea.Cmd {
	jid := m.newInput("", "user@server", "JID:      ", "")
	password := m.newPasswordInput("Password: ")
	password.Model.Placeholder = "(leave blank to use password_cmd/keyring)"
	confirm := m.newPasswordInput("Confirm:  ")
	confirm.Model.Placeholder = "confirm password"
	gpg := m.newInput("", "(optional) own gpg key fingerprint", "GPG key:  ", "")
	body := &addAccountBody{Form: m.newForm(addAccountLoginNote, "add", jid, password, confirm, gpg)}
	body.SetHidden(2, true)
	return m.openModal(&modal.Modal[*Model]{
		ID:    addAccountModalID,
		Title: "Add account",
		Body:  body,
		OnAct: func(m *Model, v any) tea.Cmd { return m.submitAddAccount(body, v.([]string)) },
	})
}

// submitAddAccount checks the form's values and starts adding the account;
// AccountAddedMsg or AccountAddErrorMsg says how it went.
func (m *Model) submitAddAccount(body *addAccountBody, values []string) tea.Cmd {
	jid := strings.TrimSpace(values[0])
	password, confirm, gpgKeyID := values[1], values[2], strings.TrimSpace(values[3])
	switch {
	case jid == "":
		body.SetError("JID is required")
		return nil
	case m.accountAdder == nil:
		body.SetError("no account adder configured")
		return nil
	case body.register && password == "":
		body.SetError("password is required to register")
		return nil
	case body.register && password != confirm:
		body.SetError("passwords don't match")
		return nil
	}
	if body.register {
		body.SetBusy("registering…")
	} else {
		body.SetBusy("logging in…")
	}
	adder, register := m.accountAdder, body.register
	return func() tea.Msg { return adder.AddAccount(jid, password, gpgKeyID, register) }
}

// OpenAddAccountForm switches to the accounts panel and opens the
// add-account form, same as pressing "a" there — used by main.go to land
// directly in it on a fresh install with zero accounts configured, since an
// empty sidebar with no visible way to add one would otherwise be a dead end.
func (m Model) OpenAddAccountForm() Model {
	m.setSelectedView(viewAccounts)
	m.openAddAccount()
	return m
}
