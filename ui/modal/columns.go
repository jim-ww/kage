package modal

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Section is a titled block of lines for Columns.
type Section struct {
	Title string
	Lines []string
}

// Columns flows sections down side-by-side columns, as many as fit the
// width it's given - a long reference uses the screen's width instead of
// one tall column that has to be scrolled. Narrower than two columns, it's
// one; taller than its space, it scrolls like a Text (which it is
// underneath).
type Columns struct {
	*Text
	Sections []Section
	// Gap is the space between columns (3 if zero).
	Gap        int
	TitleStyle lipgloss.Style
}

// NewColumns lays out sections.
func NewColumns(sections ...Section) *Columns {
	c := &Columns{Text: NewText(), Sections: sections, TitleStyle: lipgloss.NewStyle().Bold(true)}
	c.layout(0)
	return c
}

func (c *Columns) SetSize(width, height int) {
	c.layout(width)
	c.Text.SetSize(width, height)
}

// PreferredWidth is everything side by side - as wide as the space allows.
func (c *Columns) PreferredWidth() int {
	w := 0
	for i, s := range c.Sections {
		if i > 0 {
			w += c.gap()
		}
		w += sectionWidth(s)
	}
	return w
}

func (c *Columns) gap() int {
	if c.Gap > 0 {
		return c.Gap
	}
	return 3
}

func sectionWidth(s Section) int {
	w := lipgloss.Width(s.Title)
	for _, l := range s.Lines {
		w = max(w, lipgloss.Width(l))
	}
	return w
}

// layout flows the sections down as many columns as fit width (one for
// width 0), each column about as tall as the others: a long section goes on
// in the next column rather than leaving the rest half empty, and a title
// never ends a column apart from its lines. The result goes to the Text.
func (c *Columns) layout(width int) {
	colWidth := 0
	for _, s := range c.Sections {
		colWidth = max(colWidth, sectionWidth(s))
	}
	type item struct {
		text  string
		title bool
	}
	var items []item
	for i, s := range c.Sections {
		if i > 0 {
			items = append(items, item{})
		}
		items = append(items, item{text: c.TitleStyle.Render(s.Title), title: true})
		for _, l := range s.Lines {
			items = append(items, item{text: l})
		}
	}
	n := 1
	if width > 0 && colWidth > 0 {
		n = max(1, (width+c.gap())/(colWidth+c.gap()))
	}
	height := (len(items) + n - 1) / n
	var cols [][]string
	var col []string
	for i, it := range items {
		full := len(col) >= height
		// A title starting the column's last row would be cut off from
		// its lines: it starts the next column instead.
		if !full && it.title && len(col) == height-1 && len(cols) < n-1 {
			full = true
		}
		if full && len(cols) < n-1 {
			cols, col = append(cols, col), nil
		}
		if len(col) == 0 && it.text == "" && !it.title && i > 0 {
			continue // no column starts with the gap between sections
		}
		col = append(col, it.text)
	}
	cols = append(cols, col)

	rows := 0
	for _, col := range cols {
		rows = max(rows, len(col))
	}
	lines := make([]Line, rows)
	pad := strings.Repeat(" ", c.gap())
	for r := range rows {
		var b strings.Builder
		for i, col := range cols {
			cell := ""
			if r < len(col) {
				cell = col[r]
			}
			if i < len(cols)-1 {
				cell += strings.Repeat(" ", max(0, colWidth-lipgloss.Width(cell))) + pad
			}
			b.WriteString(cell)
		}
		lines[r] = Line{Text: b.String()}
	}
	c.Text.SetLines(lines...)
}
