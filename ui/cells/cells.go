// Package cells measures text in terminal cells.
package cells

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Width is ansi.StringWidth with a fast path for ASCII: a printable
// ASCII byte not followed by a non-ASCII one (a combining mark could join
// it) is a one-column grapheme on its own.
func Width(s string) int {
	w := 0
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c >= 0x20 && c < 0x7f && (i+1 == len(s) || s[i+1] < 0x80):
			w++
			i++
		case c == ansi.ESC:
			j := skipEscape(s, i)
			if j < 0 {
				return w + ansi.StringWidth(s[i:])
			}
			i = j
		case c < 0x20 || c == 0x7f:
			i++
		case c < 0x7f || c >= 0xc2 && c <= 0xf4:
			// What ansi.StringWidth does at a printable byte or UTF-8 lead.
			cluster, cw := ansi.FirstGraphemeCluster(s[i:], ansi.GraphemeWidth)
			w += cw
			i += len(cluster)
		default:
			return w + ansi.StringWidth(s[i:])
		}
	}
	return w
}

// skipEscape returns the index just past the CSI or OSC sequence starting
// at s[i], or -1 for any other escape, left to ansi.StringWidth.
func skipEscape(s string, i int) int {
	if i+1 >= len(s) {
		return -1
	}
	switch s[i+1] {
	case '[':
		for j := i + 2; j < len(s); j++ {
			if s[j] >= 0x40 && s[j] <= 0x7e {
				return j + 1
			}
			if s[j] < 0x20 || s[j] > 0x7e {
				return -1
			}
		}
	case ']':
		for j := i + 2; j < len(s); j++ {
			switch {
			case s[j] == 0x07:
				return j + 1
			case s[j] == ansi.ESC && j+1 < len(s) && s[j+1] == '\\':
				return j + 2
			case s[j] < 0x20:
				return -1
			}
		}
	}
	return -1
}

// BlockWidth is lipgloss.Width: the widest of s's lines.
func BlockWidth(s string) int {
	w := 0
	for line := range strings.SplitSeq(s, "\n") {
		w = max(w, Width(line))
	}
	return w
}
