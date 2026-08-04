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

// TestComposeUpDownCursorVsMessageNav checks that plain up/down move the
// compose textarea's cursor while it holds more than one line, but fall
// back to their usual job of moving the selected-message highlight once the
// input is back to a single line (or empty).
func TestComposeUpDownCursorVsMessageNav(t *testing.T) {
	chat := Chat{Address: "bob@localhost"}
	m := newTestModel(&fakeAccountAdder{})
	m.selectedView = viewChat
	m.accounts = []Account{{Name: "me", Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.chats.Select(0)
	m.setCurrentMessages([]Message{{Author: "bob", Content: "one"}, {Author: "bob", Content: "two"}})
	m.selectedMsg = 1

	// Single line: up/down navigate messages, untouched by the input.
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = next.(Model)
	if m.selectedMsg != 0 {
		t.Fatalf("selectedMsg after up (single-line input) = %d, want 0", m.selectedMsg)
	}
	if m.input.Value() != "" {
		t.Fatalf("input should be untouched by message-nav up, got %q", m.input.Value())
	}

	// Multiple lines: up/down move the textarea cursor, not the message
	// selection.
	next, _ = m.Update(keyText("a"))
	m = next.(Model)
	next, _ = m.Update(shiftEnterKey())
	m = next.(Model)
	next, _ = m.Update(keyText("b"))
	m = next.(Model)
	if !m.composeMultiline() {
		t.Fatal("expected composeMultiline() true after inserting a newline")
	}

	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = next.(Model)
	if m.selectedMsg != 0 {
		t.Fatalf("selectedMsg changed by up while composing multiline, got %d", m.selectedMsg)
	}
	if m.input.Line() != 0 {
		t.Fatalf("input cursor line after up = %d, want 0 (moved onto first line)", m.input.Line())
	}
}

// TestComposeUpDownSoftWrapCountsAsMultiline checks that a single long line
// with no explicit newline, once it's word-wrapped onto multiple visible
// rows, is treated the same as an explicit multi-line message for up/down
// purposes — it looks multiline on screen, so it should traverse like one.
func TestComposeUpDownSoftWrapCountsAsMultiline(t *testing.T) {
	chat := Chat{Address: "bob@localhost"}
	m := newTestModel(&fakeAccountAdder{})
	m.selectedView = viewChat
	m.accounts = []Account{{Name: "me", Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.chats.Select(0)
	m.setCurrentMessages([]Message{{Author: "bob", Content: "one"}, {Author: "bob", Content: "two"}})
	m.selectedMsg = 1

	// A single logical line, but long enough to wrap across several visual
	// rows at the input's (narrow, test-fixture) width.
	next, _ := m.Update(keyText(strings.Repeat("a", 300)))
	m = next.(Model)

	if lines := m.input.LineCount(); lines != 1 {
		t.Fatalf("input LineCount() = %d, want 1 (still one logical line)", lines)
	}
	if !m.composeMultiline() {
		t.Fatal("expected composeMultiline() true once the single line wraps onto multiple rows")
	}

	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = next.(Model)
	if m.selectedMsg != 1 {
		t.Fatalf("selectedMsg changed by up while the wrapped line is being edited, got %d", m.selectedMsg)
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
