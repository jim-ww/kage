package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestFilePickerHoverBurstLandsOnLastRow fires several hover events back to
// back without running any of the commands they return, the way a fast
// pointer outruns the program's message queue: the cursor must end up on
// the row last hovered, not overshoot by deltas computed from a cursor
// position that queued moves hadn't caught up to yet.
func TestFilePickerHoverBurstLandsOnLastRow(t *testing.T) {
	m := newFilePickerBenchModel(t, 50)
	points := filePickerSweepPoints(t, &m)

	for _, row := range []int{8, 3, 9, 5} {
		next, _ := m.Update(tea.MouseMotionMsg(points[row]))
		m = next.(Model)
	}

	got := m.filePicker.HighlightedPath()
	m.filePicker.SelectRow(5)
	if want := m.filePicker.HighlightedPath(); got != want {
		t.Fatalf("after hover burst cursor is on %q, want row 5 (%q)", got, want)
	}
}
