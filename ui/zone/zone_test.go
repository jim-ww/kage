package zone

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestScanStripsMarkersAndRecordsBounds(t *testing.T) {
	m := New()
	red := "\x1b[31m"
	frame := "日本 " + m.Mark("a", red+"ab\x1b[m") + "\n" +
		m.Mark("box", "xy\n"+m.Mark("in", "z")+"w") + "\n" +
		"plain"
	got := m.Scan(frame)
	if want := "日本 " + red + "ab\x1b[m\nxy\nzw\nplain"; got != want {
		t.Fatalf("Scan = %q, want %q", got, want)
	}
	for id, want := range map[string]ZoneInfo{
		"a":   {StartX: 5, StartY: 0, EndX: 6, EndY: 0},
		"box": {StartX: 0, StartY: 1, EndX: 1, EndY: 2},
		"in":  {StartX: 0, StartY: 2, EndX: 0, EndY: 2},
	} {
		if z := m.Get(id); z == nil || *z != want {
			t.Errorf("Get(%q) = %+v, want %+v", id, z, want)
		}
	}
}

func TestScanReplacesThePreviousFramesZones(t *testing.T) {
	m := New()
	m.Scan(m.Mark("old", "x"))
	m.Scan(m.Mark("new", "x"))
	if m.Get("old") != nil {
		t.Fatal("a zone missing from the latest frame is still reported")
	}
	if m.Get("new").IsZero() {
		t.Fatal("the latest frame's zone isn't reported")
	}
}

func TestDisabledMarksNothing(t *testing.T) {
	m := New()
	m.SetEnabled(false)
	if got := m.Mark("a", "x"); got != "x" {
		t.Fatalf("disabled Mark = %q, want the input as is", got)
	}
}

func TestInBoundsAndPos(t *testing.T) {
	z := &ZoneInfo{StartX: 2, StartY: 1, EndX: 4, EndY: 1}
	in := tea.MouseClickMsg{X: 3, Y: 1}
	if !z.InBounds(in) || z.InBounds(tea.MouseClickMsg{X: 5, Y: 1}) {
		t.Fatal("InBounds is wrong at the zone's edge")
	}
	if x, y := z.Pos(in); x != 1 || y != 0 {
		t.Fatalf("Pos = %d,%d, want 1,0", x, y)
	}
	var none *ZoneInfo
	if none.InBounds(in) {
		t.Fatal("a nil zone is in bounds")
	}
}

func BenchmarkScan(b *testing.B) {
	m := New()
	var frame strings.Builder
	for row := range 80 {
		for col := range 10 {
			frame.WriteString(m.Mark(string(rune('a'+row%26))+string(rune('0'+col)), "\x1b[32mcell text\x1b[m "))
		}
		frame.WriteString("\n")
	}
	s := frame.String()
	for b.Loop() {
		m.Scan(s)
	}
}
