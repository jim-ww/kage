package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	zone "github.com/lrstanley/bubblezone/v2"
)

// spyHistoryLoader records every LoadOlderHistory call, standing in for a
// real network fetch — used to distinguish "genuinely reached the top of
// all loaded messages" from "merely reached the edge of the render window
// (see renderWindowMessages) while plenty more is already loaded above."
type spyHistoryLoader struct {
	*fakeSuccessSender
	calls int
}

func (s *spyHistoryLoader) LoadOlderHistory(accountIdx int, to string) tea.Cmd {
	s.calls++
	return nil
}

// newScrollBoundaryTestModel builds a Model with n loaded messages (well
// above renderWindowMessages) in a single chat, wide/tall enough that a
// mouse wheel notch (3 lines) actually moves the viewport, with a
// spyHistoryLoader wired in and accounts[0].HistoryMore[0] set so a
// maybeLoadOlderHistory call is observable and would actually attempt a
// fetch if triggered.
func newScrollBoundaryTestModel(t *testing.T, n int) (*Model, *spyHistoryLoader) {
	t.Helper()
	loader := &spyHistoryLoader{fakeSuccessSender: &fakeSuccessSender{}}
	m := newTestModelWithSender(loader, nil)
	m.mouseEnabled = true

	msgs := make([]Message, n)
	base := time.Now()
	for i := range msgs {
		msgs[i] = Message{ID: fmt.Sprintf("msg-%03d", i), Author: "bob", Content: fmt.Sprintf("content-%03d", i), SentAt: base.Add(time.Duration(i) * time.Second)}
	}
	chat := Chat{Name: "bob", Address: "bob@example.com"}
	m.accounts = []Account{{
		Chats:       []list.Item{chat},
		Messages:    map[int][]Message{0: msgs},
		HistoryMore: map[int]bool{0: true},
	}}
	m.currentAccount = 0
	m.chats.SetItems([]list.Item{chat})
	m.chats.Select(0)
	m.selectedView = viewChat
	m.selectedMsg = n - 1
	m.width, m.termHeight = 100, 40
	m.updateSizes()
	m.refreshViewport()
	m.viewport.GotoBottom()
	_ = m.View() // populate zone bounds

	return &m, loader
}

// wheelUpInViewport sends one mouse-wheel-up tick inside the viewport pane.
// zonePaneViewportBounds returns zonePaneViewport's current bounds,
// re-rendering and retrying briefly if needed. bubblezone's Scan() (called
// from View()) hands zone updates to a background goroutine over a channel
// and returns immediately — its own doc comment warns "an immediate call
// to Get(id) may not return the correct information" — so a tight
// View()-then-Get() loop with no real I/O in between (unlike an actual
// running program, which always has a gap before the next input) can
// legitimately race ahead of that goroutine. Retrying briefly is the
// library's documented workaround, not a product bug.
func zonePaneViewportBounds(t *testing.T, m *Model) *zone.ZoneInfo {
	t.Helper()
	for i := 0; i < 50; i++ {
		if z := m.zone.Get(zonePaneViewport); z != nil {
			return z
		}
		_ = m.View()
		time.Sleep(time.Millisecond)
	}
	t.Fatal("zonePaneViewport not found after retrying — check test model setup")
	return nil
}

func wheelUpInViewport(t *testing.T, m *Model) {
	t.Helper()
	z := zonePaneViewportBounds(t, m)
	next, _ := m.Update(tea.MouseWheelMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseWheelUp})
	*m = next.(Model)
	_ = m.View() // real usage re-scans zones after every Update; the static bounds from setup go stale otherwise
}

func wheelDownInViewport(t *testing.T, m *Model) {
	t.Helper()
	z := zonePaneViewportBounds(t, m)
	next, _ := m.Update(tea.MouseWheelMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseWheelDown})
	*m = next.(Model)
	_ = m.View()
}

// TestWheelScrollDoesNotFetchOlderHistoryAtRenderWindowEdge guards the
// reported regression: scrolling up used to fire maybeLoadOlderHistory
// (a real network fetch, via HistoryLoader) as soon as the viewport hit
// the top of the currently *rendered* window — msgIndexAtOffset/YOffset==0
// — even when hundreds of already-loaded messages sat above it, just
// outside the render window. That's not "reached the top of history", so
// it must not fire a fetch (and, per the second reported symptom, doing so
// corrupted the scroll position enough to make scrolling back down land on
// unrelated messages).
func TestWheelScrollDoesNotFetchOlderHistoryAtRenderWindowEdge(t *testing.T) {
	const n = 300
	m, loader := newScrollBoundaryTestModel(t, n)

	// Scroll up enough to cross several render-window recenters (window is
	// renderWindowMessages=60 wide; each wheel notch moves ~3 lines) without
	// reaching anywhere near message index 0 of the 300 loaded.
	for i := 0; i < 40; i++ {
		wheelUpInViewport(t, m)
	}

	if loader.calls != 0 {
		t.Fatalf("LoadOlderHistory called %d times while still well within already-loaded history (not the true top)", loader.calls)
	}
	if m.renderWindowStart == 0 {
		t.Fatal("test didn't actually exercise a window recenter — render window still covers the start; increase the scroll amount")
	}
}

// TestWheelScrollFetchesOlderHistoryOnlyAtTrueTop guards the other half:
// a fetch must still fire once the viewport genuinely reaches message
// index 0 of all loaded messages, not just the render window's edge.
func TestWheelScrollFetchesOlderHistoryOnlyAtTrueTop(t *testing.T) {
	const n = 80 // just past renderWindowMessages, so the true top is reachable in a reasonable number of notches
	m, loader := newScrollBoundaryTestModel(t, n)

	for i := 0; i < 200 && loader.calls == 0; i++ {
		wheelUpInViewport(t, m)
	}

	if loader.calls == 0 {
		t.Fatal("LoadOlderHistory was never called after scrolling all the way to the true top of loaded history")
	}
	if m.renderWindowStart != 0 {
		t.Fatalf("renderWindowStart = %d, want 0 once the true top is reached", m.renderWindowStart)
	}
}

// TestWheelScrollUpThenDownReturnsToTail guards the "jumping" symptom
// directly: scrolling up partway (crossing a render-window recenter) and
// then all the way back down must land the viewport back at the true
// tail — the last loaded message — not some unrelated window elsewhere in
// the chat.
func TestWheelScrollUpThenDownReturnsToTail(t *testing.T) {
	const n = 300
	m, _ := newScrollBoundaryTestModel(t, n)

	for i := 0; i < 40; i++ {
		wheelUpInViewport(t, m)
	}
	if m.renderWindowStart == 0 {
		t.Fatal("test didn't actually exercise a window recenter; increase the scroll amount")
	}

	for i := 0; i < 200 && !m.viewport.AtBottom(); i++ {
		wheelDownInViewport(t, m)
	}
	if !m.viewport.AtBottom() {
		t.Fatal("never made it back to the bottom of the viewport")
	}

	content := m.viewport.View()
	wantTail := fmt.Sprintf("content-%03d", n-1)
	if !strings.Contains(content, wantTail) {
		t.Fatalf("after scrolling back to the bottom, viewport doesn't show the true last message %q:\n%s", wantTail, content)
	}
	if m.renderWindowStart+len(m.msgOffsets) != n {
		t.Fatalf("render window end = %d, want %d (back at the true tail)", m.renderWindowStart+len(m.msgOffsets), n)
	}
}

// TestChaoticWheelScrollNeverGetsStuck guards against the reported
// symptom in its original form — scrolling rapidly and chaotically in both
// directions leaving the chat unable to scroll further in either one. A
// pseudo-random up/down sequence must always end with the viewport able to
// reach both the true top and the true bottom of loaded history.
func TestChaoticWheelScrollNeverGetsStuck(t *testing.T) {
	const n = 300
	m, _ := newScrollBoundaryTestModel(t, n)

	seed := uint32(12345)
	next := func() uint32 { seed = seed*1664525 + 1013904223; return seed }
	for i := 0; i < 150; i++ {
		if next()%2 == 0 {
			wheelUpInViewport(t, m)
		} else {
			wheelDownInViewport(t, m)
		}
	}

	// However the chaotic sequence left things, both true edges must still
	// be reachable — a real "stuck" bug leaves at least one of these loops
	// spinning without ever getting there.
	for i := 0; i < 400 && !(m.renderWindowStart == 0 && m.viewport.AtTop()); i++ {
		wheelUpInViewport(t, m)
	}
	if !(m.renderWindowStart == 0 && m.viewport.AtTop()) {
		t.Fatal("could not scroll all the way to the true top after the chaotic sequence — scrolling got stuck")
	}

	for i := 0; i < 400 && !m.viewport.AtBottom(); i++ {
		wheelDownInViewport(t, m)
	}
	if !m.viewport.AtBottom() || m.renderWindowStart+len(m.msgOffsets) != n {
		t.Fatal("could not scroll all the way to the true bottom after the chaotic sequence — scrolling got stuck")
	}
}

// TestViewportPagingDoesNotFetchOlderHistoryAtRenderWindowEdge is
// TestWheelScrollDoesNotFetchOlderHistoryAtRenderWindowEdge's PageUp/
// PageDown counterpart — the same bug existed in update_keys.go's
// isViewportPagingKey handler.
func TestViewportPagingDoesNotFetchOlderHistoryAtRenderWindowEdge(t *testing.T) {
	const n = 300
	m, loader := newScrollBoundaryTestModel(t, n)
	m.keys = DefaultKeyMap

	initialFront := m.renderWindowStart
	for i := 0; i < 3; i++ {
		next, _, _ := m.updateKeyMsg(tea.KeyPressMsg{Code: tea.KeyPgUp})
		*m = next
	}

	if loader.calls != 0 {
		t.Fatalf("LoadOlderHistory called %d times via PageUp while still well within already-loaded history", loader.calls)
	}
	if m.renderWindowStart == initialFront {
		t.Fatal("test didn't actually exercise a window recenter; adjust the number of PageUp presses")
	}
	if m.renderWindowStart == 0 {
		t.Fatal("test overshot all the way to the true top; reduce the number of PageUp presses so it only exercises an intermediate recenter")
	}
}
