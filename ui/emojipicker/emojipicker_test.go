package emojipicker

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSearchFlagReturnsManyResultsAndDedupes(t *testing.T) {
	results := search("flag")
	if len(results) < 10 {
		t.Fatalf("expected many flag matches, got %d", len(results))
	}
	seen := make(map[string]bool, len(results))
	for _, e := range results {
		if seen[e.Emoji] {
			t.Fatalf("duplicate emoji %q in search results", e.Emoji)
		}
		seen[e.Emoji] = true
	}
}

func TestMoveCursorClampsAndScrolls(t *testing.T) {
	m := New(nil)
	m.Columns = 4
	m.VisibleRows = 2
	m.visible = make([]entry, 20) // 5 rows
	for i := range m.visible {
		m.visible[i] = entry{Emoji: string(rune('a' + i))}
	}

	m.moveCursor(-100)
	if m.cursor != 0 {
		t.Fatalf("expected cursor clamped to 0, got %d", m.cursor)
	}

	m.moveCursor(100)
	if m.cursor != len(m.visible)-1 {
		t.Fatalf("expected cursor clamped to last index, got %d", m.cursor)
	}
	if m.scrollRow != 3 { // last row (index 4) must be within [scrollRow, scrollRow+2)
		t.Fatalf("expected scrollRow 3 to follow cursor into view, got %d", m.scrollRow)
	}

	m.moveCursor(-100)
	if m.scrollRow != 0 {
		t.Fatalf("expected scrollRow back to 0 after returning to first row, got %d", m.scrollRow)
	}
}

func TestClearPickedThenConfirmSendsEmptySet(t *testing.T) {
	m := New(nil)
	m.SetPicked([]string{"👍", "❤️"})

	// enter with existing picks re-sends them unchanged
	if got, ok := m.DidConfirm(tea.KeyPressMsg{Code: tea.KeyEnter}); !ok || len(got) != 2 {
		t.Fatalf("expected existing picks to be confirmed unchanged, got %v ok=%v", got, ok)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	if len(m.picked) != 0 {
		t.Fatalf("expected ClearPicked to empty the set, got %v", m.picked)
	}

	got, ok := m.DidConfirm(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !ok {
		t.Fatal("expected confirm to match enter")
	}
	if len(got) != 0 {
		t.Fatalf("expected an explicit clear to confirm as empty, not fall back to the cursor cell, got %v", got)
	}
}

func TestResizeReflowsAndClampsScroll(t *testing.T) {
	m := New(nil)
	m.Columns = 4
	m.VisibleRows = 2
	m.visible = make([]entry, 20) // 5 rows of 4
	for i := range m.visible {
		m.visible[i] = entry{Emoji: string(rune('a' + i))}
	}
	m.cursor = 19 // last cell, row 4
	m.ensureCursorVisible()
	if m.scrollRow != 3 {
		t.Fatalf("setup: expected scrollRow 3, got %d", m.scrollRow)
	}

	// Widening to 10 columns collapses 20 cells into 2 rows - the cursor's
	// row under the new shape (19/10 = row 1) must still end up in view,
	// not stuck at a scrollRow computed for the old, narrower grid.
	m.Resize(10, 2)
	if m.Columns != 10 || m.VisibleRows != 2 {
		t.Fatalf("expected Columns=10 VisibleRows=2, got Columns=%d VisibleRows=%d", m.Columns, m.VisibleRows)
	}
	row := m.cursor / m.Columns
	if row < m.scrollRow || row >= m.scrollRow+m.VisibleRows {
		t.Fatalf("cursor row %d not within scrolled view [%d, %d)", row, m.scrollRow, m.scrollRow+m.VisibleRows)
	}
}

func TestUntouchedConfirmFallsBackToCursorCell(t *testing.T) {
	m := New(nil)
	if len(m.visible) == 0 {
		t.Fatal("expected a default grid to be populated")
	}
	got, ok := m.DidConfirm(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !ok || len(got) != 1 || got[0] != m.visible[m.cursor].Emoji {
		t.Fatalf("expected untouched confirm to pick the highlighted cell, got %v ok=%v", got, ok)
	}
}
