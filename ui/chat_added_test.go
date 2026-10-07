package ui

import (
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
)

// newChatModel builds a Model with one existing chat, the state every test
// here starts from.
func newChatModel() Model {
	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	m.accounts = []Account{{
		Chats:    []list.Item{Chat{Name: "bob@example.test", Address: "bob@example.test"}},
		Messages: map[int][]Message{},
	}}
	m.currentAccount = 0
	m.chats.SetItems(m.accounts[0].Chats)
	return m
}

func chatAt(t *testing.T, m Model, accountIdx, chatIdx int) Chat {
	t.Helper()
	chat, ok := m.accounts[accountIdx].Chats[chatIdx].(Chat)
	if !ok {
		t.Fatalf("chat %d of account %d is not a Chat", chatIdx, accountIdx)
	}
	return chat
}

// TestChatAddedAppearsWithoutRestart is the regression this message exists
// for: a contact who writes to us for the first time had no row in the chat
// list, so the daemon's IncomingMessageMsg was dropped by the UI and the
// conversation only showed up after a restart.
func TestChatAddedAppearsWithoutRestart(t *testing.T) {
	m := newChatModel()
	before := len(m.accounts[0].Chats)

	sent := time.Now()
	next := updated(t, m, ChatAddedMsg{
		AccountIdx: 0,
		Chat:       Chat{Name: "stranger@x", Address: "stranger@x"},
		Messages:   []Message{{ID: "m1", Content: "hello", SentAt: sent}},
	})

	idx := next.chatIndexByAddress(0, "stranger@x")
	if idx < 0 {
		t.Fatal("ChatAddedMsg did not add the chat to the list")
	}
	if got := len(next.accounts[0].Chats); got != before+1 {
		t.Errorf("chat count = %d, want %d", got, before+1)
	}
	if got := next.accounts[0].Messages[idx]; len(got) != 1 || got[0].ID != "m1" {
		t.Errorf("messages for new chat = %v, want the one message it arrived with", got)
	}
	chat := chatAt(t, next, 0, idx)
	if chat.LastMessage == "" {
		t.Error("new chat has no last-message preview, so it sorts as if it had no activity")
	}
	if !chat.LastActivity.Equal(sent) {
		t.Errorf("LastActivity = %v, want %v", chat.LastActivity, sent)
	}

	// A follow-up message must land in the chat that now exists, rather
	// than being dropped as it was before. The index is re-resolved because
	// a new message re-sorts the list by activity.
	next = updated(t, next, IncomingMessageMsg{
		AccountIdx: 0,
		From:       "stranger@x",
		Message:    Message{ID: "m2", Content: "still here"},
	})
	idx = next.chatIndexByAddress(0, "stranger@x")
	if got := next.accounts[0].Messages[idx]; len(got) != 2 {
		t.Fatalf("after a second message the chat holds %d messages, want 2", len(got))
	}
}

func TestChatAddedIsAnUpsert(t *testing.T) {
	m := newChatModel()
	addr := chatAt(t, m, 0, 0).Address
	before := len(m.accounts[0].Chats)

	next := updated(t, m, ChatAddedMsg{
		AccountIdx: 0,
		Chat:       Chat{Name: "Renamed Elsewhere", Address: addr},
	})

	if got := len(next.accounts[0].Chats); got != before {
		t.Fatalf("chat count = %d, want %d - an existing address must not be duplicated", got, before)
	}
	if got := chatAt(t, next, 0, 0).Name; got != "Renamed Elsewhere" {
		t.Errorf("Name = %q, want the pushed name", got)
	}
}

// A chat the user hid must not come back just because the roster changed;
// only a message from the contact brings it back (IncomingMessageMsg's
// auto-unhide).
func TestChatAddedLeavesHiddenChatHidden(t *testing.T) {
	m := newChatModel()
	m.stashHidden(0, hiddenChat{chat: Chat{Name: "hidden@x", Address: "hidden@x"}})
	before := len(m.accounts[0].Chats)

	next := updated(t, m, ChatAddedMsg{
		AccountIdx: 0,
		Chat:       Chat{Name: "hidden@x", Address: "hidden@x"},
	})

	if got := len(next.accounts[0].Chats); got != before {
		t.Errorf("chat count = %d, want %d - a hidden chat must stay hidden", got, before)
	}
	if next.chatIndexByAddress(0, "hidden@x") >= 0 {
		t.Error("hidden chat was put back in the visible list")
	}
}

func TestChatAddedIgnoresUnknownAccount(t *testing.T) {
	m := newChatModel()
	for _, idx := range []int{-1, len(m.accounts)} {
		next := updated(t, m, ChatAddedMsg{AccountIdx: idx, Chat: Chat{Address: "x@y"}})
		if len(next.accounts) != len(m.accounts) {
			t.Fatalf("AccountIdx %d perturbed the account list", idx)
		}
	}
}

// TestChatRemovedReindexesMessages guards the per-chat maps, which are keyed
// by list position: splicing the slice without re-keying them hands every
// chat after the removed one its neighbour's history.
func TestChatRemovedReindexesMessages(t *testing.T) {
	m := newChatModel()
	m.accounts[0].Chats = []list.Item{
		Chat{Name: "a", Address: "a@x"},
		Chat{Name: "b", Address: "b@x"},
		Chat{Name: "c", Address: "c@x"},
	}
	m.accounts[0].Messages = map[int][]Message{
		0: {{ID: "a1"}},
		1: {{ID: "b1"}},
		2: {{ID: "c1"}},
	}
	m.accounts[0].HistoryMore = map[int]bool{2: true}
	m.accounts[0].HistoryNewer = map[int]bool{2: true}

	next := updated(t, m, ChatRemovedMsg{AccountIdx: 0, Address: "b@x"})

	if next.chatIndexByAddress(0, "b@x") >= 0 {
		t.Fatal("removed chat is still in the list")
	}
	cIdx := next.chatIndexByAddress(0, "c@x")
	if cIdx != 1 {
		t.Fatalf("c@x is at index %d, want 1", cIdx)
	}
	if got := next.accounts[0].Messages[cIdx]; len(got) != 1 || got[0].ID != "c1" {
		t.Errorf("messages at c@x's new index = %v, want c1 - the maps were not re-keyed", got)
	}
	if !next.accounts[0].HistoryMore[cIdx] || !next.accounts[0].HistoryNewer[cIdx] {
		t.Error("HistoryMore/HistoryNewer were not re-keyed onto c@x's new index")
	}
	if aIdx := next.chatIndexByAddress(0, "a@x"); aIdx != 0 {
		t.Errorf("a@x moved to index %d, want 0", aIdx)
	}
}

func TestChatRemovedUnknownAddressIsNoop(t *testing.T) {
	m := newChatModel()
	before := len(m.accounts[0].Chats)
	next := updated(t, m, ChatRemovedMsg{AccountIdx: 0, Address: "nobody@x"})
	if got := len(next.accounts[0].Chats); got != before {
		t.Errorf("chat count = %d, want %d", got, before)
	}
}
