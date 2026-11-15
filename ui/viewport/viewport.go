// Package viewport is a vertically scrolling window onto lines of content:
// only what kage's message view needs, so View costs no more than padding
// the visible lines.
package viewport

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jim-ww/kage/ui/cells"
)

// wheelDelta is how many lines one mouse wheel tick scrolls.
const wheelDelta = 3

// Model is the viewport's state. The zero value is an empty viewport.
type Model struct {
	width, height int
	yOffset       int
	lines         []string
}

// New returns an empty viewport.
func New() Model { return Model{} }

func (m *Model) Width() int          { return m.width }
func (m *Model) Height() int         { return m.height }
func (m *Model) SetWidth(w int)      { m.width = w }
func (m *Model) SetHeight(h int)     { m.height = h }
func (m *Model) YOffset() int        { return m.yOffset }
func (m *Model) TotalLineCount() int { return len(m.lines) }

// SetContent sets the content, split into lines on '\n'.
func (m *Model) SetContent(s string) {
	if s == "" {
		m.SetContentLines(nil)
		return
	}
	m.SetContentLines(strings.Split(s, "\n"))
}

// SetContentLines sets the content, one line per element. The viewport
// keeps lines without copying it.
func (m *Model) SetContentLines(lines []string) {
	m.lines = lines
	if m.yOffset > m.maxYOffset() {
		m.GotoBottom()
	}
}

func (m *Model) maxYOffset() int { return max(0, len(m.lines)-m.height) }

// SetYOffset scrolls so line n is at the top, clamped to the content.
func (m *Model) SetYOffset(n int) { m.yOffset = min(max(n, 0), m.maxYOffset()) }

func (m *Model) AtTop() bool    { return m.yOffset <= 0 }
func (m *Model) AtBottom() bool { return m.yOffset >= m.maxYOffset() }
func (m *Model) GotoTop()       { m.SetYOffset(0) }
func (m *Model) GotoBottom()    { m.SetYOffset(m.maxYOffset()) }

// Update scrolls a page on pgup/pgdown and wheelDelta lines per wheel tick.
func (m *Model) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "pgup":
			m.SetYOffset(m.yOffset - m.height)
		case "pgdown":
			m.SetYOffset(m.yOffset + m.height)
		}
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.SetYOffset(m.yOffset - wheelDelta)
		case tea.MouseWheelDown:
			m.SetYOffset(m.yOffset + wheelDelta)
		}
	}
	return nil
}

// View is the visible lines, each padded (or cut) to Width, padded with
// blank lines to Height.
func (m *Model) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	blank := strings.Repeat(" ", m.width)
	var b strings.Builder
	for i := range m.height {
		if i > 0 {
			b.WriteByte('\n')
		}
		idx := m.yOffset + i
		if idx >= len(m.lines) {
			b.WriteString(blank)
			continue
		}
		line := m.lines[idx]
		w := cells.Width(line)
		if w > m.width {
			line = ansi.Truncate(line, m.width, "")
			w = cells.Width(line)
		}
		b.WriteString(line)
		b.WriteString(blank[:m.width-w])
	}
	return b.String()
}
