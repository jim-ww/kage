package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestHelpPopupOpensAndCloses guards the ctrl+? full-keybindings modal. The
// binding covers several real key encodings (see KeyMap.Help's comment) since
// no terminal actually reports the literal string "ctrl+?" — this exercises
// the ones an actual ctrl+/ (with or without shift) keypress produces.
func TestHelpPopupOpensAndCloses(t *testing.T) {
	variants := []tea.KeyMsg{
		tea.KeyPressMsg{Code: '/', Mod: tea.ModCtrl},
		tea.KeyPressMsg{Code: '/', Mod: tea.ModCtrl | tea.ModShift},
		tea.KeyPressMsg{Code: '_', Mod: tea.ModCtrl},
	}
	for _, open := range variants {
		m := newTestModel(nil)
		m.selectedView = viewChat

		next, cmd := m.Update(open)
		nm := next.(Model)
		if !nm.showHelp {
			t.Fatalf("%q: showHelp = false after pressing it, want true", open.String())
		}
		if msg := nonIdleCmd(cmd); msg != nil {
			t.Fatalf("%q: expected no cmd while opening help, got %T", open.String(), msg)
		}
		if !nm.popupActive() {
			t.Fatalf("%q: popupActive() = false while help is open, want true", open.String())
		}

		next, _ = nm.Update(keyText("esc"))
		nm = next.(Model)
		if nm.showHelp {
			t.Fatalf("%q: showHelp still true after esc, want false", open.String())
		}
	}
}

// TestHelpPopupSectionsAreLabeledByTab guards against the help popup dumping
// every binding into one undifferentiated list — each section must be
// titled with the tab it applies to, so it's clear e.g. "c" only means
// "manage contacts" while the accounts tab is focused.
func TestHelpPopupSectionsAreLabeledByTab(t *testing.T) {
	sections := DefaultKeyMap.helpSections()
	wantTitles := []string{"Accounts tab", "Chats tab", "Chat tab", "Global"}
	if len(sections) != len(wantTitles) {
		t.Fatalf("helpSections() returned %d sections, want %d", len(sections), len(wantTitles))
	}
	for i, want := range wantTitles {
		if sections[i].Title != want {
			t.Fatalf("section %d title = %q, want %q", i, sections[i].Title, want)
		}
		if len(sections[i].Entries) == 0 {
			t.Fatalf("section %q has no entries", want)
		}
	}
}

// TestFooterHintIsAlwaysOneLine guards the always-one-line footer hint: even
// viewChat's dozen-odd bindings must never wrap to a second row — anything
// that doesn't fit is truncated with an ellipsis instead (see ctrl+? for the
// full list).
func TestFooterHintIsAlwaysOneLine(t *testing.T) {
	for _, view := range []selectedView{viewAccounts, viewChats, viewChat} {
		hint := DefaultKeyMap.helpHint(view, true)
		for _, width := range []int{20, 40, 80, 120} {
			lines := footerWrapLines(hint, width, footerMaxLines)
			if len(lines) != 1 {
				t.Fatalf("view %v width %d: footerWrapLines returned %d lines, want 1: %q", view, width, len(lines), lines)
			}
		}
	}
	if !strings.Contains(DefaultKeyMap.helpHint(viewChat, false), "help") {
		t.Fatal(`viewChat footer hint should mention "help" (ctrl+?)`)
	}
}
