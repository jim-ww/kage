package ui

import (
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// changePasswordState holds the "change local storage password" popup's
// state: two masked fields (new password, confirm), which of them is
// focused, whether the async re-encryption (StoragePasswordChanger) is in
// flight, and any validation/failure message to show.
type changePasswordState struct {
	inputs [2]textinput.Model // 0: new password, 1: confirm
	focus  int
	busy   bool
	err    string
}

// newChangePasswordForm builds a fresh, empty two-field password form,
// styled/keymapped the same as the add-account form's own masked password
// field (see newAddAccountForm).
func (m Model) newChangePasswordForm() *changePasswordState {
	newInput := textinput.New()
	newInput.Prompt = "New password:     "
	newInput.EchoMode = textinput.EchoPassword
	newInput.KeyMap = m.keys.TextInputKeys
	newInput.SetWidth(addAccountFieldWidth)
	applyTextInputStyles(&newInput, m.styles.colors)
	newInput.Focus()

	confirmInput := textinput.New()
	confirmInput.Prompt = "Confirm password: "
	confirmInput.EchoMode = textinput.EchoPassword
	confirmInput.KeyMap = m.keys.TextInputKeys
	confirmInput.SetWidth(addAccountFieldWidth)
	applyTextInputStyles(&confirmInput, m.styles.colors)

	return &changePasswordState{inputs: [2]textinput.Model{newInput, confirmInput}}
}

// openChangePasswordPopup opens the "change local storage password" popup —
// triggered by the account bar's key icon (mouse) or the ChangeStoragePassword
// keybind, both gated to viewAccounts.
func (m *Model) openChangePasswordPopup() tea.Cmd {
	m.changePasswordState = m.newChangePasswordForm()
	return textinput.Blink
}

// updateChangePasswordForm handles all key input while the change-password
// popup is open: tab/shift+tab cycles the two fields, enter submits (see
// submitChangePassword), esc cancels — except while busy, since the
// re-encryption transaction already committed can't be un-committed by
// backing out of the popup.
func (m Model) updateChangePasswordForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.changePasswordState
	switch {
	case msg.String() == "esc":
		if !s.busy {
			m.changePasswordState = nil
		}
		return m, nil

	case msg.String() == "tab", msg.String() == "down":
		if s.busy {
			return m, nil
		}
		s.inputs[s.focus].Blur()
		s.focus = (s.focus + 1) % len(s.inputs)
		s.inputs[s.focus].Focus()
		return m, textinput.Blink

	case msg.String() == "shift+tab", msg.String() == "up":
		if s.busy {
			return m, nil
		}
		s.inputs[s.focus].Blur()
		s.focus = (s.focus - 1 + len(s.inputs)) % len(s.inputs)
		s.inputs[s.focus].Focus()
		return m, textinput.Blink

	case matchesKey(msg, m.keys.SelectSend):
		return m, m.submitChangePassword()

	default:
		var cmd tea.Cmd
		s.inputs[s.focus], cmd = s.inputs[s.focus].Update(msg)
		return m, cmd
	}
}

// submitChangePassword validates the two fields. A non-empty pair that
// matches kicks off the async re-encryption right away (see
// startStoragePasswordChange); an empty pair (both fields blank) is a
// request to turn local storage encryption *off* entirely, which is
// destructive enough (every message/draft gets rewritten to disk in plain
// text) to gate behind an explicit confirmation popup instead of submitting
// immediately - see confirmDisableStorageEncryption in update_keys.go.
func (m *Model) submitChangePassword() tea.Cmd {
	s := m.changePasswordState
	if s.busy {
		return nil
	}
	newPassword := s.inputs[0].Value()
	confirm := s.inputs[1].Value()
	if newPassword != confirm {
		s.err = "passwords don't match"
		return nil
	}
	if newPassword == "" {
		s.err = ""
		m.confirmTarget = confirmDisableStorageEncryption
		return nil
	}
	return m.startStoragePasswordChange(newPassword)
}

// startStoragePasswordChange kicks off the async re-encryption via
// StoragePasswordChanger — the actual transactional rotation (and the
// daemon-restart-required aftermath) lives entirely on the other side of
// that interface; see main.go's adapter.ChangeStoragePassword. newPassword
// may be "" to turn local storage encryption off (see submitChangePassword).
func (m *Model) startStoragePasswordChange(newPassword string) tea.Cmd {
	s := m.changePasswordState
	if m.storagePasswordChanger == nil {
		s.err = "storage password changing isn't available"
		return nil
	}
	s.busy = true
	s.err = ""
	changer := m.storagePasswordChanger
	return func() tea.Msg {
		return StoragePasswordChangedMsg{Err: changer.ChangeStoragePassword(newPassword)}
	}
}
