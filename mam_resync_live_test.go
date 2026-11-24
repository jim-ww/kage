//go:build integration

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
)

// TestResyncRecoversSkippedMessage checks a resync walk decrypts a message
// an earlier sync skipped, from the ratchet's skipped-message key a later
// message left behind, and leaves the stored MAM cursor alone.
func TestResyncRecoversSkippedMessage(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()

	_, bobDB, err := storage.Open(filepath.Join(t.TempDir(), "bob.db"))
	if err != nil {
		t.Fatal(err)
	}
	bobSess := &accountSession{account: config.Account{JID: "bob@localhost", Password: "bobpw"}, db: bobDB, tlsConfig: tlsConfig}
	bobClient, err := xmpp.Dial(ctx, "bob@localhost", "bobpw", tlsConfig)
	if err != nil {
		t.Fatalf("dial bob: %v", err)
	}
	t.Cleanup(func() { bobClient.Close() })
	bobSess.client.Store(bobClient)
	setupOmemo(ctx, bobSess)
	bob := &adapter{sessions: []*accountSession{bobSess}}

	_, aliceDB, err := storage.Open(filepath.Join(t.TempDir(), "alice.db"))
	if err != nil {
		t.Fatal(err)
	}
	aliceSess := &accountSession{account: config.Account{JID: "alice@localhost", Password: "alicepw"}, db: aliceDB, tlsConfig: tlsConfig}
	aliceClient, err := xmpp.Dial(ctx, "alice@localhost", "alicepw", tlsConfig)
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}
	t.Cleanup(func() { aliceClient.Close() })
	aliceSess.client.Store(aliceClient)
	setupOmemo(ctx, aliceSess)
	alice := &adapter{sessions: []*accountSession{aliceSess}}

	since := time.Now().Add(-time.Second)
	// deliver hands the archived copy of id to sess's live path, as if it
	// had been pushed to it.
	deliver := func(sess *accountSession, client *xmpp.Client, peer, id string) {
		t.Helper()
		// The send went out on the other account's stream, so the archive
		// may not have it yet.
		var am xmpp.ArchivedMessage
		var err error
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(100 * time.Millisecond) {
			if am, err = findArchivedByID(ctx, t, client, peer, id, since); err == nil || time.Now().After(deadline) {
				break
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		dispatchEvent(ctx, nil, 0, sess, xmpp.MessageEvent{ID: am.ID, From: am.From, To: am.To, SentAt: am.SentAt, EncryptedV1: am.EncryptedV1})
	}
	send := func(a *adapter, to, body string) string {
		t.Helper()
		id, err := a.send(ctx, 0, to, body, ui.SendOptions{})
		if err != nil {
			t.Fatalf("send %q: %v", body, err)
		}
		return id
	}

	// A session both ways, so what follows are plain ratchet messages, not
	// prekey ones.
	deliver(bobSess, bobClient, "alice@localhost", send(alice, "bob@localhost", "hello"))
	deliver(aliceSess, aliceClient, "bob@localhost", send(bob, "alice@localhost", "hi"))

	missed := send(alice, "bob@localhost", "missed message")
	deliver(bobSess, bobClient, "alice@localhost", send(alice, "bob@localhost", "later message"))

	if n := syncArchiveForContact(ctx, nil, 0, bobSess, bobClient, "alice@localhost", mamCursor{}, true); n < 1 {
		t.Errorf("resync recovered %d messages, want at least 1", n)
	}

	rows, err := bobDB.ListMessagesByRoster(ctx, storage.ListMessagesByRosterParams{
		AccountJid: "bob@localhost", RosterJid: nullString("alice@localhost"), StanzaType: "chat",
	})
	if err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, r := range rows {
		if r.Idattr.String == missed {
			bodies = append(bodies, r.Body.String)
		}
	}
	if len(bodies) != 1 || bodies[0] != "missed message" {
		t.Errorf("missed message stored as %q, want it once, decrypted", bodies)
	}

	cursors, err := bobDB.ListMamSyncCursors(ctx, "bob@localhost")
	if err != nil {
		t.Fatal(err)
	}
	if len(cursors) != 0 {
		t.Errorf("resync stored MAM cursors %+v, want none", cursors)
	}
}
