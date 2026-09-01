package emojipicker

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
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

	// enter with existing (seeded, untouched) picks confirms the
	// highlighted cell, same as a fresh empty picker would - see
	// TestSeededPickedConfirmsCursorCellUntilTouched for why: reconfirming
	// the seeded set unchanged made a plain Enter (or click) on a *new*
	// emoji look like it did nothing at all whenever the message already
	// had a reaction.
	if got, ok := m.DidConfirm(tea.KeyPressMsg{Code: tea.KeyEnter}); !ok || len(got) != 1 || got[0] != m.visible[m.cursor].Emoji {
		t.Fatalf("expected untouched confirm to pick the highlighted cell, got %v ok=%v", got, ok)
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
	m.Resize(10, 2, 0)
	if m.Columns != 10 || m.VisibleRows != 2 {
		t.Fatalf("expected Columns=10 VisibleRows=2, got Columns=%d VisibleRows=%d", m.Columns, m.VisibleRows)
	}
	row := m.cursor / m.Columns
	if row < m.scrollRow || row >= m.scrollRow+m.VisibleRows {
		t.Fatalf("cursor row %d not within scrolled view [%d, %d)", row, m.scrollRow, m.scrollRow+m.VisibleRows)
	}
}

func TestViewLinesNeverExceedWidth(t *testing.T) {
	m := New(nil)
	m.Title = "react to \"a very long message preview that would otherwise overflow a narrow popup\""
	m.Resize(4, 3, 24)

	for i, line := range strings.Split(m.View(), "\n") {
		if w := ansi.StringWidth(line); w > m.Width {
			t.Fatalf("line %d (%q) is %d columns wide, wider than Width=%d", i, line, w, m.Width)
		}
	}
}

func TestBrokenTagSequenceFlagsExcluded(t *testing.T) {
	known := make(map[string]bool, len(shortcodes))
	for _, code := range shortcodes {
		known[code] = true
	}
	for _, code := range []string{":england:", ":scotland:", ":wales:", ":flag_for_england:", ":flag_for_scotland:", ":flag_for_wales:"} {
		if known[code] {
			t.Fatalf("%s should be excluded from shortcodes - it's a tag-sequence subdivision flag most terminal fonts can't render as one glyph, breaking grid alignment", code)
		}
	}
}

func TestClickConfirmMatchesEnterOnThatCell(t *testing.T) {
	m := New(nil)
	if len(m.visible) < 2 {
		t.Fatal("expected a default grid with at least 2 cells")
	}
	target := 1
	got := m.ClickConfirm(target)
	if len(got) != 1 || got[0] != m.visible[target].Emoji {
		t.Fatalf("expected clicking cell %d to confirm its emoji, got %v", target, got)
	}

	// With something already toggled via Tab, a click elsewhere confirms
	// the whole toggled set - same as enter would - not just the clicked cell.
	m.picked = []string{m.visible[0].Emoji}
	m.touched = true
	got = m.ClickConfirm(target)
	if len(got) != 1 || got[0] != m.visible[0].Emoji {
		t.Fatalf("expected click to confirm the toggled set, got %v", got)
	}
}

func TestVisibleCellsMatchesScrollWindow(t *testing.T) {
	m := New(nil)
	m.Columns = 4
	m.VisibleRows = 2
	m.visible = make([]entry, 20)
	for i := range m.visible {
		m.visible[i] = entry{Emoji: string(rune('a' + i))}
	}
	m.scrollRow = 0
	if got := m.VisibleCells(); len(got) != 8 || got[0] != 0 || got[7] != 7 {
		t.Fatalf("expected cells [0,8) at scrollRow 0, got %v", got)
	}
	m.scrollRow = 1
	if got := m.VisibleCells(); len(got) != 8 || got[0] != 4 || got[7] != 11 {
		t.Fatalf("expected cells [4,12) at scrollRow 1, got %v", got)
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

// TestSeededPickedConfirmsCursorCellUntilTouched guards against confirmFrom
// treating SetPicked's seeded set (a message's existing reactions, loaded
// before the user has touched anything) the same as an explicitly toggled
// one: reopening the picker on an already-reacted-to message left m.picked
// non-empty from the start, so a plain click or Enter on a *different*
// emoji silently re-returned the untouched seeded set instead of the
// clicked one - clicking or pressing Enter looked like it did nothing at
// all, while Tab (which always sets touched) worked fine.
func TestSeededPickedConfirmsCursorCellUntilTouched(t *testing.T) {
	m := New(nil)
	m.SetPicked([]string{"👍"})
	if len(m.visible) == 0 {
		t.Fatal("expected a default grid to be populated")
	}

	target := -1
	for i, e := range m.visible {
		if e.Emoji != "👍" {
			target = i
			break
		}
	}
	if target < 0 {
		t.Fatal("expected a visible cell other than the seeded emoji")
	}

	got := m.ClickConfirm(target)
	if len(got) != 1 || got[0] != m.visible[target].Emoji {
		t.Fatalf("expected an untouched click to confirm just the clicked cell, got %v", got)
	}

	// Once actually touched (Tab), the full toggled set confirms as usual.
	m.touched = true
	m.picked = []string{"👍", m.visible[target].Emoji}
	got = m.ClickConfirm(target)
	if len(got) != 2 {
		t.Fatalf("expected a touched click to confirm the whole toggled set, got %v", got)
	}
}
