package ui

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestNilFetchDoesNotStallPagingForever is the regression for "rarely,
// cannot scroll past certain message and load older history".
//
// The in-flight marker is what stops a second concurrent fetch for the same
// chat, and only the returned HistoryWindowMsg clears it. It used to be set
// before the fetch was handed off, so a loader that returned no command -
// which the daemon-side loader does for an account that hasn't finished
// connecting yet - left it stuck true. That chat could then never load older
// history again for the rest of the session, from a condition that lasted a
// moment.
func TestNilFetchDoesNotStallPagingForever(t *testing.T) {
	m, _ := newScrollBoundaryTestModel(t, 30)
	// A loader that hands back no command at all, so no HistoryWindowMsg can
	// ever come back to clear the marker.
	loader := &nilCmdLoader{}
	m.historyLoader = loader

	m.maybeLoadOlderHistory()
	if loader.calls != 1 {
		t.Fatalf("first paging attempt: loader called %d times, want 1", loader.calls)
	}
	if m.loadingHistoryWindow[0] {
		t.Error("a fetch that never started was marked in flight; this chat's paging is now wedged")
	}

	m.maybeLoadOlderHistory()
	if loader.calls != 2 {
		t.Fatalf("second paging attempt: loader called %d times, want 2 - paging is wedged", loader.calls)
	}
}

// nilCmdLoader models the daemon-side loader refusing a fetch outright -
// adapter.LoadHistoryWindow returns a nil tea.Cmd for an account that
// hasn't finished connecting yet.
type nilCmdLoader struct{ calls int }

func (l *nilCmdLoader) LoadHistoryWindow(int, string, *HistoryAnchor) tea.Cmd {
	l.calls++
	return nil
}

// replayLoader hands back a fixed HistoryWindowMsg, so a test can drive the
// response half of a fetch rather than only the request half.
type replayLoader struct {
	*fakeSuccessSender
	reply HistoryWindowMsg
	calls int
}

func (l *replayLoader) LoadHistoryWindow(int, string, *HistoryAnchor) tea.Cmd {
	l.calls++
	reply := l.reply
	return func() tea.Msg { return reply }
}

// TestFailedFetchKeepsLoadedHistory is the other half of the same symptom.
//
// A failed fetch used to come back as a zero-valued HistoryWindowMsg, which
// is indistinguishable from an authoritative "this chat has no messages and
// no more history in either direction" - so a momentary IPC error blanked
// the chat AND declared storage exhausted, and no amount of further
// scrolling recovered it.
func TestFailedFetchKeepsLoadedHistory(t *testing.T) {
	m, _ := newScrollBoundaryTestModel(t, 30)
	before := len(m.accounts[0].Messages[0])

	m.markHistoryWindowLoading(0, "msg-000")
	next := updated(t, *m, HistoryWindowMsg{
		AccountIdx: 0,
		From:       "bob@example.com",
		Err:        errors.New("ipc: connection reset"),
	})

	if got := len(next.accounts[0].Messages[0]); got != before {
		t.Errorf("loaded messages = %d, want %d left untouched - the chat was blanked", got, before)
	}
	if !next.accounts[0].HistoryMore[0] {
		t.Error("HistoryMore was cleared by a failed fetch; older history is now unreachable")
	}
	if next.loadingHistoryWindow[0] {
		t.Error("in-flight marker survived the failure, so the fetch can never be retried")
	}
}

// A successful window is still authoritative about what more exists - which
// is what distinguishes it from the Err case above, where those flags must
// be left alone.
//
// Note it is NOT asserted that an empty window empties the chat:
// mergeLiveTail can establish no cutoff from an empty fresh window, so it
// carries every loaded message forward. That is pre-existing behaviour and
// only reachable if storage lost messages the UI still has, so it is left
// as-is rather than changed on the way past.
func TestSuccessfulWindowIsStillAuthoritative(t *testing.T) {
	m, _ := newScrollBoundaryTestModel(t, 30)

	m.markHistoryWindowLoading(0, "msg-000")
	next := updated(t, *m, HistoryWindowMsg{
		AccountIdx: 0,
		From:       "bob@example.com",
		Messages:   m.accounts[0].Messages[0],
		HasOlder:   false,
	})

	if next.accounts[0].HistoryMore[0] {
		t.Error("HistoryMore should be false after a window reporting no older history")
	}
	if next.loadingHistoryWindow[0] {
		t.Error("in-flight marker survived a successful window")
	}
}

// TestFailedFetchRetriesAfterRecovery closes the loop: once the error has
// been handled, the next scroll to the edge must actually fetch again.
func TestFailedFetchRetriesAfterRecovery(t *testing.T) {
	m, _ := newScrollBoundaryTestModel(t, 30)
	loader := &replayLoader{
		fakeSuccessSender: &fakeSuccessSender{},
		reply:             HistoryWindowMsg{AccountIdx: 0, From: "bob@example.com", Err: errors.New("ipc: connection reset")},
	}
	m.historyLoader = loader

	m.maybeLoadOlderHistory()
	if loader.calls != 1 {
		t.Fatalf("loader called %d times, want 1", loader.calls)
	}
	if !m.loadingHistoryWindow[0] {
		t.Fatal("a real fetch was not marked in flight, so nothing stops a duplicate")
	}

	next := updated(t, *m, loader.reply)
	next.historyLoader = loader
	next.maybeLoadOlderHistory()
	if loader.calls != 2 {
		t.Errorf("loader called %d times after the failure, want 2 - the retry never fired", loader.calls)
	}
}
