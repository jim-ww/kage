package ui

import (
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

func ctrlZKey() tea.KeyMsg { return tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl} }
func ctrlShiftZKey() tea.KeyMsg {
	return tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl | tea.ModShift}
}

// TestComposeUndoRedo checks ctrl+z/ctrl+shift+z step the compose box back
// and forth through the draft's edit history for the current session.
func TestComposeUndoRedo(t *testing.T) {
	m := newTestModel(&fakeAccountAdder{})
	m.selectedView = viewChat
	m.accounts = []Account{{Name: "me", Chats: nil, Messages: map[int][]Message{}}}

	for _, s := range []string{"a", "b", "c"} {
		next, _ := m.Update(keyText(s))
		m = next.(Model)
	}
	if got, want := m.input.Value(), "abc"; got != want {
		t.Fatalf("input value after typing = %q, want %q", got, want)
	}

	next, _ := m.Update(ctrlZKey())
	m = next.(Model)
	if got, want := m.input.Value(), "ab"; got != want {
		t.Fatalf("input value after undo = %q, want %q", got, want)
	}

	next, _ = m.Update(ctrlZKey())
	m = next.(Model)
	if got, want := m.input.Value(), "a"; got != want {
		t.Fatalf("input value after second undo = %q, want %q", got, want)
	}

	next, _ = m.Update(ctrlShiftZKey())
	m = next.(Model)
	if got, want := m.input.Value(), "ab"; got != want {
		t.Fatalf("input value after redo = %q, want %q", got, want)
	}

	// Undoing once more, then typing something new, should discard the
	// forward ("abc") history rather than leaving it reachable by redo.
	next, _ = m.Update(ctrlZKey())
	m = next.(Model)
	next, _ = m.Update(keyText("x"))
	m = next.(Model)
	if got, want := m.input.Value(), "ax"; got != want {
		t.Fatalf("input value after undo+type = %q, want %q", got, want)
	}
	next, _ = m.Update(ctrlShiftZKey())
	m = next.(Model)
	if got, want := m.input.Value(), "ax"; got != want {
		t.Fatalf("redo after new edit should be a no-op, got %q, want %q", got, want)
	}
}

// TestComposeUndoHistoryClearedOnSend checks that sending a message resets
// undo history, so ctrl+z after a send can't reach back into the message
// that was just sent.
func TestComposeUndoHistoryClearedOnSend(t *testing.T) {
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

	next, _ = m.Update(ctrlZKey())
	m = next.(Model)
	if got := m.input.Value(); got != "" {
		t.Fatalf("undo after send should stay empty, got %q", got)
	}
}
