package ui

import (
	"strings"
	"testing"
)

// TestBounceOverridesAck checks a message the recipient's side bounced is
// shown failed even though our server had acked it, with a notice saying
// why - while a plain dropped-connection failure still defers to the ack.
func TestBounceOverridesAck(t *testing.T) {
	m := newTestModelWithMessages([]Message{{ID: "m1", IsMe: true, Content: "hi", ServerAcked: true}}, nil)

	next, _ := m.Update(MessageSendFailedMsg{To: "bob@example.test", MessageID: "m1"})
	m = next.(Model)
	if m.currentMessages()[0].Failed {
		t.Fatal("a dropped-connection failure overrode the ack")
	}

	next, _ = m.Update(MessageSendFailedMsg{To: "bob@example.test", MessageID: "m1", Reason: "service unavailable"})
	m = next.(Model)
	got := m.currentMessages()[0]
	if !got.Failed || got.ServerAcked {
		t.Fatalf("after bounce: failed=%v acked=%v", got.Failed, got.ServerAcked)
	}
	if !strings.Contains(m.noticeText, "not delivered to bob@example.test: service unavailable") {
		t.Fatalf("notice %q", m.noticeText)
	}
}
