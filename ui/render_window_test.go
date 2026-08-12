package ui

import (
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	zone "github.com/lrstanley/bubblezone/v2"
)

// TestRenderWindow guards renderWindow's centering/clamping math directly —
// see trimMessagesAround for the equivalent at the in-memory-cap layer.
func TestRenderWindow(t *testing.T) {
	// Whole chat fits in one window: no windowing at all.
	if front, end := renderWindow(40, 10, 60); front != 0 || end != 40 {
		t.Fatalf("under-cap: front=%d end=%d, want 0/40", front, end)
	}

	// Centered in the middle of a long chat.
	if front, end := renderWindow(200, 100, 60); front != 70 || end != 130 {
		t.Fatalf("centered: front=%d end=%d, want 70/130", front, end)
	}

	// Clamped at the start.
	if front, end := renderWindow(200, 5, 60); front != 0 || end != 60 {
		t.Fatalf("start-clamped: front=%d end=%d, want 0/60", front, end)
	}

	// Clamped at the end.
	if front, end := renderWindow(200, 195, 60); front != 140 || end != 200 {
		t.Fatalf("end-clamped: front=%d end=%d, want 140/200", front, end)
	}

	// Disabled.
	if front, end := renderWindow(200, 100, 0); front != 0 || end != 200 {
		t.Fatalf("disabled: front=%d end=%d, want 0/200", front, end)
	}
}

// newRenderWindowTestModel builds a Model with n messages in a single chat,
// sized so renderWindowMessages actually kicks in windowing behavior is
// testable (n must be well above renderWindowMessages).
func newRenderWindowTestModel(t *testing.T, n int) *Model {
	t.Helper()
	styles := newUIStyles(DefaultTheme())
	msgs := make([]Message, n)
	base := time.Now()
	for i := range msgs {
		msgs[i] = Message{ID: string(rune('a' + i%26)), Author: "bob", Content: "hello there", SentAt: base.Add(time.Duration(i) * time.Second)}
	}
	chat := Chat{Name: "bob", Address: "bob@example.com"}
	zm := zone.New()
	delegate := newChatListDelegate(styles.colors, zm, false, &hoverState{})
	l := list.New([]list.Item{chat}, delegate, 0, 0)
	l.Select(0)

	m := &Model{
		styles:             styles,
		width:              80,
		height:             24,
		sidebarHidden:      true,
		zone:               zm,
		chats:              &l,
		maxMessagesPerChat: 1000,
		accounts: []Account{{
			Chats:    []list.Item{chat},
			Messages: map[int][]Message{0: msgs},
		}},
		selectedView: viewChat,
	}
	m.viewport.SetWidth(80)
	m.viewport.SetHeight(24)
	return m
}

// TestRefreshViewportWindowsLargeChats guards the actual reported symptom
// (a slow/laggy message cursor): charm.land/bubbles viewport.SetContentLines
// rescans every line it's given (computing longestLineWidth) on every call,
// regardless of how much actually changed — a cost that used to scale with
// every message loaded in the chat (up to maxMessagesPerChat), not just the
// 1-2 whose selection state changed. m.msgOffsets/m.viewportLines must stay
// bounded to renderWindowMessages once a chat exceeds it, independent of
// how many messages are actually loaded.
func TestRefreshViewportWindowsLargeChats(t *testing.T) {
	n := renderWindowMessages * 4
	m := newRenderWindowTestModel(t, n)
	m.selectedMsg = n / 2
	m.refreshViewport()

	if len(m.msgOffsets) != renderWindowMessages {
		t.Fatalf("len(msgOffsets) = %d, want %d (bounded by the render window, not %d loaded messages)",
			len(m.msgOffsets), renderWindowMessages, n)
	}
	wantFront := m.selectedMsg - renderWindowMessages/2
	if m.renderWindowStart != wantFront {
		t.Fatalf("renderWindowStart = %d, want %d (centered on selectedMsg)", m.renderWindowStart, wantFront)
	}
}

// TestRefreshViewportScrollToRecentersWindowNearEdge guards the other half
// of windowing: moving the selection close to the render window's edge must
// still work (and show the message) even though it's no longer inside the
// currently-buffered window — refreshViewportScrollTo has to notice and
// fall back to a full (still window-bounded) rebuild rather than silently
// doing nothing because refreshViewportSelection can't patch a message
// that was never rendered.
func TestRefreshViewportScrollToRecentersWindowNearEdge(t *testing.T) {
	n := renderWindowMessages * 4
	m := newRenderWindowTestModel(t, n)
	m.selectedMsg = n / 2
	m.refreshViewport()
	initialFront := m.renderWindowStart

	// Walk the selection down toward the window's near edge until a
	// recenter is triggered.
	old := m.selectedMsg
	for m.renderWindowStart == initialFront && m.selectedMsg > 0 {
		old = m.selectedMsg
		m.selectedMsg--
		m.refreshViewportScrollTo(old, m.selectedMsg)
	}

	if m.renderWindowStart == initialFront {
		t.Fatal("window never recentered while walking toward its edge")
	}
	rel := m.selectedMsg - m.renderWindowStart
	if rel < 0 || rel >= len(m.msgOffsets) {
		t.Fatalf("selectedMsg %d not inside the recentered window [%d, %d)", m.selectedMsg, m.renderWindowStart, m.renderWindowStart+len(m.msgOffsets))
	}
}
