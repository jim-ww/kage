package datepicker

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/lrstanley/bubblezone/v2"
)

func date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
}

func press(m Model, code rune) (Model, tea.Cmd) {
	return m.Update(tea.KeyPressMsg{Code: code})
}

func picked(t *testing.T, cmd tea.Cmd) PickedMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("no pick")
	}
	msg, ok := cmd().(PickedMsg)
	if !ok {
		t.Fatalf("got %T, want PickedMsg", cmd())
	}
	return msg
}

func TestKeysMoveTheCursor(t *testing.T) {
	m := New(date(2026, time.January, 31))
	steps := []struct {
		code rune
		want time.Time
	}{
		{tea.KeyRight, date(2026, time.February, 1)},
		{tea.KeyLeft, date(2026, time.January, 31)},
		{tea.KeyPgDown, date(2026, time.February, 28)}, // clamped, not 3 Mar
		{tea.KeyUp, date(2026, time.February, 21)},
		{tea.KeyDown, date(2026, time.February, 28)},
		{tea.KeyPgUp, date(2026, time.January, 28)},
		{'{', date(2025, time.January, 28)},
		{'}', date(2026, time.January, 28)},
	}
	for _, s := range steps {
		m, _ = press(m, s.code)
		if !m.Cursor().Equal(s.want) {
			t.Fatalf("after %q: cursor %s, want %s", s.code, m.Cursor().Format(time.DateOnly), s.want.Format(time.DateOnly))
		}
	}
	m.Today = date(2026, time.June, 1)
	if m, _ = press(m, 't'); !m.Cursor().Equal(date(2026, time.June, 1)) {
		t.Fatalf("t: cursor %s, want today", m.Cursor().Format(time.DateOnly))
	}
}

func TestEnterPicksStartOfDay(t *testing.T) {
	m := New(time.Date(2026, time.June, 1, 15, 4, 5, 0, time.Local))
	_, cmd := press(m, tea.KeyEnter)
	if got := picked(t, cmd); !got.Date.Equal(date(2026, time.June, 1)) || got.ID != m.ID {
		t.Fatalf("picked %+v, want midnight 1 June", got)
	}
}

func TestMinMaxBoundTheCursorAndPicks(t *testing.T) {
	m := New(date(2026, time.June, 10))
	m.Min, m.Max = date(2026, time.June, 5), time.Date(2026, time.June, 12, 18, 0, 0, 0, time.Local)
	if m, _ = press(m, tea.KeyDown); !m.Cursor().Equal(date(2026, time.June, 12)) {
		t.Fatalf("cursor %s, want held at Max", m.Cursor().Format(time.DateOnly))
	}
	if m, _ = press(m, tea.KeyPgUp); !m.Cursor().Equal(date(2026, time.June, 5)) {
		t.Fatalf("cursor %s, want held at Min", m.Cursor().Format(time.DateOnly))
	}
	if cmd := m.pick(date(2026, time.June, 13)); cmd != nil {
		t.Fatal("a day after Max could be picked")
	}
}

func TestViewHasAFixedShape(t *testing.T) {
	for _, d := range []time.Time{date(2026, time.February, 1), date(2026, time.August, 1), date(2026, time.March, 1)} {
		m := New(d)
		lines := strings.Split(ansi.Strip(m.View()), "\n")
		if len(lines) != Height {
			t.Fatalf("%s: %d rows, want %d", d.Format("Jan"), len(lines), Height)
		}
		for _, l := range lines {
			if w := ansi.StringWidth(l); w != Width {
				t.Fatalf("%s: row %q is %d wide, want %d", d.Format("Jan"), l, w, Width)
			}
		}
	}
	// June 2026 starts on a Monday: the first week row starts with 1.
	lines := strings.Split(ansi.Strip(New(date(2026, time.June, 15)).View()), "\n")
	if lines[1] != "Mo Tu We Th Fr Sa Su" || !strings.HasPrefix(lines[2], " 1  2") {
		t.Fatalf("unexpected grid:\n%s", strings.Join(lines, "\n"))
	}
}

func TestMouseHoverAndClick(t *testing.T) {
	zm := zone.New()
	m := New(date(2026, time.June, 15))
	m.Zone = zm
	zm.Scan(m.View())

	z := awaitZone(t, zm, m.dayZoneID(date(2026, time.June, 3)))
	m, _ = m.Update(tea.MouseMotionMsg{X: z.StartX, Y: z.StartY})
	if !m.hover.Equal(date(2026, time.June, 3)) {
		t.Fatal("hovering 3 June didn't highlight it")
	}
	m, cmd := m.Update(tea.MouseClickMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseLeft})
	if got := picked(t, cmd); !got.Date.Equal(date(2026, time.June, 3)) {
		t.Fatalf("clicked 3 June, picked %s", got.Date.Format(time.DateOnly))
	}

	nz := awaitZone(t, zm, m.ID+"-next")
	m, _ = m.Update(tea.MouseClickMsg{X: nz.StartX, Y: nz.StartY, Button: tea.MouseLeft})
	if m.Cursor().Month() != time.July {
		t.Fatalf("› went to %s, want July", m.Cursor().Month())
	}
}

// awaitZone waits for bubblezone's asynchronous Scan to record id.
func awaitZone(t *testing.T, zm *zone.Manager, id string) *zone.ZoneInfo {
	t.Helper()
	for range 200 {
		if z := zm.Get(id); !z.IsZero() {
			return z
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("zone %q never recorded", id)
	return nil
}
