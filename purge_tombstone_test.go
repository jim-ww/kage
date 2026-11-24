package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/xmpp"
	omemolib "github.com/jim-ww/omemo-go"
)

// TestPurgedMessageNotRestoredFromArchive checks a purged message stays gone
// when a walk over the archive (a re-sync) meets it again, by either id.
func TestPurgedMessageNotRestoredFromArchive(t *testing.T) {
	ctx := context.Background()
	_, q, err := storage.Open(filepath.Join(t.TempDir(), "kage.db"))
	if err != nil {
		t.Fatal(err)
	}
	s := &accountSession{account: config.Account{JID: "me@example.com"}, db: q}
	a := &adapter{sessions: []*accountSession{s}}
	walk := &mamWalk{healed: map[omemolib.Device]bool{}, resync: true}
	item := func(archiveID, id string) xmpp.ArchivedMessage {
		return xmpp.ArchivedMessage{ArchiveID: archiveID, ID: id, From: "bob@example.com/pc", To: "me@example.com", Body: "regret", SentAt: time.Now()}
	}

	if out := s.processMAMItem(ctx, nil, 0, "bob@example.com", "me@example.com", "bob", item("a1", "m1"), walk); out.msg == nil {
		t.Fatal("item not stored")
	}
	if err := a.PurgeMessage(0, "bob@example.com", "m1"); err != nil {
		t.Fatal(err)
	}

	for _, it := range []xmpp.ArchivedMessage{item("a1", "m1"), item("a1", ""), item("a9", "m1")} {
		if out := s.processMAMItem(ctx, nil, 0, "bob@example.com", "me@example.com", "bob", it, walk); out.msg != nil {
			t.Errorf("archive id %q id %q: purged message stored again", it.ArchiveID, it.ID)
		}
	}
	rows, err := q.ListMessagesByRoster(ctx, storage.ListMessagesByRosterParams{
		AccountJid: "me@example.com", RosterJid: nullString("bob@example.com"), StanzaType: "chat",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("%d rows after purge and re-walk, want 0", len(rows))
	}

	// Another message is still taken in.
	if out := s.processMAMItem(ctx, nil, 0, "bob@example.com", "me@example.com", "bob", item("a2", "m2"), walk); out.msg == nil {
		t.Error("unrelated item not stored")
	}
}
