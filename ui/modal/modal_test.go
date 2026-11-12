package modal

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/lrstanley/bubblezone/v2"

	"github.com/jim-ww/kage/ui/datepicker"
)

// host stands in for the embedding program: it records what its modals
// handed back.
type host struct {
	submitted []any
	canceled  int
}

func newStack(width, height int) (*Stack[*host], *zone.Manager) {
	zm := zone.New()
	s := &Stack[*host]{Zone: zm}
	s.SetSize(width, height)
	return s, zm
}

func open(s *Stack[*host], title string, body Component) {
	s.Push(&Modal[*host]{
		Title:    title,
		Body:     body,
		OnSubmit: func(h *host, v any) tea.Cmd { h.submitted = append(h.submitted, v); return nil },
		OnCancel: func(h *host) tea.Cmd { h.canceled++; return nil },
	})
}

// draw renders the stack's view through the zone manager and waits for
// zone to be recorded (bubblezone records asynchronously).
func draw(t *testing.T, s *Stack[*host], zm *zone.Manager, zoneID string) *zone.ZoneInfo {
	t.Helper()
	zm.Scan(s.View())
	for range 200 {
		if z := zm.Get(zoneID); !z.IsZero() {
			return z
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("zone %q never drawn", zoneID)
	return nil
}

func click(z *zone.ZoneInfo) tea.MouseClickMsg {
	return tea.MouseClickMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseLeft}
}

func typeText(s *Stack[*host], h *host, text string) {
	for _, r := range text {
		s.Update(h, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func TestInputKeyboardOnly(t *testing.T) {
	s, _ := newStack(80, 20)
	h := &host{}
	open(s, "Rename", NewInput("", ""))
	typeText(s, h, "bob")
	s.Update(h, enterKey)
	if s.Open() || len(h.submitted) != 1 || h.submitted[0] != "bob" {
		t.Fatalf("enter: open=%v submitted=%v, want \"bob\" and closed", s.Open(), h.submitted)
	}
}

func TestInputMouseOnly(t *testing.T) {
	s, zm := newStack(80, 20)
	h := &host{}
	open(s, "Rename", NewInput("bob", ""))
	// The hint row's "enter ok" is a button doing what enter does.
	s.Update(h, click(draw(t, s, zm, s.hintID(0))))
	if s.Open() || len(h.submitted) != 1 || h.submitted[0] != "bob" {
		t.Fatalf("clicking enter's hint: submitted=%v", h.submitted)
	}
	// So is esc's, and clicking outside closes too.
	open(s, "Rename", NewInput("", ""))
	hints, _ := s.hints(s.Top())
	s.Update(h, click(draw(t, s, zm, s.hintID(len(hints)-1))))
	open(s, "Rename", NewInput("", ""))
	draw(t, s, zm, s.frameID())
	s.Update(h, tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	if s.Open() || h.canceled != 2 {
		t.Fatalf("esc's hint and a click outside: canceled %d times, want 2", h.canceled)
	}
}

func TestConfirmBothWays(t *testing.T) {
	s, zm := newStack(80, 20)
	h := &host{}
	open(s, "Delete?", NewConfirm("bob: hi"))
	s.Update(h, tea.KeyPressMsg{Code: 'y', Text: "y"})
	open(s, "Delete?", NewConfirm("bob: hi"))
	s.Update(h, tea.KeyPressMsg{Code: 'n', Text: "n"})
	open(s, "Delete?", NewConfirm("bob: hi"))
	s.Update(h, click(draw(t, s, zm, s.hintID(0)))) // "y yes"
	open(s, "Delete?", NewConfirm("bob: hi"))
	s.Update(h, click(draw(t, s, zm, s.hintID(1)))) // "n no"
	if len(h.submitted) != 2 || h.canceled != 2 {
		t.Fatalf("y, n, click yes, click no: submitted %v, canceled %d", h.submitted, h.canceled)
	}
}

func TestTabsBothWays(t *testing.T) {
	s, zm := newStack(80, 24)
	h := &host{}
	day := time.Date(2026, time.June, 10, 0, 0, 0, 0, time.Local)
	tabs := NewTabs(Tab{Title: "Text", Body: NewInput("", "")}, Tab{Title: "Date", Body: NewDate(datepicker.New(day))})
	open(s, "Search", tabs)

	s.Update(h, tabKey)
	if tabs.Active() != 1 {
		t.Fatal("tab didn't switch tabs")
	}
	s.Update(h, click(draw(t, s, zm, tabs.titleID(0))))
	if tabs.Active() != 0 {
		t.Fatal("clicking a tab's title didn't switch to it")
	}
	typeText(s, h, "needle")
	s.Update(h, enterKey)
	if r, ok := h.submitted[0].(TabResult); !ok || r.Tab != 0 || r.Value != "needle" {
		t.Fatalf("text tab submitted %#v", h.submitted[0])
	}

	open(s, "Search", tabs)
	tabs.Select(1)
	day3 := draw(t, s, zm, tabs.bodyID(1)+"-day-20260603")
	s.Update(h, click(day3))
	if r, ok := h.submitted[1].(TabResult); !ok || r.Tab != 1 || !r.Value.(time.Time).Equal(day.AddDate(0, 0, -7)) {
		t.Fatalf("clicking 3 June submitted %#v", h.submitted[1])
	}
}

func TestSmallScreens(t *testing.T) {
	// Room for the border: the frame costs 2 rows, title and hints in it.
	s, _ := newStack(40, 10)
	open(s, "Delete message?", NewConfirm("bob: hi"))
	lines := strings.Split(strings.TrimRight(ansi.Strip(s.View()), "\n "), "\n")
	var frame []string
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			frame = append(frame, strings.TrimSpace(l))
		}
	}
	if len(frame) != 3 || !strings.Contains(frame[0], "Delete message?") || !strings.Contains(frame[2], "esc close") {
		t.Fatalf("want title in the top border, one body row, hints in the bottom one:\n%s", strings.Join(frame, "\n"))
	}

	// Too narrow for a border: none, and hints that don't fit give way,
	// esc last of all.
	s, _ = newStack(24, 10)
	open(s, "Search", NewTabs(Tab{Title: "Text", Body: NewInput("", "")}, Tab{Title: "Date", Body: NewDate(datepicker.New(time.Now()))}))
	view := ansi.Strip(s.View())
	if strings.Contains(view, "╭") || !strings.Contains(view, "esc close") {
		t.Fatalf("narrow: want no border and esc kept:\n%s", view)
	}
	for _, l := range strings.Split(view, "\n") {
		if ansi.StringWidth(l) > 24 {
			t.Fatalf("narrow: line wider than the space: %q", l)
		}
	}

	// Too short for the body: it scrolls, and says so.
	s, _ = newStack(60, 6)
	h := &host{}
	open(s, "Pick a day", NewDate(datepicker.New(time.Now())))
	if !strings.Contains(ansi.Strip(s.View()), "scroll") {
		t.Fatal("an overflowing body doesn't offer scrolling")
	}
	before := ansi.Strip(s.View())
	s.Update(h, scrollDown)
	if ansi.Strip(s.View()) == before {
		t.Fatal("ctrl+d didn't scroll the overflowing body")
	}
	if got := strings.Count(ansi.Strip(s.View()), "\n") + 1; got > 6 {
		t.Fatalf("view is %d rows in a 6-row space", got)
	}
}

func TestOtherMessagesReachTheProgram(t *testing.T) {
	s, _ := newStack(80, 20)
	h := &host{}
	open(s, "Rename", NewInput("", ""))
	type incoming struct{}
	if _, consumed := s.Update(h, incoming{}); consumed {
		t.Fatal("a non-input message was swallowed by the modal")
	}
	if _, consumed := s.Update(h, tea.PasteMsg{Content: "x"}); !consumed {
		t.Fatal("a paste leaked past the modal")
	}
}

// TestFrameKeepsItsWidth: what a frame is wide for changes as it's used
// (another tab has other hints), but it must not resize under the pointer.
func TestFrameKeepsItsWidth(t *testing.T) {
	s, _ := newStack(80, 24)
	open(s, "Search", NewTabs(Tab{Title: "Text", Body: NewInput("", "")}, Tab{Title: "Go to date", Body: NewDate(datepicker.New(time.Now()))}))
	width := func() int { return ansi.StringWidth(strings.TrimSpace(strings.Split(strings.TrimLeft(ansi.Strip(s.View()), "\n "), "\n")[0])) }
	before := width()
	s.Update(&host{}, tabKey)
	if after := width(); after != before {
		t.Fatalf("frame went from %d to %d columns wide on switching tabs", before, after)
	}
}
