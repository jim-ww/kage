package ui

import (
	"fmt"
	"strings"
	"testing"
)

// TestScrollNeverLosesOrDuplicatesMessages drives a chaotic up/down wheel
// sequence (same pseudo-random pattern as TestChaoticWheelScrollNeverGetsStuck
// in scroll_boundary_test.go) and checks the underlying data survives
// unscathed: the loaded message slice's length and every message's content
// stay exactly what they started as, and msgOffsets always has exactly one
// entry per loaded message. Scrolling only ever moves a read-only view over
// this data — it must never mutate, drop, or duplicate it.
func TestScrollNeverLosesOrDuplicatesMessages(t *testing.T) {
	const n = 300
	m, _ := newScrollBoundaryTestModel(t, n)

	wantContents := make([]string, n)
	for i, msg := range m.currentMessages() {
		wantContents[i] = msg.Content
	}

	seed := uint32(777)
	next := func() uint32 { seed = seed*1664525 + 1013904223; return seed }
	for i := 0; i < 250; i++ {
		if next()%2 == 0 {
			wheelUpInViewport(t, m)
		} else {
			wheelDownInViewport(t, m)
		}

		msgs := m.currentMessages()
		if len(msgs) != n {
			t.Fatalf("iteration %d: message count changed from %d to %d", i, n, len(msgs))
		}
		if len(m.msgOffsets) != n {
			t.Fatalf("iteration %d: msgOffsets has %d entries, want %d (one per message)", i, len(m.msgOffsets), n)
		}
	}

	msgs := m.currentMessages()
	for i, want := range wantContents {
		if msgs[i].Content != want {
			t.Fatalf("message %d content changed: got %q, want %q", i, msgs[i].Content, want)
		}
	}
}

// TestMsgOffsetsStrictlyIncreasing checks the offset table refreshViewport
// builds is well-formed: exactly one entry per loaded message, strictly
// increasing (every message occupies at least one line), and the last
// message's range doesn't run past the rendered content — msgIndexAtOffset's
// binary-search-like scan (and so all of mouse-wheel/keyboard scroll
// selection tracking) silently misbehaves if any of this doesn't hold.
func TestMsgOffsetsStrictlyIncreasing(t *testing.T) {
	const n = 150
	m, _ := newScrollBoundaryTestModel(t, n)

	if len(m.msgOffsets) != n {
		t.Fatalf("msgOffsets has %d entries, want %d", len(m.msgOffsets), n)
	}
	for i := 1; i < len(m.msgOffsets); i++ {
		if m.msgOffsets[i] <= m.msgOffsets[i-1] {
			t.Fatalf("msgOffsets not strictly increasing at %d: %d <= %d", i, m.msgOffsets[i], m.msgOffsets[i-1])
		}
	}
	if m.msgOffsets[len(m.msgOffsets)-1] >= len(m.viewportLines) {
		t.Fatalf("last message's offset %d is out of bounds of %d rendered lines", m.msgOffsets[len(m.msgOffsets)-1], len(m.viewportLines))
	}
}

// TestScrollToTopShowsFirstMessage checks the content-visibility half of
// reaching the true top of loaded history: the first loaded message must
// actually be on screen (right at the top edge, nothing above it hidden).
// It deliberately does not assert anything about selectedMsg — forcing the
// selection to jump to message 0 just because the viewport hit the top edge
// was the original reported bug (see TestSelectedMsgStaysWithinVisibleRangeDuringWheelScroll
// for the invariant that replaced it: selection tracks naturally and only
// moves when it would otherwise scroll off screen).
func TestScrollToTopShowsFirstMessage(t *testing.T) {
	const n = 200
	m, _ := newScrollBoundaryTestModel(t, n)

	for i := 0; i < 400 && !m.viewport.AtTop(); i++ {
		wheelUpInViewport(t, m)
	}
	if !m.viewport.AtTop() {
		t.Fatal("never reached the true top of loaded history")
	}
	if m.viewport.YOffset() != 0 {
		t.Fatalf("YOffset() = %d at the true top, want 0", m.viewport.YOffset())
	}
	if top := m.msgIndexAtOffset(0); top != 0 {
		t.Fatalf("topmost visible message index = %d at the true top, want 0 (the first loaded message)", top)
	}

	content := m.viewport.View()
	wantHead := fmt.Sprintf("content-%03d", 0)
	if !strings.Contains(content, wantHead) {
		t.Fatalf("viewport at top doesn't show the true first message %q:\n%s", wantHead, content)
	}
}

// TestScrollToBottomShowsLastMessage is
// TestScrollToTopShowsFirstMessage's mirror image at the tail — it's also
// TestWheelScrollUpThenDownReturnsToTail's assertion restated as its own
// test, run independently of the "scroll up then back down" scenario.
func TestScrollToBottomShowsLastMessage(t *testing.T) {
	const n = 200
	m, _ := newScrollBoundaryTestModel(t, n)

	for i := 0; i < 20; i++ {
		wheelUpInViewport(t, m)
	}
	for i := 0; i < 400 && !m.viewport.AtBottom(); i++ {
		wheelDownInViewport(t, m)
	}
	if !m.viewport.AtBottom() {
		t.Fatal("never reached the true bottom of loaded history")
	}

	content := m.viewport.View()
	wantTail := fmt.Sprintf("content-%03d", n-1)
	if !strings.Contains(content, wantTail) {
		t.Fatalf("viewport at bottom doesn't show the true last message %q:\n%s", wantTail, content)
	}
}

// TestSelectedMsgStaysWithinVisibleRangeDuringWheelScroll asserts, after
// every single wheel tick of a chaotic scroll, that selectedMsg is one of
// the messages actually visible on screen (between msgIndexAtOffset at the
// top and bottom viewport lines) — never a message scrolled off above or
// below it. This is the invariant handleMouseWheel's clamp
// (m.selectedMsg < top / > bottom) exists to hold; a naive
// "selection = topmost visible message" implementation (the earlier,
// reported-buggy behavior) would instead force selectedMsg == top on every
// tick, which this test also would have failed since selectedMsg would never
// sit anywhere else in the visible range.
func TestSelectedMsgStaysWithinVisibleRangeDuringWheelScroll(t *testing.T) {
	const n = 300
	m, _ := newScrollBoundaryTestModel(t, n)

	seed := uint32(42)
	next := func() uint32 { seed = seed*1664525 + 1013904223; return seed }
	for i := 0; i < 300; i++ {
		if next()%2 == 0 {
			wheelUpInViewport(t, m)
		} else {
			wheelDownInViewport(t, m)
		}

		if len(m.msgOffsets) == 0 {
			continue
		}
		yOffset := m.viewport.YOffset()
		top := m.msgIndexAtOffset(yOffset)
		bottom := m.msgIndexAtOffset(yOffset + max(0, m.viewport.Height()-1))
		if m.selectedMsg < top || m.selectedMsg > bottom {
			t.Fatalf("iteration %d: selectedMsg=%d outside visible range [%d,%d]", i, m.selectedMsg, top, bottom)
		}
	}
}

// TestSelectedMsgUnchangedWhenStillVisible checks the other half of the same
// fix: a wheel tick that keeps the current selection on screen must leave
// selectedMsg untouched rather than snapping it to whatever message is now
// topmost — otherwise every scroll tick would move the selection even when
// it never needed to.
func TestSelectedMsgUnchangedWhenStillVisible(t *testing.T) {
	const n = 300
	m, _ := newScrollBoundaryTestModel(t, n)

	// A single wheel notch scrolls a handful of lines — nowhere near a full
	// screen — so the message that was selected (the last loaded one, sitting
	// at the bottom of the viewport at rest) should still be visible after
	// one tick, and therefore still selected.
	before := m.selectedMsg
	wheelUpInViewport(t, m)
	if m.selectedMsg != before {
		yOffset := m.viewport.YOffset()
		top := m.msgIndexAtOffset(yOffset)
		bottom := m.msgIndexAtOffset(yOffset + max(0, m.viewport.Height()-1))
		if before >= top && before <= bottom {
			t.Fatalf("selectedMsg changed from %d to %d after one wheel notch even though %d was still visible (range [%d,%d])", before, m.selectedMsg, before, top, bottom)
		}
	}
}
