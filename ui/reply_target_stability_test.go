package ui

import (
	"strings"
	"testing"
	"time"
)

// TestReplyTargetSurvivesHistoryWindowReload guards the reply header against
// the message slice being rebuilt underneath it. A HistoryWindowMsg replaces
// Messages[chatIdx] wholesale and mergeLiveTail carries any just-arrived live
// message forward into it, so a reply's position - and its target's - shift
// by however much the new window differs from the old one. Back when a reply
// remembered its target as an index into that slice, the quote silently
// re-pointed at whatever message happened to land on the old index.
func TestReplyTargetSurvivesHistoryWindowReload(t *testing.T) {
	m := newLimitTestModel(t, 3, 0) // a, b, c
	base := m.accounts[0].Messages[0][2].SentAt

	updated, _, handled := m.handleEventMsg(IncomingMessageMsg{
		AccountIdx: 0, From: "bob@example.com",
		ReplyToID: "c",
		Message:   Message{ID: "reply", Author: "bob", Content: "agreed", SentAt: base.Add(time.Second)},
	})
	if !handled {
		t.Fatal("IncomingMessageMsg was not handled")
	}
	m = &updated

	if got := m.accounts[0].Messages[0]; messageIndexByID(got, got[3].ReplyToID) != 2 {
		t.Fatalf("reply target should be %q before the reload, got %q", "c", got[3].ReplyToID)
	}

	// A narrower window of the same tail: every message shifts down by one.
	older := m.accounts[0].Messages[0][1:3] // b, c
	updated2, _, handled := m.handleEventMsg(HistoryWindowMsg{
		AccountIdx: 0, From: "bob@example.com",
		Messages: older,
		HasNewer: false,
	})
	if !handled {
		t.Fatal("HistoryWindowMsg was not handled")
	}

	got := updated2.accounts[0].Messages[0]
	if len(got) != 3 || got[2].ID != "reply" {
		t.Fatalf("unexpected messages after reload: %+v", got)
	}
	if idx := messageIndexByID(got, got[2].ReplyToID); idx != 1 {
		t.Fatalf("reply target resolved to %d, want 1 (%q)", idx, "c")
	}
}

// TestReplyHeaderFallsBackToSnapshotOutsideWindow covers a reply whose target
// isn't in the loaded window at all - the normal case in a long chat, where
// only the most recent historyPageSize messages are ever resident and a reply
// to anything older can't resolve its target by ID. Before the
// QuotedAuthor/QuotedPreview snapshot existed, messageIndexByID simply missed
// and the message rendered with no reply indication whatsoever, making a real
// XEP-0461 reply indistinguishable from an ordinary message.
func TestReplyHeaderFallsBackToSnapshotOutsideWindow(t *testing.T) {
	m := newLimitTestModel(t, 1, 0)
	msgs := []Message{{
		ID:            "reply",
		Author:        "bob",
		Content:       "agreed",
		SentAt:        time.Now(),
		ReplyToID:     "long-since-paged-out",
		QuotedAuthor:  "me",
		QuotedPreview: "the quoted one",
	}}

	if idx := messageIndexByID(msgs, msgs[0].ReplyToID); idx >= 0 {
		t.Fatalf("target should not be resolvable in this window, got idx %d", idx)
	}

	out := m.renderMessage(msgs[0], 0, 80, msgs, 8)
	if !strings.Contains(out, "the quoted one") {
		t.Fatalf("rendered reply = %q, want it to quote the snapshot preview", out)
	}
	// The body must not get folded onto the header line when a reply quote
	// occupies that space - the quote and the body would run together.
	if !strings.Contains(out, "agreed") {
		t.Fatalf("rendered reply = %q, want it to still show the body", out)
	}
}

// TestReplyHeaderPrefersLoadedTargetOverSnapshot pins the precedence: with the
// real target present, the live message is what gets quoted (and stays
// clickable), not the snapshot frozen at persist time - which can be stale if
// the target was since edited or retracted.
func TestReplyHeaderPrefersLoadedTargetOverSnapshot(t *testing.T) {
	m := newLimitTestModel(t, 1, 0)
	target := Message{ID: "target", Author: "bob", Content: "live content", SentAt: time.Now()}
	reply := Message{
		ID:            "reply",
		Author:        "bob",
		Content:       "agreed",
		SentAt:        time.Now().Add(time.Second),
		ReplyToID:     "target",
		QuotedAuthor:  "bob",
		QuotedPreview: "stale snapshot",
	}
	msgs := []Message{target, reply}

	out := m.renderMessage(reply, 1, 80, msgs, 8)
	if !strings.Contains(out, "live content") {
		t.Fatalf("rendered reply = %q, want the loaded target's content", out)
	}
	if strings.Contains(out, "stale snapshot") {
		t.Fatalf("rendered reply = %q, want the snapshot ignored when the target is loaded", out)
	}
}

// TestReplyHeaderQuotesTargetAfterTrim covers the same staleness at the other
// end: trimming the front of a chat past maxMessagesPerChat renumbers every
// message, and the rendered header must still quote the message actually
// replied to.
func TestReplyHeaderQuotesTargetAfterTrim(t *testing.T) {
	m := newLimitTestModel(t, 3, 3) // a, b, c - already at the limit
	m.accounts[0].Messages[0][1].Content = "the quoted one"
	base := m.accounts[0].Messages[0][2].SentAt

	updated, _, handled := m.handleEventMsg(IncomingMessageMsg{
		AccountIdx: 0, From: "bob@example.com",
		ReplyToID: "b",
		Message:   Message{ID: "reply", Author: "bob", Content: "agreed", SentAt: base.Add(time.Second)},
	})
	if !handled {
		t.Fatal("IncomingMessageMsg was not handled")
	}

	msgs := updated.accounts[0].Messages[0]
	if len(msgs) != 3 || msgs[0].ID != "b" { // "a" trimmed off the front
		t.Fatalf("unexpected messages after trim: %+v", msgs)
	}
	idx := messageIndexByID(msgs, msgs[2].ReplyToID)
	if idx != 0 {
		t.Fatalf("reply target resolved to %d, want 0 (%q)", idx, "b")
	}
	if header := updated.replyHeaderFragment(idx, msgs, 80); !strings.Contains(header, "the quoted one") {
		t.Fatalf("reply header = %q, want it to quote the target message", header)
	}
}
