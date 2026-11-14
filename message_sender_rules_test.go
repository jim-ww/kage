package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/xmpp"
)

// TestCorrectionsAndRetractionsOnlyBySender checks a correction or
// retraction applies only to its own sender's message: one from the
// contact naming the id of a message we sent leaves it be (XEP-0308 §4),
// while ours, from another of our devices, still applies to our own.
func TestCorrectionsAndRetractionsOnlyBySender(t *testing.T) {
	ctx := context.Background()
	_, q, err := storage.Open(filepath.Join(t.TempDir(), "kage.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	s := &accountSession{account: config.Account{JID: "me@example.com"}, db: q}
	srv := ipc.NewServer()
	deliver := func(ev xmpp.MessageEvent) {
		ev.SentAt = time.Now()
		handleIncomingMessage(ctx, srv, 0, s, ev)
	}
	fromBob := func(ev xmpp.MessageEvent) xmpp.MessageEvent {
		ev.From, ev.To = "bob@example.com/pc", "me@example.com/laptop"
		return ev
	}
	fromOurPhone := func(ev xmpp.MessageEvent) xmpp.MessageEvent {
		ev.From, ev.To, ev.Outgoing = "me@example.com/phone", "bob@example.com", true
		return ev
	}

	deliver(fromOurPhone(xmpp.MessageEvent{ID: "ours", Body: "what I said"}))
	deliver(fromBob(xmpp.MessageEvent{ID: "his", Body: "what bob said"}))

	deliver(fromBob(xmpp.MessageEvent{ID: "c1", ReplaceID: "ours", Body: "words put in my mouth"}))
	deliver(fromBob(xmpp.MessageEvent{ID: "r1", RetractID: "ours"}))
	deliver(fromBob(xmpp.MessageEvent{ID: "c2", ReplaceID: "his", Body: "what bob meant"}))
	deliver(fromOurPhone(xmpp.MessageEvent{ID: "c3", ReplaceID: "ours", Body: "what I meant"}))

	rows, err := q.ListMessagesByRoster(ctx, storage.ListMessagesByRosterParams{
		AccountJid: "me@example.com", RosterJid: sql.NullString{String: "bob@example.com", Valid: true}, StanzaType: "chat",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]storage.Message{}
	for _, r := range rows {
		got[r.Idattr.String] = r
	}
	if ours := got["ours"]; ours.Body.String != "what I meant" || ours.Retracted {
		t.Errorf("our message: body %q retracted %v, want our own correction only", ours.Body.String, ours.Retracted)
	}
	if his := got["his"]; his.Body.String != "what bob meant" {
		t.Errorf("bob's message: body %q, want his correction", his.Body.String)
	}
}
