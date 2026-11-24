package ui

import (
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
)

func messageIDs(msgs []Message) string {
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	return strings.Join(ids, ",")
}

// orderModel is a chat with a, b, c loaded, a minute apart, c selected,
// previewing c.
func orderModel(t *testing.T) (*Model, time.Time) {
	t.Helper()
	m := newLimitTestModel(t, 3, 0)
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	for i := range m.accounts[0].Messages[0] {
		m.accounts[0].Messages[0][i].SentAt = base.Add(time.Duration(i) * time.Minute)
	}
	chat := m.accounts[0].Chats[0].(Chat)
	chat.LastMessage, chat.LastActivity = "c", base.Add(2*time.Minute)
	m.accounts[0].Chats = []list.Item{chat}
	m.chats.SetItems(m.accounts[0].Chats)
	m.selectedMsg = 2
	return m, base
}

func lastMessage(m Model) string { return m.accounts[0].Chats[0].(Chat).LastMessage }

// TestDelayedMessagePlacedBySentTime checks a message that was delayed on
// its way is shown where it was sent, not appended as the newest.
func TestDelayedMessagePlacedBySentTime(t *testing.T) {
	m, base := orderModel(t)
	next, _, _ := m.handleEventMsg(IncomingMessageMsg{AccountIdx: 0, From: "bob@example.com",
		Message: Message{ID: "late", Content: "late", SentAt: base.Add(30 * time.Second)}})

	if got := messageIDs(next.accounts[0].Messages[0]); got != "a,late,b,c" {
		t.Errorf("order = %s", got)
	}
	if next.selectedMsg != 3 {
		t.Errorf("selectedMsg = %d, want 3 (still c)", next.selectedMsg)
	}
	if got := lastMessage(next); got != "c" {
		t.Errorf("preview = %q, want c's: the newest is still c", got)
	}
}

// TestMessageOfSameSecondGoesAfter checks a message sent in the same second
// as a loaded one goes after it, as storage orders them.
func TestMessageOfSameSecondGoesAfter(t *testing.T) {
	m, base := orderModel(t)
	next, _, _ := m.handleEventMsg(IncomingMessageMsg{AccountIdx: 0, From: "bob@example.com",
		Message: Message{ID: "x", Content: "x", SentAt: base.Add(time.Minute + 500*time.Millisecond)}})
	if got := messageIDs(next.accounts[0].Messages[0]); got != "a,b,x,c" {
		t.Errorf("order = %s", got)
	}
}

// TestNewestMessageUpdatesPreview checks the newest message still goes last
// and becomes the preview, its own time the chat's activity.
func TestNewestMessageUpdatesPreview(t *testing.T) {
	m, base := orderModel(t)
	sent := base.Add(10 * time.Minute)
	next, _, _ := m.handleEventMsg(IncomingMessageMsg{AccountIdx: 0, From: "bob@example.com",
		Message: Message{ID: "d", Content: "d", SentAt: sent}})
	if got := messageIDs(next.accounts[0].Messages[0]); got != "a,b,c,d" {
		t.Errorf("order = %s", got)
	}
	chat := next.accounts[0].Chats[0].(Chat)
	if chat.LastMessage != "d" || !chat.LastActivity.Equal(sent) {
		t.Errorf("preview %q activity %v, want d at %v", chat.LastMessage, chat.LastActivity, sent)
	}
}

// TestMessageOutsideWindowLeftForPaging checks a message belonging beyond
// the loaded window isn't put at its edge, where it would become the anchor
// paging fetches past: it shows once that part of history loads.
func TestMessageOutsideWindowLeftForPaging(t *testing.T) {
	m, base := orderModel(t)
	m.accounts[0].HistoryMore = map[int]bool{0: true}
	next, _, _ := m.handleEventMsg(IncomingMessageMsg{AccountIdx: 0, From: "bob@example.com",
		Message: Message{ID: "old", Content: "old", SentAt: base.Add(-time.Hour)}})
	if got := messageIDs(next.accounts[0].Messages[0]); got != "a,b,c" {
		t.Errorf("older than the window with older unloaded: order = %s", got)
	}

	m, base = orderModel(t)
	m.accounts[0].HistoryNewer = map[int]bool{0: true}
	next, _, _ = m.handleEventMsg(IncomingMessageMsg{AccountIdx: 0, From: "bob@example.com",
		Message: Message{ID: "new", Content: "new", SentAt: base.Add(time.Hour)}})
	if got := messageIDs(next.accounts[0].Messages[0]); got != "a,b,c" {
		t.Errorf("newer than a mid-history window: order = %s", got)
	}
	next, _, _ = next.handleEventMsg(IncomingMessageMsg{AccountIdx: 0, From: "bob@example.com",
		Message: Message{ID: "mid", Content: "mid", SentAt: base.Add(30 * time.Second)}})
	if got := messageIDs(next.accounts[0].Messages[0]); got != "a,mid,b,c" {
		t.Errorf("inside a mid-history window: order = %s", got)
	}
}

// TestSyncedMessagesPlacedBySentTime checks backfilled messages are placed
// by time too, e.g. ones a re-sync recovered from hours ago.
func TestSyncedMessagesPlacedBySentTime(t *testing.T) {
	m, base := orderModel(t)
	next, _, _ := m.handleEventMsg(HistorySyncedMsg{AccountIdx: 0, From: "bob@example.com", Messages: []Message{
		{ID: "s1", Content: "s1", SentAt: base.Add(10 * time.Second)},
		{ID: "s2", Content: "s2", SentAt: base.Add(70 * time.Second)},
	}})
	if got := messageIDs(next.accounts[0].Messages[0]); got != "a,s1,b,s2,c" {
		t.Errorf("order = %s", got)
	}
	if got := lastMessage(next); got != "c" {
		t.Errorf("preview = %q, want c's", got)
	}
}

// TestOwnMessageTakesStoredTime checks our own message, shown at our clock's
// time when sent, takes the time the daemon stored it under when that comes
// back, and moves where that places it.
func TestOwnMessageTakesStoredTime(t *testing.T) {
	m, base := orderModel(t)
	shown := base.Add(3 * time.Minute)
	m.accounts[0].Messages[0] = append(m.accounts[0].Messages[0], Message{ID: "x", Content: "x", IsMe: true, SentAt: shown})
	m.selectedMsg = 2 // c

	stored := base.Add(3*time.Minute + 400*time.Millisecond)
	next, _, _ := m.handleEventMsg(IncomingMessageMsg{AccountIdx: 0, From: "bob@example.com",
		Message: Message{ID: "x", Content: "x", IsMe: true, SentAt: stored}})
	msgs := next.accounts[0].Messages[0]
	if got := messageIDs(msgs); got != "a,b,c,x" {
		t.Errorf("same place: order = %s", got)
	}
	if !msgs[3].SentAt.Equal(stored) {
		t.Errorf("SentAt = %v, want the stored %v", msgs[3].SentAt, stored)
	}

	stored = base.Add(30 * time.Second)
	next, _, _ = next.handleEventMsg(IncomingMessageMsg{AccountIdx: 0, From: "bob@example.com",
		Message: Message{ID: "x", Content: "x", IsMe: true, SentAt: stored}})
	if got := messageIDs(next.accounts[0].Messages[0]); got != "a,x,b,c" {
		t.Errorf("moved: order = %s", got)
	}
	if next.selectedMsg != 3 {
		t.Errorf("selectedMsg = %d, want 3 (still c)", next.selectedMsg)
	}
}

// TestQueuedMessageTakesStoredTime checks a queued send's placeholder, known
// only by its local ID, takes the stored time too.
func TestQueuedMessageTakesStoredTime(t *testing.T) {
	m, base := orderModel(t)
	m.accounts[0].Messages[0] = append(m.accounts[0].Messages[0], Message{LocalID: "l1", Content: "q", IsMe: true, Pending: true, SentAt: base.Add(3 * time.Minute)})
	next, _, _ := m.handleEventMsg(IncomingMessageMsg{AccountIdx: 0, From: "bob@example.com",
		Message: Message{ID: "q", LocalID: "l1", Content: "q", IsMe: true, SentAt: base.Add(90 * time.Second)}})
	msgs := next.accounts[0].Messages[0]
	if len(msgs) != 4 || msgs[2].LocalID != "l1" || !msgs[2].SentAt.Equal(base.Add(90*time.Second)) {
		t.Errorf("placeholder not retimed between b and c: %+v", msgs)
	}
}
