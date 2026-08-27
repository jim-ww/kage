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
	m := Model{styles: styles, flashMsgIdx: -1, selectedMsg: -1}
	msg := Message{
		Author:  "bob",
		Content: "this is a fairly long message that should wrap across multiple lines in the chat viewport for testing",
		SentAt:  time.Now(),
	}

	out := m.renderMessage(msg, 0, 40, []Message{msg})
	lines := strings.Split(out, "\n")
	// header+first body line, at least one more wrapped body line, plus the
	// trailing time/status line.
	if len(lines) < 4 {
		t.Fatalf("expected a header+body line, a wrapped continuation, and a status line, got %d: %q", len(lines), out)
	}

	fg := styles.plainText.Render("x")
	fgCode, _, ok := strings.Cut(fg, "x")
	if !ok || fgCode == "" {
		t.Fatalf("could not extract foreground SGR prefix from %q", fg)
	}

	// lines[0] carries the header ("name: ") plus the body's first wrapped
	// line inline; every body line (all but the last, which is the
	// time/status line) must carry its own foreground code so it can't
	// silently inherit some other line's color via an embedded ANSI reset.
	for i, line := range lines[:len(lines)-1] {
		if !strings.Contains(line, fgCode) {
			t.Fatalf("line %d missing explicit foreground code %q: %q", i, fgCode, line)
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
