package ui

import (
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
)

// TestSortChatsPreservesPagingState guards what sortChatsByActivity used to
// drop. A chat the user has paged up in is marked HistoryNewer (its loaded
// window is not the live tail); any incoming message anywhere re-sorts the
// list, and the sort rebuilt the per-chat maps without carrying that flag -
// so the next message got spliced onto the end of a mid-history window,
// which is precisely what the flag exists to prevent.
func TestSortChatsPreservesPagingState(t *testing.T) {
	m := newChatModel()
	m.accounts[0].Chats = []list.Item{
		Chat{Name: "a", Address: "a@x", LastActivity: time.Now().Add(-time.Hour)},
		Chat{Name: "b", Address: "b@x", LastActivity: time.Now().Add(-2 * time.Hour)},
	}
	m.accounts[0].Messages = map[int][]Message{
		0: {{ID: "a1"}},
		1: {{ID: "b1"}},
	}
	m.accounts[0].HistoryMore = map[int]bool{0: true}
	m.accounts[0].HistoryNewer = map[int]bool{0: true}
	m.loadingHistoryWindow = map[int]bool{0: true}
	m.pendingWindowAnchor = map[int]string{0: "a1"}
	m.chats.SetItems(m.accounts[0].Chats)

	// b@x gets a message, so it sorts above a@x and every index shifts.
	next := updated(t, m, IncomingMessageMsg{
		AccountIdx: 0,
		From:       "b@x",
		Message:    Message{ID: "b2", Content: "hi"},
	})

	aIdx := next.chatIndexByAddress(0, "a@x")
	if aIdx != 1 {
		t.Fatalf("a@x is at index %d, want 1 after b@x sorted above it", aIdx)
	}
	if !next.accounts[0].HistoryNewer[aIdx] {
		t.Error("HistoryNewer was lost by the sort; a live message would be spliced into a mid-history window")
	}
	if !next.accounts[0].HistoryMore[aIdx] {
		t.Error("HistoryMore was not re-keyed onto a@x's new index")
	}
	if !next.loadingHistoryWindow[aIdx] {
		t.Error("loadingHistoryWindow was not re-keyed; a@x's in-flight fetch is now attributed to another chat")
	}
	if got := next.pendingWindowAnchor[aIdx]; got != "a1" {
		t.Errorf("pendingWindowAnchor[%d] = %q, want %q", aIdx, got, "a1")
	}
	if got := next.accounts[0].Messages[aIdx]; len(got) != 1 || got[0].ID != "a1" {
		t.Errorf("messages at a@x's new index = %v, want a1", got)
	}
}
