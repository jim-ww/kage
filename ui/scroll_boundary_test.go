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

// spyHistoryLoader records every LoadHistoryWindow call, standing in for a
// real network fetch — used to confirm a fetch only fires once scrolling
// genuinely reaches the top of all loaded messages, not merely somewhere
// mid-scroll.
type spyHistoryLoader struct {
	*fakeSuccessSender
	calls int
}

func (s *spyHistoryLoader) LoadHistoryWindow(accountIdx int, to string, anchor *HistoryAnchor) tea.Cmd {
	s.calls++
	return nil
}

// newScrollBoundaryTestModel builds a Model with n loaded messages in a
// single chat, with a spyHistoryLoader wired in and
// accounts[0].HistoryMore[0] set so a maybeLoadHistoryWindow call is
// observable and would actually attempt a fetch if triggered.
func newScrollBoundaryTestModel(t *testing.T, n int) (*Model, *spyHistoryLoader) {
	t.Helper()
	loader := &spyHistoryLoader{fakeSuccessSender: &fakeSuccessSender{}}
	m := newTestModelWithSender(loader, nil)
	m.mouseEnabled = true

	msgs := make([]Message, n)
	base := time.Now()
	for i := range msgs {
		msgs[i] = Message{ID: fmt.Sprintf("msg-%03d", i), StoreID: int64(i + 1), Author: "bob", Content: fmt.Sprintf("content-%03d", i), SentAt: base.Add(time.Duration(i) * time.Second)}
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
	_ = m.View()
}

func wheelDownInViewport(t *testing.T, m *Model) {
	t.Helper()
	z := zonePaneViewportBounds(t, m)
	next, _ := m.Update(tea.MouseWheelMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseWheelDown})
	*m = next.(Model)
	_ = m.View()
}

// TestWheelScrollFetchesOlderHistoryOnlyAtTrueTop guards against firing a
// spurious network fetch before scrolling has actually reached the top of
// all loaded messages.
func TestWheelScrollFetchesOlderHistoryOnlyAtTrueTop(t *testing.T) {
	const n = 80
	m, loader := newScrollBoundaryTestModel(t, n)

	for i := 0; i < 5 && loader.calls == 0; i++ {
		wheelUpInViewport(t, m)
	}
	if loader.calls != 0 {
		t.Fatalf("LoadHistoryWindow called %d times after only a few wheel notches, well before the top", loader.calls)
	}

	for i := 0; i < 200 && loader.calls == 0; i++ {
		wheelUpInViewport(t, m)
	}
	if loader.calls == 0 {
		t.Fatal("LoadHistoryWindow was never called after scrolling all the way to the true top of loaded history")
	}
	if !m.viewport.AtTop() {
		t.Fatal("expected the viewport to actually be at the top when the fetch fired")
	}
}

// TestWheelScrollUpThenDownReturnsToTail guards the reported "jumping"
// symptom: scrolling up partway and then all the way back down must land
// the viewport back at the true tail — the last loaded message.
func TestWheelScrollUpThenDownReturnsToTail(t *testing.T) {
	const n = 300
	m, _ := newScrollBoundaryTestModel(t, n)

	for i := 0; i < 20; i++ {
		wheelUpInViewport(t, m)
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
}

// TestChaoticWheelScrollNeverGetsStuck guards against the reported
// symptom directly — scrolling rapidly and chaotically in both directions
// leaving the chat unable to scroll further in either one. A pseudo-random
// up/down sequence must always end with the viewport able to reach both
// the true top and the true bottom of loaded history.
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

	for i := 0; i < 400 && !m.viewport.AtTop(); i++ {
		wheelUpInViewport(t, m)
	}
	if !m.viewport.AtTop() {
		t.Fatal("could not scroll all the way to the true top after the chaotic sequence — scrolling got stuck")
	}

	for i := 0; i < 400 && !m.viewport.AtBottom(); i++ {
		wheelDownInViewport(t, m)
	}
	if !m.viewport.AtBottom() {
		t.Fatal("could not scroll all the way to the true bottom after the chaotic sequence — scrolling got stuck")
	}
}
