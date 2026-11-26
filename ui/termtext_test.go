package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"
)

// A terminal draws each halfwidth sound mark in a cell of its own; the
// layout must count the same cells.
func TestTermTextWidthMatchesTerminal(t *testing.T) {
	for _, s := range []string{"(ﾟДﾟ)ｳ", "ｶﾞｷﾞ", "ﾟ"} {
		got := termText(s)
		if lw, tw := lipgloss.Width(got), wcwidth(s); lw != tw {
			t.Errorf("termText(%q) = %q: laid out in %d cells, terminal draws %d", s, got, lw, tw)
		}
	}
}

func TestComposeKeepsHalfwidthSoundMarks(t *testing.T) {
	m := newDraftSyncModel(t, "")
	const kaomoji = "(ﾟДﾟ)ｳ ｶﾞ"
	for _, r := range kaomoji {
		next, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = next.(Model)
	}
	if got := m.composeValue(); got != kaomoji {
		t.Fatalf("composeValue() = %q, want %q", got, kaomoji)
	}
	m.setComposeValue("ﾟ")
	if got := m.composeValue(); got != "ﾟ" {
		t.Fatalf("after setComposeValue: %q", got)
	}
}

// wcwidth is s's width the way a terminal without grapheme clustering
// (mode 2027) draws it: rune by rune.
func wcwidth(s string) int {
	n := 0
	for _, r := range s {
		n += runewidth.RuneWidth(r)
	}
	return n
}
