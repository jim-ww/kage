package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

func shiftEnterKey() tea.KeyMsg {
	return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}
}

func altEnterKey() tea.KeyMsg {
	return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}
}

// TestComposeShiftEnterInsertsNewline checks that shift+enter breaks the
// compose box to a new line instead of sending, while plain enter still
// sends — the two must not race for the same keypress (see
// defaultInputAreaKeys in keybinds.go). shift+enter only arrives as its own
// key on terminals with Kitty keyboard protocol support; alt+enter is kept
// as a fallback that works everywhere else.
func TestComposeShiftEnterInsertsNewline(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tea.KeyMsg
	}{
		{"shift+enter", shiftEnterKey()},
		{"alt+enter", altEnterKey()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(&fakeAccountAdder{})
			m.selectedView = viewChat
			m.accounts = []Account{{Name: "me", Chats: nil, Messages: map[int][]Message{}}}

			next, _ := m.Update(keyText("a"))
			m = next.(Model)
			next, _ = m.Update(tc.key)
			m = next.(Model)
			next, _ = m.Update(keyText("b"))
			m = next.(Model)

			if got, want := m.input.Value(), "a\nb"; got != want {
				t.Fatalf("input value after %s = %q, want %q", tc.name, got, want)
			}
			if lines := m.input.LineCount(); lines != 2 {
				t.Fatalf("input LineCount() = %d, want 2", lines)
			}
			if !strings.Contains(m.input.View(), "\n") {
				t.Fatalf("input View() should render on multiple lines, got %q", m.input.View())
			}
		})
	}
}

// TestComposeEnterSendsNotNewline checks a plain enter still submits the
// message (via SelectSend) rather than inserting a newline into the input.
func TestComposeEnterSendsNotNewline(t *testing.T) {
	chat := Chat{Address: "bob@localhost"}
	m := newTestModel(&fakeAccountAdder{})
	m.selectedView = viewChat
	m.accounts = []Account{{Name: "me", Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.chats.Select(0)

	next, _ := m.Update(keyText("hi"))
	m = next.(Model)
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)

	if got := m.input.Value(); got != "" {
		t.Fatalf("input should be cleared after send, got %q", got)
	}
}
