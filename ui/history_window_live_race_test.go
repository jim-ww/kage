package ui

import (
	"testing"
	"time"
)

// TestHistoryWindowMsgKeepsLiveMessageArrivedDuringFetch guards the race
// between a live IncomingMessageMsg and an in-flight HistoryLoader fetch
// reaching the true tail (HasNewer: false): the fetch's storage query can
// run before the live message's insert commits, so its response can land
// after the live message was already spliced into Messages[chatIdx] and
// yet not contain it. A blind replace would silently drop that message
// from view until the next window load - see mergeLiveTail.
func TestHistoryWindowMsgKeepsLiveMessageArrivedDuringFetch(t *testing.T) {
	m := newLimitTestModel(t, 3, 0) // limit 0: no trimming, keeps this focused on the merge

	base := m.accounts[0].Messages[0][len(m.accounts[0].Messages[0])-1].SentAt

	updated, _, handled := m.handleEventMsg(IncomingMessageMsg{
		AccountIdx: 0, From: "bob@example.com",
		Message: Message{ID: "live", Content: "just arrived", SentAt: base.Add(time.Second)},
	})
	if !handled {
		t.Fatal("IncomingMessageMsg was not handled")
	}
	m = &updated

	// The fetch's storage snapshot predates "live" - the response doesn't
	// carry it, even though it claims to be the true tail.
	updated2, _, handled := m.handleEventMsg(HistoryWindowMsg{
		AccountIdx: 0, From: "bob@example.com",
		Messages: m.accounts[0].Messages[0][:len(m.accounts[0].Messages[0])-1],
		HasNewer: false,
	})
	if !handled {
		t.Fatal("HistoryWindowMsg was not handled")
	}

	got := updated2.accounts[0].Messages[0]
	if len(got) == 0 || got[len(got)-1].ID != "live" {
		t.Fatalf("live message dropped by stale HistoryWindowMsg: %+v", got)
	}
}
