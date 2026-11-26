package ui

import (
	"testing"
)

// withUnloaded sets the open chat's counts of unloaded history.
func withUnloaded(m Model, before, after int) Model {
	chat := m.accounts[0].Chats[0].(Chat)
	chat.HistoryBefore, chat.HistoryAfter = before, after
	m.accounts[0].Chats[0] = chat
	return m
}

func TestScrollbarHiddenWhenAllFits(t *testing.T) {
	m := newTestModelWithManyMessages(2)
	if _, _, ok := m.scrollbarThumb(m.viewport.Height()); ok {
		t.Error("thumb shown for a chat that fits on screen")
	}
}

func TestScrollbarShownForUnloadedHistory(t *testing.T) {
	m := withUnloaded(newTestModelWithManyMessages(2), 100, 0)
	h := m.viewport.Height()
	start, end, ok := m.scrollbarThumb(h)
	if !ok {
		t.Fatal("no thumb with older history unloaded")
	}
	if end != h || start == 0 {
		t.Errorf("thumb [%d,%d) of %d, want it at the bottom only", start, end, h)
	}
}

func TestScrollbarFollowsScroll(t *testing.T) {
	m := newTestModelWithManyMessages(50)
	h := m.viewport.Height()
	start, end, _ := m.scrollbarThumb(h)
	if end != h {
		t.Errorf("at the bottom: thumb [%d,%d), want it to end at %d", start, end, h)
	}
	m.viewport.GotoTop()
	if start, end, _ = m.scrollbarThumb(h); start != 0 || end == h {
		t.Errorf("at the top of all history: thumb [%d,%d), want it to start at 0", start, end)
	}

	// With older history unloaded, the top of the loaded part isn't the top.
	m = withUnloaded(m, 500, 0)
	if start, _, _ = m.scrollbarThumb(h); start == 0 {
		t.Error("thumb at the top with older history unloaded")
	}
	// Nor is the bottom the bottom with newer history unloaded.
	m = withUnloaded(m, 0, 500)
	m.viewport.GotoBottom()
	if _, end, _ = m.scrollbarThumb(h); end == h {
		t.Error("thumb at the bottom with newer history unloaded")
	}
}

func TestHistoryWindowSetsUnloadedCounts(t *testing.T) {
	m := newTestModelWithManyMessages(5)
	msgs := m.accounts[0].Messages[0]
	m = updated(t, m, HistoryWindowMsg{From: "bob@example.test", Messages: msgs, HasOlder: true, HasNewer: true, Older: 30, Total: 50})
	chat := m.accounts[0].Chats[0].(Chat)
	if chat.HistoryBefore != 30 || chat.HistoryAfter != 15 {
		t.Errorf("before/after = %d/%d, want 30/15", chat.HistoryBefore, chat.HistoryAfter)
	}
}

func TestTrimCountsDroppedAsUnloaded(t *testing.T) {
	m := newTestModelWithManyMessages(5)
	m.maxMessagesPerChat = 3
	m.appendAndTrim(0, 0, Message{Content: "new"})
	if got := m.accounts[0].Chats[0].(Chat).HistoryBefore; got != 3 {
		t.Errorf("HistoryBefore = %d after trimming 3, want 3", got)
	}
}
