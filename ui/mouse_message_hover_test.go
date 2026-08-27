package ui

import (
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	zone "github.com/lrstanley/bubblezone/v2"
)

// TestHoverLongMessageStraddlingViewportEdge guards against a regression
// where hovering a message whose tail had scrolled below the visible
// viewport failed to register at all, even over its still-visible header.
// bubblezone's Mark/Scan only reports a zone when both its start and end
// markers are found in the same scanned frame - a multi-line message
// straddling the viewport's bottom edge has its end marker scrolled out of
// view, so Get(zoneMessage(i)) answers with stale bounds from whenever it
// was last fully on screen instead of the message's current position.
// zoneUnderMouse must use line-offset arithmetic (messageIndexAtMouse)
// instead of relying on that zone's own (possibly stale) bounds.
func TestHoverLongMessageStraddlingViewportEdge(t *testing.T) {
	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	m.mouseEnabled = true

	items := []list.Item{Chat{Name: "bob", Address: "bob@example.com"}}
	msgs := []Message{
		{ID: "0", Author: "bob", Content: "short one", SentAt: time.Now()},
		{ID: "1", Author: "bob", Content: strings.Repeat("word ", 60), SentAt: time.Now()},
		{ID: "2", Author: "bob", Content: "short two", SentAt: time.Now()},
	}
	m.accounts = []Account{{Chats: items, Messages: map[int][]Message{0: msgs}}}
	m.currentAccount = 0
	m.chats.SetItems(items)
	m.chats.Select(0)
	m.selectedView = viewChat
	m.width, m.termHeight = 80, 10
	m.updateSizes()
	m.refreshViewport()

	if len(m.msgOffsets) != 3 {
		t.Fatalf("got %d message offsets, want 3", len(m.msgOffsets))
	}
	msg1Header := m.msgOffsets[1]
	msg2Header := m.msgOffsets[2]
	if msg2Header-msg1Header < 2 {
		t.Fatalf("message 1 needs multiple wrapped lines for this test, only spans %d lines", msg2Header-msg1Header)
	}

	// Scroll so message 1's header is the last fully-guaranteed-visible
	// line and its body/message 2 fall past the viewport's bottom edge.
	height := m.viewport.Height()
	m.viewport.SetYOffset(msg1Header - height + 1)

	_ = m.View() // populate zone bounds (zonePaneViewport etc.)

	// bubblezone's Scan() hands its results to a background goroutine
	// (zoneWorker) over a channel rather than storing them before returning
	// - per its own doc comment, "an immediate call to Get(id) may not
	// return the correct information" right after a scan. Poll briefly
	// instead of asserting on the very next line.
	var vp *zone.ZoneInfo
	for range 100 {
		if vp = m.zone.Get(zonePaneViewport); !vp.IsZero() {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if vp.IsZero() {
		t.Fatal("zonePaneViewport not marked after View()")
	}
	headerRow := vp.StartY + (msg1Header - m.viewport.YOffset())
	if headerRow < vp.StartY || headerRow > vp.EndY {
		t.Fatalf("message 1's header row %d is not within the visible viewport [%d, %d] - test setup is wrong", headerRow, vp.StartY, vp.EndY)
	}

	mouse := tea.Mouse{X: vp.StartX + 1, Y: headerRow}
	got := m.zoneUnderMouse(tea.MouseMotionMsg(mouse))
	if got != zoneMessage(1) {
		t.Fatalf("hovering message 1's visible header returned zone %q, want %q (message 1's tail is scrolled off-screen, its own bubblezone mark is stale)", got, zoneMessage(1))
	}
}
