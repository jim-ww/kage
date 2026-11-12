package ui

import (
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/jim-ww/kage/ui/modal"
)

// newModalStack is the Model's stack of open modals (see ui/modal), drawn
// with the theme's colors.
func newModalStack(zm *zone.Manager, colors uiColors) modal.Stack[*Model] {
	st := modal.Styles{
		Border:    lipgloss.NewStyle().Foreground(colors.borderA),
		Title:     lipgloss.NewStyle().Foreground(colors.borderA).Bold(true),
		HintKey:   lipgloss.NewStyle().Foreground(colors.accentCyan).Bold(true),
		HintLabel: lipgloss.NewStyle().Foreground(colors.time),
		HintHover: onAccent(colors.accentCyan),
	}
	return modal.Stack[*Model]{Zone: zm, ID: "modal", Styles: &st}
}

// openModal shows md over the chat area, on top of any modal already open.
func (m *Model) openModal(md *modal.Modal[*Model]) tea.Cmd {
	m.modals.SetSize(m.chatAreaWidth(), m.popupAreaHeight())
	return m.modals.Push(md)
}

// confirm asks a yes/no question, title with detail under it; yes runs
// onYes.
func (m *Model) confirm(title, detail string, onYes func(*Model) tea.Cmd) tea.Cmd {
	c := modal.NewConfirm(detail)
	c.Yes, c.No, c.Match = m.keys.ConfirmYes, m.keys.ConfirmNo, matchesKey
	return m.openModal(&modal.Modal[*Model]{
		Title:    title,
		Body:     c,
		OnSubmit: func(m *Model, _ any) tea.Cmd { return onYes(m) },
	})
}

// newPasswordInput is a masked field, styled and bound like newInput's.
func (m *Model) newPasswordInput(prompt string) *modal.Input {
	in := m.newInput("", "", prompt, "")
	in.Model.EchoMode = textinput.EchoPassword
	return in
}

// newForm is a modal.Form of fields under note, enter labeled submitLabel,
// in the theme's colors.
func (m *Model) newForm(note, submitLabel string, fields ...*modal.Input) *modal.Form {
	f := modal.NewForm(note, fields...)
	f.SubmitLabel = submitLabel
	f.Styles.Note = lipgloss.NewStyle().Foreground(m.styles.colors.textMuted)
	f.Styles.Error = m.styles.popupDanger
	f.Styles.Status = lipgloss.NewStyle().Foreground(m.styles.colors.accentCyan).Italic(true)
	return f
}

// newInput is a one-line text field styled and bound like kage's others.
func (m *Model) newInput(value, placeholder, prompt, submitLabel string) *modal.Input {
	in := modal.NewInput(value, placeholder)
	in.Model.Prompt = prompt
	in.Model.KeyMap = m.keys.TextInputKeys
	in.SubmitLabel = submitLabel
	applyTextInputStyles(&in.Model, m.styles.colors)
	return in
}
