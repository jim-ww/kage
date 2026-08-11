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

func TestTrimMessagesAround(t *testing.T) {
	msgs := make([]Message, 20)
	for i := range msgs {
		msgs[i] = Message{ID: string(rune('a' + i))}
	}
	replyToOutside := 0
	msgs[10].ReplyTo = &replyToOutside // points well before the window, should drop
	replyToInside := 9
	msgs[10].ReplyTo = &replyToInside // survives (index 9 is inside the window), should shift

	// target=10, limit=5 -> window centered on 10: [8,13)
	trimmed, newTarget, front := trimMessagesAround(msgs, 10, 5)
	if front != 8 {
		t.Fatalf("front = %d, want 8", front)
	}
	if len(trimmed) != 5 || trimmed[0].ID != "i" || trimmed[4].ID != "m" {
		t.Fatalf("unexpected trimmed slice: %+v", trimmed)
	}
	if newTarget != 2 || trimmed[newTarget].ID != "k" {
		t.Fatalf("newTarget = %d (%q), want 2 (%q)", newTarget, trimmed[newTarget].ID, "k")
	}
	if trimmed[newTarget].ReplyTo == nil || *trimmed[newTarget].ReplyTo != 1 { // "j" (idx 9) shifted to idx 1
		t.Fatalf("ReplyTo should have shifted to 1, got %v", trimmed[newTarget].ReplyTo)
	}

	// target near the start clamps the window to [0, limit) rather than
	// going negative.
	trimmed, newTarget, front = trimMessagesAround(msgs, 1, 5)
	if front != 0 || newTarget != 1 || trimmed[0].ID != "a" {
		t.Fatalf("start-clamped window: newTarget=%d front=%d trimmed[0]=%q, want 1/0/%q", newTarget, front, trimmed[0].ID, "a")
	}

	// target near the end clamps the window to [len-limit, len) rather than
	// running off the end.
	trimmed, newTarget, front = trimMessagesAround(msgs, 19, 5)
	if front != 15 || trimmed[len(trimmed)-1].ID != "t" || trimmed[newTarget].ID != "t" {
		t.Fatalf("end-clamped window: front=%d newTarget=%d trimmed=%+v", front, newTarget, trimmed)
	}

	// no-op when under the limit
	same, newTarget, front := trimMessagesAround(msgs, 10, 30)
	if front != 0 || newTarget != 10 || len(same) != 20 {
		t.Fatalf("expected no trimming under the limit, got front=%d newTarget=%d len=%d", front, newTarget, len(same))
	}

	// disabled
	same, newTarget, front = trimMessagesAround(msgs, 10, 0)
	if front != 0 || newTarget != 10 || len(same) != 20 {
		t.Fatalf("limit <= 0 should disable trimming, got front=%d newTarget=%d len=%d", front, newTarget, len(same))
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
		msgs[i] = Message{ID: string(rune('a' + i)), Author: "bob", Content: "hi", SentAt: base.Add(time.Duration(i) * time.Second)}
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
		chats:              l,
		maxMessagesPerChat: limit,
		accounts: []Account{
			{
				Chats:    []list.Item{chat},
				Messages: map[int][]Message{0: msgs},
			},
		},
		selectedView: viewChat,
	}
	m.viewport.SetWidth(80)
	m.viewport.SetHeight(24)
	m.refreshViewport()
	return m
}

// TestIncomingMessageTrimsToLimit verifies that a live incoming message past
// the configured cap evicts the oldest loaded message instead of growing the
// in-memory slice unbounded.
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
}

// TestOlderHistoryNotTrimmed verifies that prepending an older-history page
// past the cap keeps every message (older and already-viewed) rather than
// dropping the newest ones - the cap only bounds unbounded growth from live
// incoming traffic (trimMessagesFront), never a page the user deliberately
// scrolled up to load, since there's no way to re-fetch a dropped page.
func TestOlderHistoryNotTrimmed(t *testing.T) {
	m := newLimitTestModel(t, 3, 3)
	m.selectedMsg = 0

	older := []Message{{ID: "x"}, {ID: "y"}}
	updated, _, handled := m.handleEventMsg(OlderHistoryMsg{
		AccountIdx: 0,
		From:       "bob@example.com",
		Messages:   older,
		HasMore:    false,
	})
	if !handled {
		t.Fatal("OlderHistoryMsg was not handled")
	}

	got := updated.accounts[0].Messages[0]
	if len(got) != 5 {
		t.Fatalf("len(Messages) = %d, want 5 (uncapped)", len(got))
	}
	wantIDs := []string{"x", "y", "a", "b", "c"}
	for i, want := range wantIDs {
		if got[i].ID != want {
			t.Fatalf("got[%d].ID = %q, want %q (full IDs: %v)", i, got[i].ID, want, got)
		}
	}
}
