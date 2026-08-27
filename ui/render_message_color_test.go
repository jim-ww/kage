package ui

import (
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/textarea"
)

// TestRenderMessagePlainBodyHasOwnColor guards against a regression where the
// first line of a wrapped message body rendered in the wrong (default/dim)
// color: it relied on an outer Foreground style wrapping the whole viewport
// content, which only takes effect up to the header's own embedded ANSI
// reset. Each body line must now carry its own foreground code directly.
func TestRenderMessagePlainBodyHasOwnColor(t *testing.T) {
	styles := newUIStyles(DefaultTheme())
	m := Model{styles: styles, showNames: true, flashMsgIdx: -1, selectedMsg: -1}
	msg := Message{
		Author:  "bob",
		Content: "this is a fairly long message that should wrap across multiple lines in the chat viewport for testing",
		SentAt:  time.Now(),
	}

	out := m.renderMessage(msg, 0, 40, []Message{msg})
	lines := strings.Split(out, "\n")
	if len(lines) < 3 {
		t.Fatalf("expected a header line plus a body wrapped across multiple lines, got %d: %q", len(lines), out)
	}

	fg := styles.plainText.Render("x")
	fgCode, _, ok := strings.Cut(fg, "x")
	if !ok || fgCode == "" {
		t.Fatalf("could not extract foreground SGR prefix from %q", fg)
	}

	// lines[0] is the header (dir glyph/name/timestamp) on its own line, not
	// message body text - only the body lines that follow need their own
	// foreground code.
	for i, line := range lines[1:] {
		if !strings.Contains(line, fgCode) {
			t.Fatalf("line %d missing explicit foreground code %q: %q", i+1, fgCode, line)
		}
	}
}

// TestPlainTextLineSkipsAlreadyColoredText guards against re-wrapping
// chroma-highlighted code lines in another Foreground style, which would
// only survive up to the highlighter's own embedded resets.
func TestPlainTextLineSkipsAlreadyColoredText(t *testing.T) {
	styles := newUIStyles(DefaultTheme())
	colored := "\x1b[38;5;81mfunc\x1b[0m main()"

	if got := styles.plainTextLine(colored); got != colored {
		t.Fatalf("expected already-colored line to pass through unchanged, got %q", got)
	}

	plain := "just text"
	if got := styles.plainTextLine(plain); got == plain {
		t.Fatalf("expected plain line to gain a foreground style, got unchanged %q", got)
	}
}

// TestTextAreaCursorLineForegroundMatchesText guards against the bubbles
// textarea default CursorLine style's dim gray Foreground leaking through:
// applyTextAreaStyles must explicitly set CursorLine's Foreground to match
// Text's, not just clear the background.
func TestTextAreaCursorLineForegroundMatchesText(t *testing.T) {
	styles := newUIStyles(DefaultTheme())

	ta := textarea.New()
	applyTextAreaStyles(&ta, styles.colors)
	taStyles := ta.Styles()

	if got, want := taStyles.Focused.CursorLine.GetForeground(), taStyles.Focused.Text.GetForeground(); got != want {
		t.Fatalf("focused cursor line foreground %v does not match text foreground %v", got, want)
	}
	if got, want := taStyles.Blurred.CursorLine.GetForeground(), taStyles.Blurred.Text.GetForeground(); got != want {
		t.Fatalf("blurred cursor line foreground %v does not match text foreground %v", got, want)
	}
}
