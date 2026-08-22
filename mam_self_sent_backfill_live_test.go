//go:build integration

package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/xmpp"
)

// TestMAMBackfillsOwnMessagesSentFromAnotherDeviceWhileOffline is a
// regression/behavior test against a real Prosody instance for the scenario
// reported live: alice sends messages to bob from her phone while her kage
// session (a separate resource of the same account) is closed. XEP-0280
// carbons can't help here - they only fan out to resources that are
// connected at send time - so the only way kage's local storage ever learns
// about those self-sent messages is syncArchiveForContact backfilling them
// from alice's own MAM archive on reconnect (account.go).
//
// This drives the real production code path (syncArchive/syncArchiveForContact/
// processMAMItem) against a real server rather than asserting anything about
// server internals, so it also proves Prosody's mod_mam does archive a
// sender's own copy of a plaintext message by default (default_archive_policy
// in devtest/prosody/prosody.cfg.lua.tmpl) - the piece of the original bug
// report that couldn't be settled by reading kage's own debug.log alone.
func TestMAMBackfillsOwnMessagesSentFromAnotherDeviceWhileOffline(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()

	// --- alice's kage session ("desktop"): connects once just so its own
	// MAM sync cursor starts empty/caught-up, same as a real first run,
	// then disconnects - standing in for "kage is closed".
	kageClient, err := xmpp.Dial(ctx, "alice@localhost", "alicepw", tlsConfig)
	if err != nil {
		t.Fatalf("dial alice (kage): %v", err)
	}
	_, aliceDB, err := storage.Open(filepath.Join(t.TempDir(), "alice-mam-backfill.db"))
	if err != nil {
		t.Fatalf("open alice storage: %v", err)
	}
	aliceSess := &accountSession{
		account: config.Account{JID: "alice@localhost", Password: "alicepw"},
		db:      aliceDB,
	}
	aliceSess.client.Store(kageClient)
	aliceSess.roster.Store(&map[string]rosterEntry{"bob@localhost": {Subs: "both"}})

	// kage "closes": drop the connection entirely, exactly like the daemon
	// exiting or the machine sleeping.
	if err := kageClient.Close(); err != nil {
		t.Fatalf("close alice (kage): %v", err)
	}

	// --- alice's phone: a second, independent live connection to the same
	// bare JID (the server assigns it its own resource) - kage never sees
	// this session or any live event from it.
	phoneClient, err := xmpp.Dial(ctx, "alice@localhost", "alicepw", tlsConfig)
	if err != nil {
		t.Fatalf("dial alice (phone): %v", err)
	}
	t.Cleanup(func() { phoneClient.Close() })

	// devtest/prosody keeps its archive forever (archive_expires_after =
	// "never" in prosody.cfg.lua.tmpl, so unrelated tests/runs can share
	// alice@localhost's history) - a unique-per-run prefix is what lets this
	// test pick its own 3 messages back out of however much other archive
	// content already exists for alice<->bob.
	prefix := fmt.Sprintf("mam-backfill-%d ", time.Now().UnixNano())
	sentBodies := []string{prefix + "hey from my phone", prefix + "still here", prefix + "third one"}
	for _, body := range sentBodies {
		if _, err := phoneClient.Send(ctx, "bob@localhost", body, xmpp.SendOptions{}); err != nil {
			t.Fatalf("phone send %q: %v", body, err)
		}
	}

	// Prosody archives the stanza asynchronously as it routes it - give it a
	// moment to land before kage reconnects and asks for it, or the MAM
	// query below can race the server's own indexing.
	time.Sleep(1 * time.Second)

	// --- kage reconnects and runs the same archive sync a real reconnect
	// triggers (account.go's connectAccountLive callers all follow it with
	// syncArchive).
	kageClient2, err := xmpp.Dial(ctx, "alice@localhost", "alicepw", tlsConfig)
	if err != nil {
		t.Fatalf("dial alice (kage, reconnect): %v", err)
	}
	t.Cleanup(func() { kageClient2.Close() })
	aliceSess.client.Store(kageClient2)

	syncArchive(ctx, nil, 0, aliceSess)

	allRows, err := aliceDB.ListMessagesByRoster(ctx, storage.ListMessagesByRosterParams{
		AccountJid: "alice@localhost",
		RosterJid:  nullString("bob@localhost"),
	})
	if err != nil {
		t.Fatalf("ListMessagesByRoster: %v", err)
	}
	var rows []storage.ListMessagesByRosterRow
	for _, r := range allRows {
		if strings.HasPrefix(r.Body.String, prefix) {
			rows = append(rows, r)
		}
	}
	if len(rows) != len(sentBodies) {
		t.Fatalf("got %d messages backfilled for bob@localhost matching this run's prefix, want %d (out of %d total rows; rows=%+v)", len(rows), len(sentBodies), len(allRows), rows)
	}
	for i, row := range rows {
		if !row.Sent {
			t.Errorf("message %d (%q): Sent=false, want true - a message from alice's own other device must be flagged as sent by her, not received from bob", i, row.Body.String)
		}
		if row.Body.String != sentBodies[i] {
			t.Errorf("message %d body = %q, want %q", i, row.Body.String, sentBodies[i])
		}
	}
}
