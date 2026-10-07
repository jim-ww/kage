package ui

import (
	"testing"

	"charm.land/bubbles/v2/list"
)

// newReplyTestModel builds a model with one open chat holding n messages and
// a tight message cap, so a few arrivals trim the front of the window.
func newReplyTestModel(t *testing.T, n, cap int) (Model, *recordingReplySender) {
	t.Helper()
	sender := &recordingReplySender{}
	m := newTestModelWithSender(sender, nil)
	m.maxMessagesPerChat = cap
	m.width, m.termHeight = 80, 24
	m.updateSizes()
	msgs := make([]Message, n)
	for i := range msgs {
		msgs[i] = Message{ID: msgID(i), Author: "bob", Content: "body " + msgID(i)}
	}
	chat := Chat{Name: "bob", Address: "bob@example.test"}
	m.accounts = []Account{{Name: "me@example.test", Chats: []list.Item{chat}, Messages: map[int][]Message{0: msgs}}}
	m.currentAccount = 0
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.chats.Select(0)
	m.selectedView = viewChat
	return m, sender
}

func msgID(i int) string { return string(rune('a'+i%26)) + string(rune('0'+i/26)) }

// recordingReplySender captures the SendOptions a send actually goes out
// with, which is where a drifted reply target shows up.
type recordingReplySender struct {
	fakeSuccessSender
	replyToID  string
	quotedBody string
}

func (s *recordingReplySender) Send(accountIdx int, to, body string, opts SendOptions) (string, error) {
	s.replyToID, s.quotedBody = opts.ReplyToID, opts.QuotedBody
	return "sent-id", nil
}

// TestReplyTargetSurvivesArrivingMessages is the regression for a reply
// quoting the wrong message: the target used to be remembered as an index
// into the chat's message slice, and messages arriving while the reply was
// being typed trim the front of that slice (maxMessagesPerChat), sliding
// every index - so the reply went out threaded to, and quoting, whichever
// message had taken that slot.
func TestReplyTargetSurvivesArrivingMessages(t *testing.T) {
	m, sender := newReplyTestModel(t, 5, 5)

	// Reply to a message in the middle of the loaded window, so the
	// arrivals below shift its index without trimming it away.
	m.selectedMsg = 2
	want := m.accounts[0].Messages[0][2]
	if cmd := m.actionReplyMessage(); cmd != nil {
		_ = cmd()
	}

	// Two messages arrive, trimming two off the front of the window - the
	// target slides from index 2 to index 0.
	for i := range 2 {
		next, cmd := m.Update(IncomingMessageMsg{
			AccountIdx: 0,
			From:       "bob@example.test",
			Message:    Message{ID: "new" + msgID(i), Author: "bob", Content: "later"},
		})
		m = next.(Model)
		runCmd(cmd)
	}
	if got := m.accounts[0].Messages[0][0].ID; got != want.ID {
		t.Fatalf("test setup: target is at index %d, want it slid to 0 by the trim (window starts at %q)", m.replyTo.index(m.accounts[0].Messages[0]), got)
	}

	m.input.SetValue("my reply")
	runCmd(m.sendCurrentInput())

	if sender.replyToID != want.ID {
		t.Errorf("replied to %q, want the message the reply was started on (%q)", sender.replyToID, want.ID)
	}
	if sender.quotedBody != MessagePreviewContent(want) {
		t.Errorf("quoted %q, want %q", sender.quotedBody, MessagePreviewContent(want))
	}
}

// A reply target that has left the loaded window still threads by its
// stanza ID - what the user asked for survives even though the quote
// preview can no longer be built - and never onto whatever has taken its
// old index.
func TestReplyTargetGoneStillThreadsByID(t *testing.T) {
	m, sender := newReplyTestModel(t, 3, 3)
	m.selectedMsg = 0
	if cmd := m.actionReplyMessage(); cmd != nil {
		_ = cmd()
	}
	// The whole window is replaced by one that doesn't contain the target.
	m.accounts[0].Messages[0] = []Message{{ID: "zz", Author: "bob", Content: "unrelated"}}

	m.input.SetValue("my reply")
	runCmd(m.sendCurrentInput())

	if sender.replyToID != "a0" {
		t.Errorf("replied to %q, want the original target a0 by ID", sender.replyToID)
	}
	if sender.quotedBody != "" {
		t.Errorf("quoted %q, want no quote once the original has left the window", sender.quotedBody)
	}
}
