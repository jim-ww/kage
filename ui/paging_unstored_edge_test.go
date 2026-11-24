package ui

import (
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

// anchorLoader records the anchor of every window load asked for.
type anchorLoader struct {
	stubDaemon
	anchors []*HistoryAnchor
}

func (l *anchorLoader) LoadHistoryWindow(_ int, _ string, a *HistoryAnchor) tea.Cmd {
	l.anchors = append(l.anchors, a)
	return func() tea.Msg { return nil }
}

// An unstored message at an edge of the loaded window - our own, sent and
// shown before the daemon's copy came back - used to stop paging that way
// for good: there's no row to anchor on. The nearest stored one is used.
func TestPagingPastUnstoredEdgeMessage(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	msgs := []Message{
		{ID: "mine-old", Content: "sent", IsMe: true, SentAt: t0},
		{ID: "a", StoreID: 10, Content: "a", SentAt: t0.Add(time.Second)},
		{ID: "b", StoreID: 11, Content: "b", SentAt: t0.Add(2 * time.Second)},
		{ID: "mine-new", Content: "sent", IsMe: true, SentAt: t0.Add(3 * time.Second)},
	}
	loader := &anchorLoader{}
	m := newTestModelWithMessages(msgs, loader)
	m.accounts[0].HistoryMore = map[int]bool{0: true}
	m.accounts[0].HistoryNewer = map[int]bool{0: true}

	m.maybeLoadOlderHistory()
	delete(m.loadingHistoryWindow, 0)
	m.maybeLoadNewerHistory()

	if len(loader.anchors) != 2 {
		t.Fatalf("window loads = %d, want one each way", len(loader.anchors))
	}
	if got := loader.anchors[0].StoreID; got != 10 {
		t.Errorf("older load anchored on row %d, want 10 (oldest stored)", got)
	}
	if got := loader.anchors[1].StoreID; got != 11 {
		t.Errorf("newer load anchored on row %d, want 11 (newest stored)", got)
	}
}

// The daemon's copy of a message we sent gives the shown one its row.
func TestOwnMessageAdoptsStoredRow(t *testing.T) {
	m := newTestModelWithMessages([]Message{{ID: "x", Content: "hi", IsMe: true, SentAt: time.Now()}}, stubDaemon{})
	m = updated(t, m, IncomingMessageMsg{AccountIdx: 0, From: "bob@example.test", Message: Message{ID: "x", StoreID: 42, Content: "hi", IsMe: true, SentAt: time.Now()}})
	msgs := m.currentMessages()
	if len(msgs) != 1 || msgs[0].StoreID != 42 {
		t.Fatalf("messages = %+v, want the one message with row 42", msgs)
	}
}

// A load still in flight when the account is switched doesn't leave its
// mark behind: the marks are keyed by chat position, the account re-sorts
// while off screen, and the answer then clears the wrong position - the
// chat left marked never paged again.
func TestHistoryLoadMarkClearedOnAccountSwitch(t *testing.T) {
	loader := &anchorLoader{}
	now := time.Now()
	alice := Chat{Name: "alice", Address: "alice@example.test", LastActivity: now}
	bob := Chat{Name: "bob", Address: "bob@example.test", LastActivity: now.Add(-time.Hour)}
	m := newTestModelWithSender(nil, nil)
	m.daemon = loader
	m.accounts = []Account{
		{Name: "me@example.test", Chats: []list.Item{alice, bob}, Messages: map[int][]Message{
			0: {{ID: "a1", StoreID: 1, Content: "a", SentAt: now}},
			1: {{ID: "b1", StoreID: 2, Content: "b", SentAt: now.Add(-time.Hour)}},
		}, HistoryMore: map[int]bool{0: true, 1: true}},
		{Name: "other@example.test", Chats: []list.Item{Chat{Address: "carol@example.test"}}, Messages: map[int][]Message{}},
	}
	m.chats.SetItems(m.accounts[0].Chats)
	m.chats.Select(1) // bob
	m.selectedView = viewChat

	m.maybeLoadOlderHistory() // bob's, marked at position 1
	m.switchAccount(1)
	// Bob writes: account 0 re-sorts him to the top while it's off screen.
	m = updated(t, m, IncomingMessageMsg{AccountIdx: 0, From: bob.Address, Message: Message{ID: "b2", StoreID: 3, Content: "new", SentAt: now.Add(time.Minute)}})
	if got := m.chatIndexByAddress(0, bob.Address); got != 0 {
		t.Fatalf("test setup: bob at %d after his message, want 0", got)
	}
	m = updated(t, m, HistoryWindowMsg{AccountIdx: 0, From: bob.Address, Messages: m.accounts[0].Messages[0], HasOlder: true})
	m.switchAccount(0)
	m.chats.Select(1) // alice, now at position 1

	m.maybeLoadOlderHistory()
	if len(loader.anchors) != 2 {
		t.Fatalf("window loads = %d, want 2: alice's paging blocked by bob's stale mark", len(loader.anchors))
	}
}
