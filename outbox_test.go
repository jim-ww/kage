package main

import (
	"context"
	"testing"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/ui"
)

// TestSendQueuesWhileOffline verifies that any send attempted while an
// account has no live client - a plain message, a reaction, a retraction, or
// a correction - is queued instead of failing outright, and that
// adapter.flushOutbox replays queued sends (requeuing them again if still
// offline, rather than dropping them) once called.
func TestSendQueuesWhileOffline(t *testing.T) {
	sess := &accountSession{account: config.Account{JID: "alice@example.com"}}
	a := &adapter{sessions: []*accountSession{sess}}

	cases := []struct {
		name string
		opts ui.SendOptions
	}{
		{"plain message", ui.SendOptions{}},
		{"reaction", ui.SendOptions{ReactionTargetID: "msg1", Reactions: []string{"👍"}}},
		{"retraction", ui.SendOptions{RetractID: "msg1"}},
		{"correction", ui.SendOptions{ReplaceID: "msg1"}},
	}

	for i, c := range cases {
		id, err := a.send(context.Background(), 0, "bob@example.com", "hello", c.opts)
		if err != nil {
			t.Fatalf("%s while offline: got err %v, want nil (queued)", c.name, err)
		}
		if id != "" {
			t.Fatalf("%s while offline: got id %q, want empty (nothing sent yet)", c.name, id)
		}

		sess.outboxMu.Lock()
		queued := len(sess.outbox)
		sess.outboxMu.Unlock()
		if want := i + 1; queued != want {
			t.Fatalf("outbox length after %s = %d, want %d", c.name, queued, want)
		}
	}

	// flushOutbox with still no live client replays everything through
	// a.send, which queues each one right back (nothing is lost, just not
	// delivered yet) rather than dropping it.
	a.flushOutbox(context.Background(), sess)

	sess.outboxMu.Lock()
	queued := len(sess.outbox)
	sess.outboxMu.Unlock()
	if want := len(cases); queued != want {
		t.Fatalf("outbox length after flush while still offline = %d, want %d (requeued, not dropped)", queued, want)
	}
}

// TestUploadFileQueuesWhileOffline verifies that a staged attachment's
// upload+send is queued as one unit when the account is offline (instead of
// erroring), and that flushOutbox puts it right back in the queue if still
// offline rather than losing it.
func TestUploadFileQueuesWhileOffline(t *testing.T) {
	sess := &accountSession{account: config.Account{JID: "alice@example.com"}}
	a := &adapter{sessions: []*accountSession{sess}}

	msg := a.UploadFile(0, "bob@example.com", "/tmp/report.pdf", "here you go", ui.SendOptions{ReplyToID: "msg1"})
	result, ok := msg.(ui.FileUploadResultMsg)
	if !ok {
		t.Fatalf("UploadFile returned %T, want ui.FileUploadResultMsg", msg)
	}
	if !result.Queued {
		t.Fatal("UploadFile while offline: Queued = false, want true")
	}
	if result.Err != nil {
		t.Fatalf("UploadFile while offline: Err = %v, want nil", result.Err)
	}

	sess.outboxMu.Lock()
	queued := len(sess.outbox)
	sess.outboxMu.Unlock()
	if queued != 1 {
		t.Fatalf("outbox length = %d, want 1", queued)
	}

	a.flushOutbox(context.Background(), sess)

	sess.outboxMu.Lock()
	queued = len(sess.outbox)
	sess.outboxMu.Unlock()
	if queued != 1 {
		t.Fatalf("outbox length after flush while still offline = %d, want 1 (requeued, not dropped)", queued)
	}
}
