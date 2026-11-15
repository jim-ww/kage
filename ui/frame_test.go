package ui

import (
	"math/rand/v2"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jim-ww/kage/ui/cells"
)

// framePieces are the kinds of text a frame is built of.
var framePieces = []string{
	"a", "hello", " ", "\t", "│", "─╮", "日本", "é", "e\u0301", "👍", "👩\u200d💻", "🇯🇵", "\u200d", "#", "\ufe0f",
	"\x1b[38;2;1;2;3m", "\x1b[m", "\x1b[1;4m", ansi.SetHyperlink("https://example.com/ü"), ansi.ResetHyperlink(),
	"\x1b]8;;x\x07", "\x1b(B", "\x1b", "\r", "\x7f", "\x00",
}

func randomFrameText(r *rand.Rand, lines int) string {
	var b strings.Builder
	for l := range lines {
		if l > 0 {
			b.WriteString("\n")
		}
		for range r.IntN(12) {
			b.WriteString(framePieces[r.IntN(len(framePieces))])
		}
	}
	return b.String()
}

func TestCellsWidthMatchesAnsi(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 20000 {
		s := randomFrameText(r, 1)
		if got, want := cells.Width(s), ansi.StringWidth(s); got != want {
			t.Fatalf("Width(%q) = %d, want %d", s, got, want)
		}
	}
}

func TestFrameHelpersMatchLipgloss(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	fg := lipgloss.NewStyle().Foreground(lipgloss.Color("#123456"))
	block := newFgBlock(fg)
	noFg := lipgloss.NewStyle().Foreground(lipgloss.NoColor{})
	noBlock := newFgBlock(noFg)
	for range 3000 {
		a, b, c := randomFrameText(r, 1+r.IntN(4)), randomFrameText(r, 1+r.IntN(4)), randomFrameText(r, 1+r.IntN(4))
		if got, want := joinVertical(a, b, c), lipgloss.JoinVertical(lipgloss.Left, a, b, c); got != want {
			t.Fatalf("joinVertical(%q, %q, %q) =\n%q\nwant\n%q", a, b, c, got, want)
		}
		if got, want := joinHorizontal(a, b, c), lipgloss.JoinHorizontal(lipgloss.Top, a, b, c); got != want {
			t.Fatalf("joinHorizontal(%q, %q, %q) =\n%q\nwant\n%q", a, b, c, got, want)
		}
		if got, want := block.Render(a), fg.Render(a); got != want {
			t.Fatalf("fgBlock.Render(%q) =\n%q\nwant\n%q", a, got, want)
		}
		if got, want := noBlock.Render(a), noFg.Render(a); got != want {
			t.Fatalf("NoColor fgBlock.Render(%q) =\n%q\nwant\n%q", a, got, want)
		}
	}
}
