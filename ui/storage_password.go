package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/jim-ww/kage/ui/modal"
)

// storagePasswordModalID finds the open change-password form, to show how
// the change it started went.
const storagePasswordModalID = "storage-password"

// openChangePasswordPopup opens the "change local storage password" form —
// reached from the account menu (see accountRowContextMenuItems) or the
// account bar's key icon. Two masked fields, new password and confirm, under
// a loud warning: unlike an account password, this key isn't recoverable
// from anywhere else (not the server, not a peer) — losing it means losing
// every locally-stored message/draft it protects.
func (m *Model) openChangePasswordPopup() tea.Cmd {
	newPassword := m.newPasswordInput("New password:     ")
	confirm := m.newPasswordInput("Confirm password: ")
	form := m.newForm(m.styles.popupDanger.Render("Write this down. If you lose it, your local message history is unrecoverable.")+
		"\nLeave both fields blank to turn local storage encryption off.", "change", newPassword, confirm)
	return m.openModal(&modal.Modal[*Model]{
		ID:    storagePasswordModalID,
		Title: "Change storage password",
		Body:  form,
		OnAct: func(m *Model, v any) tea.Cmd {
			return m.submitChangePassword(form, v.([]string))
		},
	})
}

// submitChangePassword validates the two fields. A non-empty pair that
// matches kicks off the async re-encryption right away (see
// startStoragePasswordChange); an empty pair (both fields blank) is a
// request to turn local storage encryption *off* entirely, which is
// destructive enough (every message/draft gets rewritten to disk in plain
// text) to ask about first.
func (m *Model) submitChangePassword(form *modal.Form, values []string) tea.Cmd {
	newPassword, confirm := values[0], values[1]
	if newPassword != confirm {
		form.SetError("passwords don't match")
		return nil
	}
	form.SetError("")
	if newPassword == "" {
		return m.confirm("Disable local storage encryption?",
			"Every stored message and draft will be rewritten to disk in plain text.",
			func(m *Model) tea.Cmd { return m.startStoragePasswordChange("") })
	}
	return m.startStoragePasswordChange(newPassword)
}

// startStoragePasswordChange kicks off the async re-encryption via
// StoragePasswordChanger — the actual transactional rotation (and the
// daemon-restart-required aftermath) lives entirely on the other side of
// that interface; see main.go's adapter.ChangeStoragePassword. newPassword
// may be "" to turn local storage encryption off (see submitChangePassword).
func (m *Model) startStoragePasswordChange(newPassword string) tea.Cmd {
	md := m.modals.Find(storagePasswordModalID)
	if md == nil {
		return nil
	}
	form := md.Body.(*modal.Form)
	if m.storagePasswordChanger == nil {
		form.SetError("storage password changing isn't available")
		return nil
	}
	if newPassword == "" {
		form.SetBusy("disabling local storage encryption…")
	} else {
		form.SetBusy("re-encrypting local storage…")
	}
	changer := m.storagePasswordChanger
	return func() tea.Msg {
		return StoragePasswordChangedMsg{Err: changer.ChangeStoragePassword(newPassword)}
	}
}
