package ui

import (
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

// newCacheEquivModel builds a model with fixed timestamps so two
// independently-built copies render byte-identically (the mouse-sweep
// bench model uses time.Now(), which can straddle a minute boundary).
func newCacheEquivModel() Model {
	items := make([]list.Item, 30)
	for i := range items {
		items[i] = Chat{
			Name:        "chat",
			Address:     "c" + string(rune('a'+i)) + "@example.com",
			LastMessage: "hey there, how's it going",
		}
	}
	base := time.Date(2025, 3, 4, 12, 0, 0, 0, time.UTC)
	msgs := make([]Message, 150)
	for i := range msgs {
		msgs[i] = Message{
			ID:      string(rune(i)),
			Author:  "bob",
			Content: "hello world this is a message with some more text to wrap around a bit, maybe a link too https://example.com/path",
			SentAt:  base.Add(time.Duration(i) * time.Second),
		}
	}

	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	m.mouseEnabled = true
	m.accounts = []Account{{Chats: items, Messages: map[int][]Message{0: msgs}}}
	m.currentAccount = 0
	m.chats.SetItems(items)
	m.chats.Select(0)
	m.selectedView = viewChat
	m.selectedMsg = len(msgs) - 1
	m.width, m.termHeight = 120, 40
	m.updateSizes()
	m.refreshViewport()
	m.viewport.GotoBottom()
	return m
}

// TestViewIdenticalWithWarmAndColdCaches is the end-to-end guard on all
// three render caches at once (rendered message rows, the chat list body,
// the viewport frame): a model driven through a mouse sweep — so every
// cache is populated, and populated with *other* rows' states — must
// render the final state byte-for-byte the same as a model that arrived
// there cold. Any cache key that fails to capture something renderMessage
// or the chat-list delegate reads shows up here as a diff.
func TestViewIdenticalWithWarmAndColdCaches(t *testing.T) {
	warm := newCacheEquivModel()
	_ = warm.View()

	// Sweep the pointer across a run of message rows and back, exactly as
	// a user dragging through the chat does.
	var points []tea.Mouse
	for i := 149 - 20; i < 150; i++ {
		if z := warm.zone.Get(zoneMessage(i)); z != nil {
			points = append(points, tea.Mouse{X: z.StartX, Y: z.StartY})
		}
	}
	if len(points) < 3 {
		t.Fatal("not enough visible message zones to sweep over")
	}
	for j := len(points) - 1; j >= 0; j-- {
		next, _ := warm.Update(tea.MouseMotionMsg(points[j]))
		warm = next.(Model)
		_ = warm.View()
	}
	// Land back on the last row in the sweep.
	final := points[len(points)-1]
	next, _ := warm.Update(tea.MouseMotionMsg(final))
	warm = next.(Model)
	warmView := warm.View().Content

	// A cold model driven straight to that same pointer position.
	cold := newCacheEquivModel()
	_ = cold.View()
	next, _ = cold.Update(tea.MouseMotionMsg(final))
	cold = next.(Model)
	coldView := cold.View().Content

	if warm.selectedMsg != cold.selectedMsg {
		t.Fatalf("models are not in the same state: warm selected %d, cold selected %d", warm.selectedMsg, cold.selectedMsg)
	}
	if warmView != coldView {
		t.Errorf("warm-cache render differs from cold-cache render (warm %d bytes, cold %d bytes)", len(warmView), len(coldView))
	}
}

// TestViewIdenticalAfterIncomingMessage covers the invalidation direction:
// caches warmed on one chat state must not survive new content arriving.
func TestViewIdenticalAfterIncomingMessage(t *testing.T) {
	warm := newCacheEquivModel()
	_ = warm.View()
	if z := warm.zone.Get(zoneMessage(145)); z != nil {
		next, _ := warm.Update(tea.MouseMotionMsg(tea.Mouse{X: z.StartX, Y: z.StartY}))
		warm = next.(Model)
		_ = warm.View()
	}

	appendMsg := func(m *Model) {
		msgs := append(m.accounts[0].Messages[0], Message{
			ID: "new", Author: "carol", Content: "a brand new incoming message",
			SentAt: time.Date(2025, 3, 4, 12, 30, 0, 0, time.UTC),
		})
		m.accounts[0].Messages[0] = msgs
		// Before refreshViewport, not after: the selection bar is baked
		// into the rendered rows, so the selection has to be current when
		// they are rendered.
		m.selectedMsg = len(msgs) - 1
		m.refreshViewport()
		m.viewport.GotoBottom()
	}

	// Hover is cleared before the refresh, not after: the hovered row's
	// tint is baked into the rendered rows, so leaving it set here would
	// have the two models legitimately rendering different things.
	warm.hover.id = ""
	appendMsg(&warm)
	warmView := warm.View().Content

	cold := newCacheEquivModel()
	cold.hover.id = ""
	appendMsg(&cold)
	coldView := cold.View().Content

	if warmView != coldView {
		t.Error("render after an incoming message differs between a warmed and a cold model — a cache outlived the content change")
	}
}
