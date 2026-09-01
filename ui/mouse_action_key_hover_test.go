package ui

import (
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

// TestReplyKeyHoverRegistersOnSingleCellGlyph guards against a regression
// where the "↩"/"+" reply/react key glyphs - each exactly one column wide -
// could never register as hovered. isReplyKeyHovered/isReactKeyHovered (and
// their expand/reaction-chip counterparts) compared the pointer's x against
// z.EndX with a strict "<", but bubblezone's own ZoneInfo.EndX is inclusive
// (the bottom-right cell, not one past it - see its InBounds using
// "event.X > z.EndX"). For a multi-column zone that only shrinks the
// hoverable area by one column; for a single-column zone (StartX == EndX)
// it excludes the only valid column entirely, so the pointer could sit
// exactly on the glyph and still read as not-hovered.
func TestReplyKeyHoverRegistersOnSingleCellGlyph(t *testing.T) {
	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	m.mouseEnabled = true

	items := []list.Item{Chat{Name: "bob", Address: "bob@example.com"}}
	msgs := []Message{
		{ID: "0", Author: "bob", Content: "hello there", SentAt: time.Now()},
	}
	m.accounts = []Account{{Chats: items, Messages: map[int][]Message{0: msgs}}}
	m.currentAccount = 0
	m.chats.SetItems(items)
	m.chats.Select(0)
	m.selectedView = viewChat
	m.width, m.termHeight = 80, 20
	m.updateSizes()
	m.refreshViewport()

	// Hover the row first so the reply/react buttons render at all (they're
	// only drawn for the selected message).
	_ = m.View()
	var vp = m.zone.Get(zonePaneViewport)
	for range 100 {
		if vp = m.zone.Get(zonePaneViewport); !vp.IsZero() {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if vp.IsZero() {
		t.Fatal("zonePaneViewport not marked after View()")
	}
	mm, _ := m.handleMouseMotion(tea.MouseMotionMsg(tea.Mouse{X: vp.StartX + 1, Y: vp.StartY}))
	m = mm.(Model)
	if m.selectedMsg != 0 {
		t.Fatalf("selectedMsg = %d, want 0 after hovering its row", m.selectedMsg)
	}

	// Re-render now that the reply/react buttons are showing, then find the
	// reply glyph's own zone.
	_ = m.View()
	var zk = m.zone.Get(zoneMessageReplyKey(0))
	for range 100 {
		if zk = m.zone.Get(zoneMessageReplyKey(0)); !zk.IsZero() {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if zk.IsZero() {
		t.Fatal("zoneMessageReplyKey(0) never got marked - reply button isn't rendering")
	}
	if zk.StartX != zk.EndX {
		t.Fatalf("reply glyph zone spans multiple columns (StartX=%d EndX=%d) - this test needs a single-column zone to catch the regression", zk.StartX, zk.EndX)
	}

	mm, _ = m.handleMouseMotion(tea.MouseMotionMsg(tea.Mouse{X: zk.StartX, Y: zk.StartY}))
	m = mm.(Model)
	if !m.isReplyKeyHovered(0) {
		t.Fatalf("isReplyKeyHovered(0) = false with pointer exactly on the glyph at (%d,%d), zone=%+v", zk.StartX, zk.StartY, zk)
	}
}
