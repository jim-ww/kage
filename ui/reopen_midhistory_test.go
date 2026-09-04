package ui

import (
	"testing"

	"charm.land/bubbles/v2/list"
)

// newMidHistoryModel builds a model whose one chat is showing a mid-history
// window (HistoryNewer set — the user paged up far enough to fall off the live
// tail), with a spy loader wired in so a refetch is observable.
func newMidHistoryModel(t *testing.T) (Model, *spyHistoryLoader) {
	t.Helper()
	loader := &spyHistoryLoader{fakeSuccessSender: &fakeSuccessSender{}}
	m := newTestModelWithSender(loader, nil)
	m.historyLoader = loader

	chat := Chat{Name: "bob", Address: "bob@example.test"}
	m.accounts = []Account{{
		Chats:        []list.Item{chat},
		Messages:     map[int][]Message{0: {{ID: "old", StoreID: 1, Author: "bob", Content: "older window"}}},
		HistoryMore:  map[int]bool{0: true},
		HistoryNewer: map[int]bool{0: true},
	}}
	m.currentAccount = 0
	if cmd := m.chats.SetItems(m.accounts[0].Chats); cmd != nil {
		cmd()
	}
	m.chats.Select(0)
	m.selectedView = viewChats
	return m, loader
}

// TestOpenChatMidHistoryRefetchesTail guards the reopen path for a chat left
// mid-history. While HistoryNewer is set, IncomingMessageMsg deliberately does
// not splice arrivals onto the loaded window (they'd render next to unrelated
// older messages) — it only counts them unread, on the premise that the tail
// gets reloaded before the user looks at the chat again. Opening the chat has
// to actually honour that premise: without a refetch it just scrolls to the
// bottom of the stale window, and every message that arrived meanwhile stays
// invisible.
func TestOpenChatMidHistoryRefetchesTail(t *testing.T) {
	m, loader := newMidHistoryModel(t)

	next, _ := m.openCurrentChat()
	m = next.(Model)

	if loader.calls != 1 {
		t.Fatalf("LoadHistoryWindow calls = %d, want 1 — opening a chat left mid-history must refetch the live tail", loader.calls)
	}
	if !m.loadingHistoryWindow[0] {
		t.Fatal("expected the refetch to be marked in flight")
	}
}

// TestOpenChatOnTailDoesNotRefetch is the converse: the common case, where the
// loaded window already is the live tail, must stay a purely local scroll —
// opening a chat should not cost a storage round trip.
func TestOpenChatOnTailDoesNotRefetch(t *testing.T) {
	m, loader := newMidHistoryModel(t)
	m.accounts[0].HistoryNewer[0] = false

	if _, _ = m.openCurrentChat(); loader.calls != 0 {
		t.Fatalf("LoadHistoryWindow calls = %d, want 0 — the tail is already loaded", loader.calls)
	}
}
