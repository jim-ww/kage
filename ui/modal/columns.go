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

// Columns lays sections out in side-by-side columns: each column filled
// top to bottom with whole sections as far as the height allows, a new one
// started when the next section doesn't fit - so a long reference uses the
// screen's width instead of one tall column that has to be scrolled, and a
// tall screen isn't left half empty. Only a section taller than the whole
// height is split, its title repeated over the rest. More columns than
// fit the width, the columns grow taller instead, scrolling like a Text
// (which it is underneath).
type Columns struct {
	*Text
	Sections []Section
	// Gap is the space between columns (3 if zero).
	Gap        int
	TitleStyle lipgloss.Style

	height int // the height it was last given, which sets its columns
}

// NewColumns lays out sections.
func NewColumns(sections ...Section) *Columns {
	c := &Columns{Text: NewText(), Sections: sections, TitleStyle: lipgloss.NewStyle().Bold(true)}
	c.layout(0, 0)
	return c
}

func (c *Columns) SetSize(width, height int) {
	c.height = height
	c.layout(width, height)
	c.Text.SetSize(width, height)
}

// PreferredWidth is as wide as the columns its height packs the sections
// into - every section side by side before it knows its height.
func (c *Columns) PreferredWidth() int {
	n := len(c.Sections)
	if c.height > 0 {
		n = len(c.pack(c.height))
	}
	return n*c.colWidth() + (n-1)*c.gap()
}

func (c *Columns) colWidth() int {
	w := 0
	for _, s := range c.Sections {
		w = max(w, sectionWidth(s))
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
	w := lipgloss.Width(s.Title) + len(" (cont.)")
	for _, l := range s.Lines {
		w = max(w, lipgloss.Width(l))
	}
	return w
}

// pack fills columns of at most height rows with the sections, whole where
// they fit; height 0 is one column.
func (c *Columns) pack(height int) [][]string {
	var cols [][]string
	var col []string
	newCol := func() {
		if len(col) > 0 {
			cols = append(cols, col)
		}
		col = nil
	}
	for _, s := range c.Sections {
		need := 1 + len(s.Lines)
		if len(col) > 0 {
			need++ // the blank line before it
		}
		if height > 0 && len(col) > 0 && len(col)+need > height {
			newCol()
		}
		if len(col) > 0 {
			col = append(col, "")
		}
		col = append(col, c.TitleStyle.Render(s.Title))
		for _, l := range s.Lines {
			if height > 0 && len(col) >= height {
				// Taller than the whole height: on in the next column,
				// under its title again.
				newCol()
				col = append(col, c.TitleStyle.Render(s.Title+" (cont.)"))
			}
			col = append(col, l)
		}
	}
	newCol()
	return cols
}

// layout packs the sections into columns of the height given - or, when
// that takes more columns than fit the width, into as many as fit, made
// just as much taller as they need (and scrolled).
func (c *Columns) layout(width, height int) {
	colWidth := c.colWidth()
	fit := 1
	if width > 0 && colWidth > 0 {
		fit = max(1, (width+c.gap())/(colWidth+c.gap()))
	}
	cols := c.pack(height)
	for h := height + 1; len(cols) > fit; h++ {
		cols = c.pack(h)
	}

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
