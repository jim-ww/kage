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

// TestOutboxFlushAfterReconnectEncryptsForDeviceAddedDuringOutage is a
// live-server regression test for the offline-queue path adjacent to
// TestReconnectResyncsOmemoDeviceListsForNewPeerDevice
// (reconnect_omemo_live_test.go): that test proves an immediate a.send
// right after reconnectWithBackoff picks up a peer device added during an
// outage. This proves the same thing for a message that was actually
// queued *before* the outage ended and only goes out via
// adapter.flushOutbox once reconnectWithBackoff restores the client -
// flushOutbox re-runs the full a.send/resolveOmemoManagerForMode/
// EncryptMessage path fresh for each queued row (account.go/adapter.go),
// so it should naturally benefit from the same post-reconnect resync, but
// that's worth proving directly since it's a distinct code path with its
// own accountIdx plumbing.
func TestOutboxFlushAfterReconnectEncryptsForDeviceAddedDuringOutage(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()

	aliceClient, err := xmpp.Dial(ctx, "alice@localhost", "alicepw", tlsConfig)
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}
	t.Cleanup(func() { aliceClient.Close() })
	_, aliceDB, err := storage.Open(filepath.Join(t.TempDir(), "alice-outbox-reconnect.db"))
	if err != nil {
		t.Fatalf("open alice storage: %v", err)
	}
	aliceSess := &accountSession{
		account:   config.Account{JID: "alice@localhost", Password: "alicepw"},
		db:        aliceDB,
		tlsConfig: tlsConfig,
	}
	aliceSess.client.Store(aliceClient)
	aliceSess.roster.Store(&map[string]rosterEntry{"bob@localhost": {Subs: "both"}})
	setupOmemo(ctx, aliceSess)
	if aliceSess.omemoMgrV1 == nil {
		t.Fatal("setupOmemo(alice): omemoMgrV1 is nil")
	}
	a := &adapter{sessions: []*accountSession{aliceSess}}

	newOmemoTestSession(ctx, t, "bob@localhost", "bobpw", tlsConfig)
	if err := aliceSess.omemoMgrV1.SyncDevices(ctx, "bob@localhost"); err != nil {
		t.Fatalf("alice initial SyncDevices(bob): %v", err)
	}

	// Alice's connection drops - reconnectWithBackoff hasn't run yet.
	aliceClient.Close()

	// Bob adds a second real device while alice is offline.
	bobGen2 := newOmemoTestSession(ctx, t, "bob@localhost", "bobpw", tlsConfig)

	// A send attempted while still offline queues instead of failing.
	if _, err := a.send(ctx, 0, "bob@localhost", "queued during outage", ui.SendOptions{LocalID: "local-1"}); err != ui.ErrQueued {
		t.Fatalf("a.send while offline = %v, want ui.ErrQueued", err)
	}

	// Alice reconnects - the exact path fixed earlier this session: rebuilds
	// the OMEMO managers against the new client and resyncs peer device
	// lists, including bob's now-two-device list.
	reconnectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	reconnectWithBackoff(reconnectCtx, a, aliceSess)
	if reconnectCtx.Err() != nil {
		t.Fatalf("reconnectWithBackoff did not complete in time: %v", reconnectCtx.Err())
	}

	// flushOutbox replays the queued send now that the client is live.
	a.flushOutbox(ctx, aliceSess)

	body, err := receiveOneChatMessage(ctx, t, bobGen2)
	if err != nil {
		t.Fatalf("bob (gen2, added during the outage) receive: %v", err)
	}
	if body != "queued during outage" {
		t.Fatalf("bob (gen2) decrypted body = %q, want %q", body, "queued during outage")
	}
}
