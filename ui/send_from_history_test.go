package ui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// collectMsgs runs cmd, flattening batches, and returns every message.
func collectMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, collectMsgs(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// TestSendWhileInOlderWindowLoadsTail: a message sent while an older window
// is shown loads the latest window, where the sent message belongs, instead
// of leaving it appended to the bottom of stale history.
func TestSendWhileInOlderWindowLoadsTail(t *testing.T) {
	m := newTestModelWithManyMessages(30)
	m.accounts[0].HistoryNewer = map[int]bool{0: true}
	sentAt := time.Now()
	loader := &replayLoader{
		fakeSuccessSender: &fakeSuccessSender{},
		reply: HistoryWindowMsg{AccountIdx: 0, From: "bob@example.test", Messages: []Message{
			{ID: "latest", Content: "newest stored", SentAt: sentAt.Add(-time.Minute)},
			{ID: "msg-id", Content: "hello", IsMe: true, SentAt: sentAt},
		}},
	}
	m.daemon = loader
	m.input.SetValue("hello")

	cmd := m.sendCurrentInput()
	if loader.calls != 1 {
		t.Fatalf("LoadHistoryWindow called %d times, want 1", loader.calls)
	}
	next := m
	for _, msg := range collectMsgs(cmd) {
		if w, ok := msg.(HistoryWindowMsg); ok {
			next = updated(t, next, w)
		}
	}
	msgs := next.currentMessages()
	if len(msgs) != 2 || msgs[1].ID != "msg-id" {
		t.Fatalf("messages after send = %+v, want the latest window with the sent message last", msgs)
	}
	if next.accounts[0].HistoryNewer[0] {
		t.Error("HistoryNewer still set after loading the tail")
	}
}

// TestSendAtTailDoesNotReload: at the live tail there's nothing to load.
func TestSendAtTailDoesNotReload(t *testing.T) {
	m := newTestModelWithManyMessages(30)
	loader := &replayLoader{fakeSuccessSender: &fakeSuccessSender{}}
	m.daemon = loader
	m.input.SetValue("hello")
	m.sendCurrentInput()
	if loader.calls != 0 {
		t.Fatalf("LoadHistoryWindow called %d times at the tail, want 0", loader.calls)
	}
}
