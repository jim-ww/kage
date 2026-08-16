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

// newPagingOverflowTestModel builds a model whose single chat's loaded
// window is exactly what OlderHistoryMsg's own overflow retention produces
// when repeated older-history fetches push a chat's window past
// maxMessagesPerChat — as opposed to newPinnedTestModel's search-jump
// scenario. Critically, PinnedHistoryComplete is left unset: full is only
// what's been paged in from storage so far, not the chat's entire history,
// and hasMore reflects that there's genuinely more still sitting in storage
// beyond it.
func newPagingOverflowTestModel(t *testing.T, total, limit int, hasMore bool) Model {
	t.Helper()
	full := make([]Message, total)
	for i := range full {
		full[i] = Message{ID: string(rune('a' + i%26))}
	}
	m := newTestModelWithMessages(nil, &fakeHistorySearcher{})
	m.maxMessagesPerChat = limit
	m.accounts[0].Messages[0] = full[total-limit:]
	m.accounts[0].PinnedHistory = map[int][]Message{0: full}
	m.accounts[0].PinnedWindow = map[int][2]int{0: {total - limit, total}}
	m.accounts[0].HistoryMore = map[int]bool{0: hasMore}
	return m
}

// TestGrowPinnedWindowPreservesHistoryMoreForPagingOverflow guards the
// pagination "stuck" bug: reaching PinnedHistory's near edge only means "no
// older messages" when PinnedHistory holds the chat's entire history (a
// search jump). For paging-overflow retention, reaching that edge just means
// nothing more is currently in memory — storage may still hold more (as it
// does here, hasMore=true) — so HistoryMore must survive the grow instead of
// being cleared, or fast repeated older-paging (ctrl+u) would freeze there
// forever instead of firing a real fetch for the rest.
func TestGrowPinnedWindowPreservesHistoryMoreForPagingOverflow(t *testing.T) {
	m := newPagingOverflowTestModel(t, 250, 100, true)
	chatIdx := 0

	for m.growPinnedWindow(0, chatIdx, true) {
	}
	win := m.accounts[0].PinnedWindow[chatIdx]
	if win[0] != 0 {
		t.Fatalf("window start = %d, want 0 (exhausted in-memory older content)", win[0])
	}
	if !m.accounts[0].HistoryMore[chatIdx] {
		t.Fatal("HistoryMore was cleared even though storage still has more beyond PinnedHistory — this is what got fast ctrl+u paging stuck")
	}
}

// TestUnstickPinnedWindowPreservesHistoryMoreWhenOverflowFitsOneWindow
// guards the "jump to latest" analogue of
// TestGrowPinnedWindowPreservesHistoryMoreForPagingOverflow: if a chat has
// only been paged into a small amount of paging-overflow retention (small
// enough that it all fits in one maxMessagesPerChat window, dropped == 0)
// and the user hits "jump to latest" at that point, unstickPinnedWindow must
// not conclude "no more history" just because nothing is left over in
// memory — storage may still hold plenty more beyond it (hasMore=true
// here). Getting this wrong froze ctrl+u paging after a jump-to-latest even
// though plain ctrl+u/ctrl+d paging (growPinnedWindow) worked fine.
func TestUnstickPinnedWindowPreservesHistoryMoreWhenOverflowFitsOneWindow(t *testing.T) {
	chatIdx := 0
	full := make([]Message, 60) // fits comfortably under the 100 cap below
	for i := range full {
		full[i] = Message{ID: string(rune('a' + i%26))}
	}
	m := newTestModelWithMessages(nil, &fakeHistorySearcher{})
	m.maxMessagesPerChat = 100
	m.accounts[0].Messages[chatIdx] = full
	m.accounts[0].PinnedHistory = map[int][]Message{chatIdx: full}
	m.accounts[0].PinnedWindow = map[int][2]int{chatIdx: {0, len(full)}}
	// Not PinnedHistoryComplete: this mimics OlderHistoryMsg's own overflow
	// retention (paging in progress), not a search jump — hasMore=true
	// reflects that storage genuinely has more beyond what's been paged in.
	m.accounts[0].HistoryMore = map[int]bool{chatIdx: true}

	if !m.unstickPinnedWindow(0, chatIdx) {
		t.Fatal("unstickPinnedWindow returned false, want true (PinnedHistory was set)")
	}
	if !m.accounts[0].HistoryMore[chatIdx] {
		t.Fatal("HistoryMore was cleared even though storage still has more beyond the retained overflow — this is what got ctrl+u paging stuck after jump-to-latest")
	}
	if _, ok := m.accounts[0].PinnedHistory[chatIdx]; ok {
		t.Fatal("PinnedHistory should be dropped after unsticking")
	}
}

// TestGrowPinnedWindowOlder guards paging "up" (older) through a pinned
// window: it reveals more of the retained PinnedHistory while keeping the
// window capped at maxMessagesPerChat throughout (never growing
// unboundedly — see growPinnedWindow's doc comment for why that mattered),
// and drops PinnedHistory once both edges have been reached.
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
	if win[0] != 400 || win[1] != 500 {
		t.Fatalf("window after growing older = %v, want [400 500] (stepped by limit/2, still capped at limit)", win)
	}
	if len(m.accounts[0].Messages[chatIdx]) != 100 {
		t.Fatalf("window size = %d, want capped at 100", len(m.accounts[0].Messages[chatIdx]))
	}

	// Grow older repeatedly until the start edge is reached.
	for m.growPinnedWindow(0, chatIdx, true) {
	}
	win = m.accounts[0].PinnedWindow[chatIdx]
	if win[0] != 0 || win[1] != 100 {
		t.Fatalf("window after exhausting older = %v, want [0 100]", win)
	}
	if m.accounts[0].HistoryMore[chatIdx] {
		t.Fatal("HistoryMore should be false once the start edge is reached")
	}
	// PinnedHistory is still needed (newer edge not reached yet).
	if _, ok := m.accounts[0].PinnedHistory[chatIdx]; !ok {
		t.Fatal("PinnedHistory dropped before the newer edge was reached")
	}
}

// TestGrowPinnedWindowStaysCappedPagingBothDirections guards against the
// window growing unboundedly as it's paged in both directions: since a
// capped window can only ever have one edge in view at a time once the
// retained history is larger than the cap, PinnedHistory legitimately stays
// around indefinitely in that case — dropping it is unstickPinnedWindow's
// job (jumpToLatestMessage), not something growPinnedWindow tries to
// converge to on its own.
func TestGrowPinnedWindowStaysCappedPagingBothDirections(t *testing.T) {
	m := newPinnedTestModel(t, 250, 100, 125)
	chatIdx := 0

	for m.growPinnedWindow(0, chatIdx, true) {
	}
	for m.growPinnedWindow(0, chatIdx, false) {
	}

	if len(m.accounts[0].Messages[chatIdx]) != 100 {
		t.Fatalf("window size = %d, want capped at 100 even after paging through both directions", len(m.accounts[0].Messages[chatIdx]))
	}
	if _, ok := m.accounts[0].PinnedHistory[chatIdx]; !ok {
		t.Fatal("PinnedHistory should still be retained — the 250-message history is larger than the 100 cap")
	}
}

// TestGrowPinnedWindowDropsPinnedHistoryWhenWholeHistoryFits guards the
// degenerate case where the retained history is no larger than the window
// cap: paging reaches both edges in a single window and PinnedHistory is
// dropped, handing back off to ordinary (non-search) loading semantics.
func TestGrowPinnedWindowDropsPinnedHistoryWhenWholeHistoryFits(t *testing.T) {
	m := newPinnedTestModel(t, 80, 100, 40)
	chatIdx := 0

	// loadSearchResult's own trimMessagesAround already loaded the whole
	// (80-message, under the 100 cap) history and skipped PinnedHistory
	// entirely — nothing to grow.
	if _, ok := m.accounts[0].PinnedHistory[chatIdx]; ok {
		t.Fatal("PinnedHistory set even though the whole history already fit in one window")
	}
	if len(m.accounts[0].Messages[chatIdx]) != 80 {
		t.Fatalf("window size = %d, want the full 80-message history", len(m.accounts[0].Messages[chatIdx]))
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
