package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// TestFilePickerRowsFitSmallTerminal guards against picker rows wider than
// the popup: popupDialog wraps them onto several lines, collapsing each
// row's hover/click zone to a sliver, so hovering a row no longer selects
// it.
func TestFilePickerRowsFitSmallTerminal(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 20}, {60, 16}, {48, 13}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			dir := t.TempDir()
			for i := range 30 {
				path := filepath.Join(dir, fmt.Sprintf("f%02d-a-reasonably-long-file-name.txt", i))
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			m := newMouseSweepBenchModel(30, 150)
			m.filePicker.CurrentDirectory = dir
			m.pickingFile = true
			next, _ := m.Update(m.filePicker.Init()())
			next, _ = next.(Model).Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m = next.(Model)
			if lines := strings.Count(m.View().Content, "\n") + 1; lines > size[1] {
				t.Fatalf("frame is %d lines on a %d-line terminal", lines, size[1])
			}

			for row := range m.filePicker.Height() {
				z := awaitZone(t, m.zone, zoneFilePickerRow(row))
				if z.StartY != z.EndY {
					t.Fatalf("row %d spans lines %d-%d, want one line", row, z.StartY, z.EndY)
				}
				next, _ := m.Update(tea.MouseMotionMsg{X: (z.StartX + z.EndX) / 2, Y: z.StartY})
				m = next.(Model)
				got := m.filePicker.HighlightedPath()
				m.filePicker.SelectRow(row)
				if want := m.filePicker.HighlightedPath(); got != want {
					t.Fatalf("hovering row %d highlighted %q, want %q", row, got, want)
				}
			}
		})
	}
}
