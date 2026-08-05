package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestQuitAsksForConfirmation guards against 'q' exiting the app outright:
// it must open the confirm-quit popup instead, only actually quitting once
// 'y' is pressed, and 'n'/esc must dismiss the popup and leave the app
// running.
func TestQuitAsksForConfirmation(t *testing.T) {
	m := newTestModel(nil)
	m.selectedView = viewChats

	next, cmd := m.Update(keyText("q"))
	nm := next.(Model)
	if nm.confirmTarget != confirmQuit {
		t.Fatalf("confirmTarget = %v, want confirmQuit", nm.confirmTarget)
	}
	if cmd != nil {
		t.Fatalf("expected no cmd while opening the quit popup, got one")
	}

	// 'n' dismisses without quitting.
	next, cmd = nm.Update(keyText("n"))
	nm = next.(Model)
	if nm.confirmTarget != confirmNone {
		t.Fatalf("confirmTarget = %v after 'n', want confirmNone", nm.confirmTarget)
	}
	if cmd != nil {
		t.Fatalf("expected no cmd after cancelling quit, got one")
	}

	// Re-open and confirm with 'y': must yield tea.Quit.
	next, _ = nm.Update(keyText("q"))
	nm = next.(Model)
	_, cmd = nm.Update(keyText("y"))
	if cmd == nil {
		t.Fatal("expected a tea.Quit cmd after confirming quit, got nil")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected cmd to produce tea.QuitMsg, got %T", cmd())
	}
}

// TestCtrlCQuitsWithoutConfirmation guards the hard-exit escape hatch:
// ctrl+c must quit immediately, bypassing the confirm-quit popup entirely.
func TestCtrlCQuitsWithoutConfirmation(t *testing.T) {
	m := newTestModel(nil)
	m.selectedView = viewChats

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("expected a tea.Quit cmd from ctrl+c, got nil")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected cmd to produce tea.QuitMsg, got %T", cmd())
	}
}
