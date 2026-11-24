package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/xmpp"
	omemolib "github.com/jim-ww/omemo-go"
)

// storedMessage is the one row of bob's chat with id.
func storedMessage(t *testing.T, q *storage.Queries, id string) storage.Message {
	t.Helper()
	rows, err := q.ListMessagesByRoster(context.Background(), storage.ListMessagesByRosterParams{
		AccountJid: "me@example.com", RosterJid: nullString("bob@example.com"), StanzaType: "chat",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Idattr.String == id {
			return r
		}
	}
	t.Fatalf("no stored message %q", id)
	return storage.Message{}
}

// TestDelayedLiveMessageStoredAtSentTime checks a live message is stored at
// the time it was sent, not when it reached us, and reaches the UI with its
// row id.
func TestDelayedLiveMessageStoredAtSentTime(t *testing.T) {
	srv, events := attachedClient(t)
	_, q, err := storage.Open(filepath.Join(t.TempDir(), "kage.db"))
	if err != nil {
		t.Fatal(err)
	}
	s := &accountSession{account: config.Account{JID: "me@example.com"}, db: q}
	sent := time.Now().Add(-26 * time.Hour).Truncate(time.Second)

	handleIncomingMessage(context.Background(), srv, 0, s, xmpp.MessageEvent{
		ID: "m1", From: "bob@example.com/pc", Body: "while you were away", SentAt: sent,
	})

	if row := storedMessage(t, q, "m1"); row.Delay != sent.Unix() {
		t.Errorf("stored delay = %v, want %v", time.Unix(row.Delay, 0), sent)
	}
	var added ui.ChatAddedMsg
	awaitEvent(t, events, &added, 5*time.Second)
	var incoming ui.IncomingMessageMsg
	awaitEvent(t, events, &incoming, 5*time.Second)
	if !incoming.Message.SentAt.Equal(sent) || incoming.Message.StoreID == 0 {
		t.Errorf("broadcast SentAt %v StoreID %d, want %v and the row id", incoming.Message.SentAt, incoming.Message.StoreID, sent)
	}
}

// TestUnstampedArchiveItemStoredNow checks an archive item without a time
// is stored as arriving now, not at the zero time.
func TestUnstampedArchiveItemStoredNow(t *testing.T) {
	_, q, err := storage.Open(filepath.Join(t.TempDir(), "kage.db"))
	if err != nil {
		t.Fatal(err)
	}
	s := &accountSession{account: config.Account{JID: "me@example.com"}, db: q}
	walk := &mamWalk{healed: map[omemolib.Device]bool{}}

	out := s.processMAMItem(context.Background(), nil, 0, "bob@example.com", "me@example.com", "bob",
		xmpp.ArchivedMessage{ArchiveID: "a1", ID: "m1", From: "bob@example.com/pc", To: "me@example.com", Body: "hi"}, walk)
	if out.msg == nil {
		t.Fatal("item not stored")
	}
	if d := time.Since(out.msg.SentAt); d < 0 || d > time.Minute {
		t.Errorf("SentAt = %v, want now", out.msg.SentAt)
	}
	if row := storedMessage(t, q, "m1"); time.Since(time.Unix(row.Delay, 0)) > time.Minute {
		t.Errorf("stored delay = %v, want now", time.Unix(row.Delay, 0))
	}
}
