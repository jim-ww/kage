package modal

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/lrstanley/bubblezone/v2"
)

// Line is a line of a Text. One with a Value is actionable: selected with
// ↑↓, acted on with enter or a click, which hands Value to the modal's
// OnAct.
type Line struct {
	Text  string
	Value any
}

// Text shows lines, scrolling when they don't fit: ↑↓ (or k/j), pgup/pgdn,
// home/end and the wheel. With actionable lines, ↑↓ move between those
// instead, scrolling to keep the selected one in view.
type Text struct {
	Lines []Line
	// ActLabel names what enter does on an actionable line, for the hint
	// row.
	ActLabel string
	Styles   TextStyles

	selected int // index into Lines of the selected actionable line, or -1
	hover    int // index into Lines under the pointer, or -1
	scroll   int
	width    int
	height   int
	zone     *zone.Manager
	id       string
}

// TextStyles style a Text's actionable lines.
type TextStyles struct {
	Selected lipgloss.Style
	Hover    lipgloss.Style
}

// NewText shows lines, the first actionable one selected.
func NewText(lines ...Line) *Text {
	t := &Text{
		Lines: lines,
		Styles: TextStyles{
			Selected: lipgloss.NewStyle().Reverse(true),
			Hover:    lipgloss.NewStyle().Underline(true),
		},
		selected: -1,
		hover:    -1,
	}
	t.selected = t.nextActionable(-1, 1)
	return t
}

// TextLines is NewText for plain, non-actionable lines.
func TextLines(lines ...string) *Text {
	ls := make([]Line, len(lines))
	for i, l := range lines {
		ls[i] = Line{Text: l}
	}
	return NewText(ls...)
}

// SetLines replaces the lines, keeping the selection on the same index
// where it's still actionable.
func (t *Text) SetLines(lines ...Line) {
	t.Lines = lines
	if t.selected < 0 || t.selected >= len(lines) || lines[t.selected].Value == nil {
		t.selected = t.nextActionable(-1, 1)
	}
	t.clampScroll()
}

func (t *Text) nextActionable(from, dir int) int {
	for i := from + dir; i >= 0 && i < len(t.Lines); i += dir {
		if t.Lines[i].Value != nil {
			return i
		}
	}
	return -1
}

func (t *Text) visible() int {
	if t.height <= 0 {
		return len(t.Lines)
	}
	return t.height
}

func (t *Text) overflows() bool { return len(t.Lines) > t.visible() }

func (t *Text) clampScroll() {
	t.scroll = max(0, min(t.scroll, len(t.Lines)-t.visible()))
}

func (t *Text) scrollBy(n int) {
	t.scroll += n
	t.clampScroll()
}

// show scrolls line i into view.
func (t *Text) show(i int) {
	if i < t.scroll {
		t.scroll = i
	} else if i >= t.scroll+t.visible() {
		t.scroll = i - t.visible() + 1
	}
	t.clampScroll()
}

func (t *Text) move(dir int) {
	if t.selected < 0 {
		t.scrollBy(dir)
		return
	}
	if next := t.nextActionable(t.selected, dir); next >= 0 {
		t.selected = next
		t.show(next)
	}
}

func (t *Text) lineID(i int) string { return t.id + "-line-" + strconv.Itoa(i) }

func (t *Text) lineAt(msg tea.MouseMsg) int {
	if t.zone == nil {
		return -1
	}
	for i := t.scroll; i < min(len(t.Lines), t.scroll+t.visible()); i++ {
		if t.Lines[i].Value != nil && t.zone.Get(t.lineID(i)).InBounds(msg) {
			return i
		}
	}
	return -1
}

func (t *Text) Update(msg tea.Msg) (tea.Cmd, Outcome) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "up", "k":
			t.move(-1)
		case "down", "j":
			t.move(1)
		case "pgup":
			t.scrollBy(-t.visible())
		case "pgdown":
			t.scrollBy(t.visible())
		case "home":
			t.scroll = 0
		case "end":
			t.scroll = len(t.Lines)
			t.clampScroll()
		case "enter":
			if t.selected >= 0 {
				return nil, Act(t.Lines[t.selected].Value)
			}
		}
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			t.scrollBy(-1)
		case tea.MouseWheelDown:
			t.scrollBy(1)
		}
	case tea.MouseMotionMsg:
		t.hover = t.lineAt(msg)
	case tea.MouseClickMsg:
		if i := t.lineAt(msg); i >= 0 && msg.Button == tea.MouseLeft {
			t.selected = i
			return nil, Act(t.Lines[i].Value)
		}
	}
	return nil, Continue
}

func (t *Text) View() string {
	end := min(len(t.Lines), t.scroll+t.visible())
	out := make([]string, 0, end-t.scroll)
	for i := t.scroll; i < end; i++ {
		line := t.Lines[i].Text
		if t.Lines[i].Value != nil {
			switch {
			case i == t.selected:
				line = t.Styles.Selected.Render(line)
			case i == t.hover:
				line = t.Styles.Hover.Render(line)
			}
			if t.zone != nil {
				line = t.zone.Mark(t.lineID(i), line)
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func (t *Text) SetSize(width, height int) {
	t.width, t.height = width, height
	t.clampScroll()
}

func (t *Text) PreferredWidth() int {
	w := 0
	for _, l := range t.Lines {
		w = max(w, lipgloss.Width(l.Text))
	}
	return w
}

func (t *Text) Bind(z *zone.Manager, id string) { t.zone, t.id = z, id }

func (t *Text) Hints() []Hint {
	var hints []Hint
	if t.selected >= 0 {
		label := t.ActLabel
		if label == "" {
			label = "select"
		}
		hints = append(hints, KeyHint(enterKey, label))
	}
	if t.overflows() {
		hints = append(hints,
			KeyHint(tea.KeyPressMsg{Code: tea.KeyPgUp}, "page up"),
			KeyHint(tea.KeyPressMsg{Code: tea.KeyPgDown}, "page down"))
	}
	return hints
}
