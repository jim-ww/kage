package ui

import "testing"

// newPinnedTestModel builds a model whose single chat's loaded window is
// exactly what loadSearchResult would produce for a search-result jump into
// the middle of a large history: a maxMessagesPerChat-sized window, with
// the full history retained as PinnedHistory.
func newPinnedTestModel(t *testing.T, total, limit, target int) Model {
	t.Helper()
	full := make([]Message, total)
	for i := range full {
		full[i] = Message{ID: string(rune('a' + i%26))}
	}
	m := newTestModelWithMessages(nil, &fakeHistorySearcher{})
	m.maxMessagesPerChat = limit
	m.searchResults = &searchResultsState{
		accountIdx: 0, chatAddress: "bob@example.test",
		messages: full, matches: []int{target},
	}
	m.loadSearchResult(target)
	return m
}

// TestGrowPinnedWindowOlder guards paging "up" (older) through a pinned
// window: it reveals more of the retained PinnedHistory, keeps the window
// only extends the window (never re-trims what's already loaded, matching
// how normal older-history scrolling already works in this app — see
// growPinnedWindow's doc comment), and drops PinnedHistory once both edges
// have been reached.
func TestGrowPinnedWindowOlder(t *testing.T) {
	m := newPinnedTestModel(t, 1000, 100, 500)
	chatIdx := 0

	win := m.accounts[0].PinnedWindow[chatIdx]
	if win[0] != 450 || win[1] != 550 {
		t.Fatalf("initial window = %v, want [450 550]", win)
	}
	if !m.accounts[0].HistoryMore[chatIdx] {
		t.Fatal("HistoryMore should be true — older history exists beyond the window")
	}

	if !m.growPinnedWindow(0, chatIdx, true) {
		t.Fatal("growPinnedWindow(older) = false, want true (more history above)")
	}
	win = m.accounts[0].PinnedWindow[chatIdx]
	if win[0] != 350 || win[1] != 550 {
		t.Fatalf("window after growing older = %v, want [350 550] (end unchanged, only start extends)", win)
	}
	if len(m.accounts[0].Messages[chatIdx]) != 200 {
		t.Fatalf("window size = %d, want 200 (grew by pinnedWindowStep, nothing dropped)", len(m.accounts[0].Messages[chatIdx]))
	}

	// Grow older repeatedly until the start edge is reached.
	for m.growPinnedWindow(0, chatIdx, true) {
	}
	win = m.accounts[0].PinnedWindow[chatIdx]
	if win[0] != 0 || win[1] != 550 {
		t.Fatalf("window after exhausting older = %v, want [0 550]", win)
	}
	if m.accounts[0].HistoryMore[chatIdx] {
		t.Fatal("HistoryMore should be false once the start edge is reached")
	}
	// PinnedHistory is still needed (newer edge not reached yet).
	if _, ok := m.accounts[0].PinnedHistory[chatIdx]; !ok {
		t.Fatal("PinnedHistory dropped before the newer edge was reached")
	}
}

// TestGrowPinnedWindowNewerDropsPinnedHistoryAtBothEdges guards paging
// "down" (newer): once both edges of PinnedHistory have been reached (by
// growing both directions), PinnedHistory/PinnedWindow are removed entirely
// so ordinary (non-search) loading semantics take back over.
func TestGrowPinnedWindowNewerDropsPinnedHistoryAtBothEdges(t *testing.T) {
	m := newPinnedTestModel(t, 250, 100, 125)
	chatIdx := 0

	for m.growPinnedWindow(0, chatIdx, true) {
	}
	for m.growPinnedWindow(0, chatIdx, false) {
	}

	if _, ok := m.accounts[0].PinnedHistory[chatIdx]; ok {
		t.Fatal("PinnedHistory should be dropped once both edges are reached")
	}
	if _, ok := m.accounts[0].PinnedWindow[chatIdx]; ok {
		t.Fatal("PinnedWindow should be dropped once both edges are reached")
	}
	if m.accounts[0].HistoryMore[chatIdx] {
		t.Fatal("HistoryMore should be false once the start edge is reached")
	}
	// Once an edge is reached, growing the other direction keeps it pinned
	// rather than shrinking back away from it (see growPinnedWindow's
	// comment) — so by the time both edges have been walked, the window
	// has grown to cover the entire (here: small) retained history, not
	// stayed capped at limit.
	if len(m.accounts[0].Messages[chatIdx]) != 250 {
		t.Fatalf("final window size = %d, want the full 250-message history", len(m.accounts[0].Messages[chatIdx]))
	}
}

// TestGrowPinnedWindowKeepsSelectionOnSameMessage guards that selectedMsg
// tracks the same underlying message across a grow, not just the same
// index — growing older shifts every existing index forward by however
// many new (older) messages were prepended. Uses selectedMsg at the
// window's top edge, matching growPinnedWindow's real trigger condition
// (maybeLoadOlderHistory only calls it once the selection has scrolled to
// index 0 of the currently loaded window).
func TestGrowPinnedWindowKeepsSelectionOnSameMessage(t *testing.T) {
	m := newPinnedTestModel(t, 1000, 100, 500)
	m.selectedMsg = 0
	before := m.currentMessages()[m.selectedMsg]

	m.growPinnedWindow(0, 0, true)

	after := m.currentMessages()
	if m.selectedMsg < 0 || m.selectedMsg >= len(after) {
		t.Fatalf("selectedMsg = %d out of range (len=%d)", m.selectedMsg, len(after))
	}
	if after[m.selectedMsg].ID != before.ID {
		t.Fatalf("selection moved to a different message after growing: had %q, now %q", before.ID, after[m.selectedMsg].ID)
	}
}

// TestMaybeLoadOlderHistoryUsesPinnedWindow guards the MsgUp/top-of-window
// trigger path: with a pinned window loaded, reaching the top calls
// growPinnedWindow instead of falling through to a (wrong-cursor)
// HistoryLoader fetch.
func TestMaybeLoadOlderHistoryUsesPinnedWindow(t *testing.T) {
	m := newPinnedTestModel(t, 1000, 100, 500)
	m.selectedView = viewChat
	startWin := m.accounts[0].PinnedWindow[0]

	m.maybeLoadOlderHistory()

	newWin := m.accounts[0].PinnedWindow[0]
	if newWin[0] >= startWin[0] {
		t.Fatalf("window start didn't move older: before=%d after=%d", startWin[0], newWin[0])
	}
}

// TestJumpToLatestMessageUnwindsPinnedWindow guards jumpToLatestMessage
// against reporting a pinned window's tail as "latest" — it must first
// fully unwind PinnedHistory toward the newer edge.
func TestJumpToLatestMessageUnwindsPinnedWindow(t *testing.T) {
	m := newPinnedTestModel(t, 250, 100, 10) // match near the start, far from the true tail

	m.jumpToLatestMessage()

	if _, ok := m.accounts[0].PinnedHistory[0]; ok {
		t.Fatal("PinnedHistory still present after jumpToLatestMessage — it didn't reach the true tail")
	}
	msgs := m.currentMessages()
	if m.selectedMsg != len(msgs)-1 {
		t.Fatalf("selectedMsg = %d, want %d (the last loaded message)", m.selectedMsg, len(msgs)-1)
	}
}

// TestIncomingMessageDoesNotCorruptPinnedWindow guards against a live
// message arriving while the chat's loaded window is a pinned (mid-history)
// one from a search jump: it must not be spliced onto the end of that old
// window (which isn't anchored to the live tail), just counted unread.
func TestIncomingMessageDoesNotCorruptPinnedWindow(t *testing.T) {
	m := newPinnedTestModel(t, 1000, 100, 500)
	m.selectedView = viewChat
	before := m.currentMessages()

	next, _ := m.Update(IncomingMessageMsg{
		AccountIdx: 0, From: "bob@example.test",
		Message: Message{ID: "live-1", Content: "just arrived"},
	})
	m = next.(Model)

	after := m.currentMessages()
	if len(after) != len(before) {
		t.Fatalf("pinned window changed size after incoming message: %d -> %d, want unchanged", len(before), len(after))
	}
	for i := range after {
		if after[i].ID != before[i].ID {
			t.Fatalf("pinned window content changed at index %d: %q -> %q", i, before[i].ID, after[i].ID)
		}
	}
	if _, ok := m.accounts[0].PinnedHistory[0]; !ok {
		t.Fatal("PinnedHistory was dropped by an incoming message — it should be left alone")
	}
	chat := m.accounts[0].Chats[0].(Chat)
	if chat.Unread != 1 {
		t.Fatalf("chat.Unread = %d, want 1", chat.Unread)
	}
}
