package ui

import (
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	zone "github.com/lrstanley/bubblezone/v2"
)

func TestTrimMessagesFront(t *testing.T) {
	msgs := make([]Message, 5)
	for i := range msgs {
		msgs[i] = Message{ID: string(rune('a' + i))}
	}
	replyToFirst := 0
	msgs[4].ReplyTo = &replyToFirst // points at a message that will be dropped
	replyToLast := 4
	msgs[3].ReplyTo = &replyToLast // survives, index should shift down

	trimmed, dropped := trimMessagesFront(msgs, 3)
	if dropped != 2 {
		t.Fatalf("dropped = %d, want 2", dropped)
	}
	if len(trimmed) != 3 || trimmed[0].ID != "c" || trimmed[2].ID != "e" {
		t.Fatalf("unexpected trimmed slice: %+v", trimmed)
	}
	if trimmed[2].ReplyTo != nil { // was "e", pointed at dropped "a"
		t.Fatalf("ReplyTo to a dropped message should be nil, got %v", *trimmed[2].ReplyTo)
	}
	if trimmed[1].ReplyTo == nil || *trimmed[1].ReplyTo != 2 { // was "d", pointed at "e" (shifted 4-2=2)
		t.Fatalf("ReplyTo should have shifted to 2, got %v", trimmed[1].ReplyTo)
	}

	// no-op when under the limit
	same, dropped := trimMessagesFront(msgs, 10)
	if dropped != 0 || len(same) != 5 {
		t.Fatalf("expected no trimming under the limit, got dropped=%d len=%d", dropped, len(same))
	}

	// disabled
	same, dropped = trimMessagesFront(msgs, 0)
	if dropped != 0 || len(same) != 5 {
		t.Fatalf("limit <= 0 should disable trimming, got dropped=%d len=%d", dropped, len(same))
	}
}

// newLimitTestModel builds a Model with a single chat holding n messages and
// maxMessagesPerChat set to limit.
func newLimitTestModel(t *testing.T, n, limit int) *Model {
	t.Helper()
	styles := newUIStyles(DefaultTheme())

	msgs := make([]Message, n)
	base := time.Now()
	for i := range msgs {
		msgs[i] = Message{ID: string(rune('a' + i)), StoreID: int64(i + 1), Author: "bob", Content: "hi", SentAt: base.Add(time.Duration(i) * time.Second)}
	}

	chat := Chat{Name: "bob", Address: "bob@example.com"}
	zm := zone.New()
	delegate := newChatListDelegate(styles.colors, zm, false, &hoverState{})
	l := list.New([]list.Item{chat}, delegate, 0, 0)
	l.Select(0)

	m := &Model{
		styles:             styles,
		width:              80,
		height:             24,
		sidebarHidden:      true,
		zone:               zm,
		chats:              &l,
		maxMessagesPerChat: limit,
		accounts: []Account{
			{
				Chats:    []list.Item{chat},
				Messages: map[int][]Message{0: msgs},
			},
		},
		selectedView:         viewChat,
		loadingHistoryWindow: make(map[int]bool),
	}
	m.viewport.SetWidth(80)
	m.viewport.SetHeight(24)
	m.refreshViewport()
	return m
}

// TestIncomingMessageTrimsToLimit verifies that a live incoming message past
// the configured cap evicts the oldest loaded message instead of growing the
// in-memory slice unbounded, and marks HistoryMore so that message is known
// to still be reachable (via an older-history fetch) rather than just lost.
func TestIncomingMessageTrimsToLimit(t *testing.T) {
	m := newLimitTestModel(t, 3, 3)

	updated, _, handled := m.handleEventMsg(IncomingMessageMsg{
		AccountIdx: 0,
		From:       "bob@example.com",
		Message:    Message{ID: "new", Author: "bob", Content: "hi", SentAt: time.Now()},
	})
	if !handled {
		t.Fatal("IncomingMessageMsg was not handled")
	}

	got := updated.accounts[0].Messages[0]
	if len(got) != 3 {
		t.Fatalf("len(Messages) = %d, want 3 (capped)", len(got))
	}
	if got[0].ID != "b" || got[2].ID != "new" {
		t.Fatalf("expected oldest message dropped, got IDs %q, %q, %q", got[0].ID, got[1].ID, got[2].ID)
	}
	if updated.selectedMsg != 2 {
		t.Fatalf("selectedMsg = %d, want 2 (last)", updated.selectedMsg)
	}
	if !updated.accounts[0].HistoryMore[0] {
		t.Fatal("HistoryMore should be true — trimming dropped a message that's still in storage")
	}
}

// TestIncomingMessageSkipsAppendWhenViewingMidHistory guards against a live
// message arriving while the chat's loaded window isn't the live tail
// (HistoryNewer set — paged up, or landed on a search result): it must not
// be spliced onto the end of that unrelated window, just counted unread.
func TestIncomingMessageSkipsAppendWhenViewingMidHistory(t *testing.T) {
	m := newLimitTestModel(t, 3, 3)
	m.accounts[0].HistoryNewer = map[int]bool{0: true}
	before := append([]Message{}, m.accounts[0].Messages[0]...)

	updated, _, handled := m.handleEventMsg(IncomingMessageMsg{
		AccountIdx: 0,
		From:       "bob@example.com",
		Message:    Message{ID: "live", Content: "just arrived"},
	})
	if !handled {
		t.Fatal("IncomingMessageMsg was not handled")
	}

	after := updated.accounts[0].Messages[0]
	if len(after) != len(before) {
		t.Fatalf("window changed size while viewing mid-history: %d -> %d", len(before), len(after))
	}
	for i := range after {
		if after[i].ID != before[i].ID {
			t.Fatalf("window content changed at index %d: %q -> %q", i, before[i].ID, after[i].ID)
		}
	}
	chat := updated.accounts[0].Chats[0].(Chat)
	if chat.Unread != 1 {
		t.Fatalf("chat.Unread = %d, want 1", chat.Unread)
	}
}

// TestHistoryWindowMsgReplacesMessagesAndRestoresSelection guards
// HistoryWindowMsg's core contract: the response fully replaces whatever was
// loaded before (no merge with the old window), and the selection follows
// whichever message was anchored on (Model.pendingWindowAnchor) by ID, not
// by index — a full replace makes any old index meaningless.
func TestHistoryWindowMsgReplacesMessagesAndRestoresSelection(t *testing.T) {
	m := newLimitTestModel(t, 5, 5) // a, b, c, d, e
	m.pendingWindowAnchor = map[int]string{0: "c"}
	m.loadingHistoryWindow[0] = true

	newWindow := []Message{{ID: "x"}, {ID: "c"}, {ID: "y"}}
	updated, _, handled := m.handleEventMsg(HistoryWindowMsg{
		AccountIdx: 0, From: "bob@example.com",
		Messages: newWindow, HasOlder: true, HasNewer: true,
	})
	if !handled {
		t.Fatal("HistoryWindowMsg was not handled")
	}

	got := updated.accounts[0].Messages[0]
	if len(got) != 3 || got[0].ID != "x" || got[1].ID != "c" || got[2].ID != "y" {
		t.Fatalf("Messages = %+v, want the response's window verbatim", got)
	}
	if updated.selectedMsg != 1 {
		t.Fatalf("selectedMsg = %d, want 1 (the anchor message %q)", updated.selectedMsg, "c")
	}
	if !updated.accounts[0].HistoryMore[0] || !updated.accounts[0].HistoryNewer[0] {
		t.Fatal("HistoryMore/HistoryNewer should match the response's HasOlder/HasNewer")
	}
	if updated.loadingHistoryWindow[0] {
		t.Fatal("loadingHistoryWindow should be cleared once the response arrives")
	}
	if _, stillPending := updated.pendingWindowAnchor[0]; stillPending {
		t.Fatal("pendingWindowAnchor should be cleared once consumed")
	}
}

// TestHistoryWindowMsgFallsBackToLastMessageWhenAnchorMissing guards the
// fallback path: if the anchor message somehow isn't in the returned window
// (shouldn't normally happen — see loadHistoryWindow), the selection lands
// on the last message rather than out of range or left stale.
func TestHistoryWindowMsgFallsBackToLastMessageWhenAnchorMissing(t *testing.T) {
	m := newLimitTestModel(t, 3, 3)
	m.pendingWindowAnchor = map[int]string{0: "does-not-exist"}

	future := time.Now().Add(time.Hour)
	updated, _, handled := m.handleEventMsg(HistoryWindowMsg{
		AccountIdx: 0, From: "bob@example.com",
		Messages: []Message{{ID: "x", SentAt: future}, {ID: "y", SentAt: future.Add(time.Second)}},
	})
	if !handled {
		t.Fatal("HistoryWindowMsg was not handled")
	}
	if updated.selectedMsg != 1 {
		t.Fatalf("selectedMsg = %d, want 1 (fallback to last message)", updated.selectedMsg)
	}
}
