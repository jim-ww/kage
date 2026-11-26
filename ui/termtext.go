package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Halfwidth katakana voiced/semi-voiced sound marks (ﾞ U+FF9E, ﾟ U+FF9F,
// as in ｶﾞ or the kaomoji (ﾟДﾟ)) are grapheme extenders: lipgloss and the
// textarea measure "(ﾟ" as one cell, while a terminal draws them as two.
// Every row holding one comes out a cell wider than laid out, pushing the
// layout and the cursor off. Shown as look-alikes that every width table
// agrees are one cell wide.
var (
	termTextIn  = strings.NewReplacer("ﾞ", "ʺ", "ﾟ", "˚")
	termTextOut = strings.NewReplacer("ʺ", "ﾞ", "˚", "ﾟ")
)

// termText is s safe to lay out and draw: see termTextIn.
func termText(s string) string {
	if !strings.ContainsAny(s, "ﾞﾟ") {
		return s
	}
	return termTextIn.Replace(s)
}

// composeValue is the compose box's text as the user wrote it, undoing the
// termText substitution its contents are kept in.
func (m Model) composeValue() string {
	v := m.input.Value()
	if !strings.ContainsAny(v, "ʺ˚") {
		return v
	}
	return termTextOut.Replace(v)
}

// setComposeValue replaces the compose box's text with s.
func (m *Model) setComposeValue(s string) {
	m.input.SetValue(termText(s))
}

// termTextInput is a key press or paste headed for the compose box, its text
// substituted as termText.
func termTextInput(msg tea.Msg) tea.Msg {
	switch k := msg.(type) {
	case tea.KeyPressMsg:
		k.Text = termText(k.Text)
		return k
	case tea.PasteMsg:
		k.Content = termText(k.Content)
		return k
	}
	return msg
}
