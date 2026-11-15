package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jim-ww/kage/ui/cells"
)

// Frame-sized blocks are measured line by line on every View, and
// ansi.StringWidth segments graphemes even for plain ASCII. These are
// lipgloss's JoinVertical(Left)/JoinHorizontal(Top) and a foreground-only
// Render, byte for byte (see frame_test.go), measuring with cells.Width.

// blockLines is lipgloss's getLines with textWidth.
func blockLines(s string) (lines []string, widest int) {
	s = strings.ReplaceAll(s, "\t", "    ")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines = strings.Split(s, "\n")
	for _, l := range lines {
		widest = max(widest, cells.Width(l))
	}
	return lines, widest
}

// joinVertical is lipgloss.JoinVertical(lipgloss.Left, strs...).
func joinVertical(strs ...string) string {
	if len(strs) == 0 {
		return ""
	}
	if len(strs) == 1 {
		return strs[0]
	}
	blocks := make([][]string, len(strs))
	widest := 0
	for i, s := range strs {
		var w int
		blocks[i], w = blockLines(s)
		widest = max(widest, w)
	}
	var b strings.Builder
	for i, block := range blocks {
		for j, line := range block {
			if i > 0 || j > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(line)
			b.WriteString(strings.Repeat(" ", widest-cells.Width(line)))
		}
	}
	return b.String()
}

// joinHorizontal is lipgloss.JoinHorizontal(lipgloss.Top, strs...).
func joinHorizontal(strs ...string) string {
	if len(strs) == 0 {
		return ""
	}
	if len(strs) == 1 {
		return strs[0]
	}
	blocks := make([][]string, len(strs))
	widths := make([]int, len(strs))
	height := 0
	for i, s := range strs {
		blocks[i], widths[i] = blockLines(s)
		height = max(height, len(blocks[i]))
	}
	var b strings.Builder
	for row := range height {
		if row > 0 {
			b.WriteByte('\n')
		}
		for i, block := range blocks {
			line := ""
			if row < len(block) {
				line = block[row]
			}
			b.WriteString(line)
			b.WriteString(strings.Repeat(" ", widths[i]-cells.Width(line)))
		}
	}
	return b.String()
}

// fgBlock renders a block like a lipgloss style setting only a foreground:
// sgr is that style's escape sequence, empty for NoColor.
type fgBlock struct{ sgr string }

// newFgBlock is st's fgBlock; st must set nothing but a foreground.
func newFgBlock(st lipgloss.Style) fgBlock {
	r := st.Render("x")
	if r == "x" {
		return fgBlock{}
	}
	return fgBlock{sgr: strings.TrimSuffix(r, "x"+ansi.ResetStyle)}
}

// Render is st.Render(s) for newFgBlock(st).
func (f fgBlock) Render(s string) string {
	if f.sgr == "" {
		// lipgloss replaces \r\n again when aligning, which with no escapes
		// between catches a \r left before one.
		s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r\n", "\n")
	}
	lines, widest := blockLines(s)
	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		if f.sgr != "" {
			b.WriteString(f.sgr)
			b.WriteString(line)
			b.WriteString(ansi.ResetStyle)
		} else {
			b.WriteString(line)
		}
		if len(lines) > 1 {
			b.WriteString(strings.Repeat(" ", widest-cells.Width(line)))
		}
	}
	return b.String()
}
