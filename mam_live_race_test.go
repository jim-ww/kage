//go:build integration

package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/xmpp"
)

// TestMAMBackfillVsLiveMessageRaceDoesNotCorruptDecrypt is a live-server
// regression test for accountSession.omemoMu (account.go): MAM backfill
// (syncArchiveForContact/processMAMItem) and the live path
// (handleIncomingMessage, dispatched from listen/dispatchEvent) can both
// try to decrypt the same OMEMO ciphertext for the same stanza ID - e.g.
// bob reconnects and both backfills his MAM archive and (on some server/
// timing) sees the same message delivered live around the same time. OMEMO
// decrypt is not idempotent (the double ratchet advances irreversibly, and
// a PreKeyMessage's one-time prekey is consumed exactly once), so decrypting
// the same ciphertext twice concurrently would either corrupt the ratchet
// state or spuriously fail the second attempt. omemoMu exists specifically
// to serialize these two paths against each other; this proves it actually
// does, instead of just trusting the doc comment.
//
// Alice sends bob one message while bob's client is offline (so it only
// ever reaches him via MAM, never a live push). Bob reconnects, fetches the
// archived item over the real Prosody MAM archive, then this fires several
// concurrent goroutines simulating a live delivery of that exact stanza
// (dispatchEvent, the same entrypoint listen() uses) racing against a real
// syncArchiveForContact backfill of it - and asserts exactly one correctly
// decrypted message ends up stored, not zero, not duplicated, not corrupted.
func TestMAMBackfillVsLiveMessageRaceDoesNotCorruptDecrypt(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()

	_, bobDB, err := storage.Open(filepath.Join(t.TempDir(), "bob-mam-race.db"))
	if err != nil {
		t.Fatalf("open bob storage: %v", err)
	}
	bobSess := &accountSession{
		account:   config.Account{JID: "bob@localhost", Password: "bobpw"},
		db:        bobDB,
		tlsConfig: tlsConfig,
	}

	// Bob comes online once just long enough to publish his OMEMO identity
	// (device list + bundle), then goes "offline" - closing the client, but
	// keeping the same on-disk identity/prekey state for reuse below.
	bobClient1, err := xmpp.Dial(ctx, "bob@localhost", "bobpw", tlsConfig)
	if err != nil {
		t.Fatalf("dial bob (first time): %v", err)
	}
	bobSess.client.Store(bobClient1)
	setupOmemo(ctx, bobSess)
	if bobSess.omemoMgrV1 == nil {
		t.Fatal("setupOmemo(bob): omemoMgrV1 is nil")
	}
	bobClient1.Close()

	// Alice sends while bob is offline - this message only ever reaches him
	// via MAM archive replay, never a genuine live push.
	aliceClient, err := xmpp.Dial(ctx, "alice@localhost", "alicepw", tlsConfig)
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}
	t.Cleanup(func() { aliceClient.Close() })
	_, aliceDB, err := storage.Open(filepath.Join(t.TempDir(), "alice-mam-race.db"))
	if err != nil {
		t.Fatalf("open alice storage: %v", err)
	}
	aliceSess := &accountSession{account: config.Account{JID: "alice@localhost", Password: "alicepw"}, db: aliceDB}
	aliceSess.client.Store(aliceClient)
	setupOmemo(ctx, aliceSess)
	a := &adapter{sessions: []*accountSession{aliceSess}}

	sentID, err := a.send(ctx, 0, "bob@localhost", "race message", ui.SendOptions{})
	if err != nil {
		t.Fatalf("alice send while bob offline: %v", err)
	}

	// Bob reconnects - a fresh client, OMEMO managers rebuilt against it,
	// same as any real reconnect/relaunch.
	bobClient2, err := xmpp.Dial(ctx, "bob@localhost", "bobpw", tlsConfig)
	if err != nil {
		t.Fatalf("dial bob (second time): %v", err)
	}
	t.Cleanup(func() { bobClient2.Close() })
	bobSess.client.Store(bobClient2)
	setupOmemo(ctx, bobSess)

	// bob@localhost is a real, persistent devtest account shared by every
	// live test in this package, and MAM archives everything forever
	// (devtest/prosody's default_archive_policy), so his archive with
	// alice can carry a lot of unrelated history from other tests run
	// earlier in this or a previous process against the same server - find
	// this test's own message by stanza ID rather than assuming it's the
	// most recent item, paging through as far as it takes.
	am, err := findArchivedByID(ctx, t, bobClient2, "alice@localhost", sentID)
	if err != nil {
		t.Fatalf("finding this test's message in bob's MAM archive: %v", err)
	}
	if am.EncryptedV1 == nil {
		t.Fatalf("archived item isn't OMEMO-v1 encrypted: %+v", am)
	}

	liveMsgEv := xmpp.MessageEvent{
		ID:          am.ID,
		From:        am.From,
		SentAt:      am.SentAt,
		EncryptedV1: am.EncryptedV1,
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	const liveRacers = 5
	for i := 0; i < liveRacers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			dispatchEvent(ctx, nil, 0, bobSess, liveMsgEv)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		syncArchiveForContact(ctx, nil, 0, bobSess, bobClient2, "alice@localhost", mamCursor{})
	}()
	close(start)
	wg.Wait()

	rows, err := bobDB.ListMessagesByRoster(ctx, storage.ListMessagesByRosterParams{
		AccountJid: "bob@localhost",
		RosterJid:  nullString("alice@localhost"),
	})
	if err != nil {
		t.Fatalf("listing bob's stored messages: %v", err)
	}
	// Filter to this test's own message by stanza ID - bob's archive (and
	// so his backfilled history) can include unrelated messages from other
	// live tests sharing this account; what matters here is that the race
	// produced exactly one stored copy of THIS message, correctly decrypted.
	var matches []string
	for _, r := range rows {
		if r.Idattr.String == sentID {
			matches = append(matches, r.Body.String)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("bob stored %d copies of the raced message (id %q), want exactly 1: %v", len(matches), sentID, matches)
	}
	if matches[0] != "race message" {
		t.Fatalf("bob's stored message body = %q, want %q (decrypt corruption or failure)", matches[0], "race message")
	}
}

// findArchivedByID pages through peerJID's MAM archive (oldest first) until
// it finds the item with stanza id wantID, or exhausts the archive.
func findArchivedByID(ctx context.Context, t *testing.T, client *xmpp.Client, peerJID, wantID string) (xmpp.ArchivedMessage, error) {
	t.Helper()
	after := ""
	for page := 0; page < 50; page++ {
		items, complete, err := client.FetchArchive(ctx, peerJID, after, 200)
		if err != nil {
			return xmpp.ArchivedMessage{}, err
		}
		for _, it := range items {
			if it.ID == wantID {
				return it, nil
			}
		}
		if complete || len(items) == 0 {
			break
		}
		after = items[len(items)-1].ArchiveID
	}
	return xmpp.ArchivedMessage{}, fmt.Errorf("id %q not found in archive with %s", wantID, peerJID)
}
