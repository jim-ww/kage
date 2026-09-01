package emojipicker

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestFlagSearchGridRowsAlignToUniformWidth guards against the grid columns
// drifting out of alignment for flag emoji (2-codepoint regional-indicator
// pairs, or ZWJ/skin-tone sequences) whose measured width varies from cell
// to cell - without a fixed per-cell width, strings.Join-ing variable-width
// cells produced rows of different total widths, so the popup's border
// wrapped unevenly from row to row.
func TestFlagSearchGridRowsAlignToUniformWidth(t *testing.T) {
	m := New(nil)
	m.Columns = 7
	m.VisibleRows = 8
	m.query.SetValue("flag")
	m.refresh()
	if len(m.visible) < m.Columns*2 {
		t.Fatalf("expected at least 2 full rows of flag matches, got %d", len(m.visible))
	}

	view := m.View()
	var rowWidths []int
	for _, line := range strings.Split(view, "\n") {
		// Grid rows are the only lines containing multiple emoji cells;
		// identify them by width being a multiple of CellWidth and > 0.
		w := ansi.StringWidth(line)
		if w > 0 && w%CellWidth == 0 && w/CellWidth == m.Columns {
			rowWidths = append(rowWidths, w)
		}
	}
	if len(rowWidths) < 2 {
		t.Fatalf("expected multiple full grid rows to inspect, got widths %v in view:\n%s", rowWidths, view)
	}
	for _, w := range rowWidths[1:] {
		if w != rowWidths[0] {
			t.Fatalf("grid rows have inconsistent widths %v - flag glyphs threw off column alignment", rowWidths)
		}
	}
}
