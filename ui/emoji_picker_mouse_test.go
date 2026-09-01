package ui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/jim-ww/kage/ui/emojipicker"
	zone "github.com/lrstanley/bubblezone/v2"
)

// awaitZone polls until id's zone is recorded - Scan buffers zone updates
// through an internal channel/goroutine (see the Manager.Scan doc), so an
// immediate Get right after Scan can race it.
func awaitZone(t *testing.T, zm *zone.Manager, id string) *zone.ZoneInfo {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if z := zm.Get(id); !z.IsZero() {
			return z
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("zone %q never recorded", id)
	return nil
}

// TestEmojiPickerCellUnderMouseSnapsToNearestInRow guards against a click
// aimed at a visible emoji glyph missing its cell entirely because the
// glyph's actual terminal display width disagreed with what bubblezone
// measured when recording the cell's hitbox (a real-world mismatch for
// flags, ZWJ family/skin-tone sequences, and codepoints missing a U+FE0F
// selector - different terminals' wcwidth tables disagree with Go's, so a
// column of drift like this can't be reproduced by re-rendering through the
// same width library that recorded it). Whatever the cause, a coordinate
// gap between two adjacent cells' recorded boxes must still resolve to
// whichever cell the click is closer to, in the same row - not silently
// miss and do nothing.
func TestEmojiPickerCellUnderMouseSnapsToNearestInRow(t *testing.T) {
	zm := zone.New()
	t.Cleanup(zm.Close)

	picker := emojipicker.New(nil)
	picker.Zone = zm
	picker.ID = "emoji-picker"
	picker.Columns = 4
	picker.VisibleRows = 1

	m := Model{zone: zm}
	m.emojiPicker = &picker

	cells := picker.VisibleCells()
	if len(cells) < 2 {
		t.Fatal("expected at least 2 visible cells")
	}

	// Hand-craft marked content for cells 0 and 1 with a deliberate 3-column
	// gap between their boxes, standing in for a real terminal's width
	// disagreement - VisibleCells()/CellZoneID() only depend on Columns/ID,
	// not on what View() actually rendered, so this is a faithful stand-in.
	content := zm.Mark(picker.CellZoneID(cells[0]), "A") + "   " + zm.Mark(picker.CellZoneID(cells[1]), "B")
	zm.Scan(content)

	z0 := awaitZone(t, zm, picker.CellZoneID(cells[0]))
	z1 := awaitZone(t, zm, picker.CellZoneID(cells[1]))
	if z1.StartX-z0.EndX < 2 {
		t.Fatalf("expected a real gap between the two cells, got z0=%+v z1=%+v", z0, z1)
	}
	gapX := z0.EndX + 1 // strictly inside the gap, in neither box

	click := tea.MouseClickMsg{X: gapX, Y: z0.StartY, Button: tea.MouseLeft}
	i, ok := m.emojiPickerCellUnderMouse(click)
	if !ok {
		t.Fatal("expected a hit via nearest-in-row fallback for a click in the gap")
	}
	if i != cells[0] {
		t.Fatalf("expected the gap click (closer to cell 0) to snap to cell 0, got index %d", i)
	}
}
