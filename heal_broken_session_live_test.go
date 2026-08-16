package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jim-ww/kage/config"
	kageomemo "github.com/jim-ww/kage/crypto/omemo"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/xmpp"
	omemolib "github.com/jim-ww/omemo-go"
)

// TestHealBrokenSessionResyncsStaleDeviceCache is a live-server regression
// test for a bug in healBrokenSession (events.go): its recovery reply for
// ErrUnknownSession/ErrPreKeyNotFound (e.g. "consume prekey N: ... sql: no
// rows in result set", from a peer retrying a session build against a
// one-time prekey we already consumed) only reaches devices already in the
// manager's *cached* device list for that peer, via
// EncryptKeyTransport -> recipientDevices -> devicesFor, which only
// self-refreshes when that cache is completely empty. If the one device
// that triggered the heal isn't in an otherwise non-empty cache - exactly
// the kind of staleness this whole area is prone to, e.g. after a
// reconnect gap or before this peer's most recent device was ever synced -
// the "healing" key-transport used to silently never reach it while still
// logging success, leaving that device stuck retrying its stale prekey
// message forever.
//
// This seeds alice's cached device list for bob with an unrelated fake
// device ID only (a stale, non-empty cache missing bob's real device),
// then calls healBrokenSession as if bob's real device had just failed to
// decrypt, and verifies (a) alice's cache picks up bob's real device and
// (b) bob's live client actually receives a real OMEMO stanza over the
// wire - not just that the call returned without error.
func TestHealBrokenSessionResyncsStaleDeviceCache(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()

	aliceClient, err := xmpp.Dial(ctx, "alice@localhost", "alicepw", tlsConfig)
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}
	t.Cleanup(func() { aliceClient.Close() })

	_, aliceDB, err := storage.Open(filepath.Join(t.TempDir(), "alice-heal.db"))
	if err != nil {
		t.Fatalf("open alice storage: %v", err)
	}

	aliceSess := &accountSession{
		account: config.Account{JID: "alice@localhost", Password: "alicepw"},
		db:      aliceDB,
	}
	aliceSess.client.Store(aliceClient)
	setupOmemo(ctx, aliceSess)
	if aliceSess.omemoMgrV1 == nil {
		t.Fatal("setupOmemo(alice): omemoMgrV1 is nil")
	}

	bob := newOmemoTestSession(ctx, t, "bob@localhost", "bobpw", tlsConfig)
	bobDevice := bob.omemoMgrV1.LocalDevice()

	// Seed a stale, non-empty cache for bob that's missing his real device.
	store := kageomemo.NewStore(aliceDB, "alice@localhost", omemolib.ProtocolV1)
	if err := store.SetDevices(ctx, "bob@localhost", []omemolib.DeviceID{999999}); err != nil {
		t.Fatalf("seeding stale device cache: %v", err)
	}

	healBrokenSession(ctx, aliceSess, aliceSess.omemoMgrV1, bobDevice, "bob@localhost")

	devices, err := store.Devices(ctx, "bob@localhost")
	if err != nil {
		t.Fatalf("reading alice's cached devices for bob: %v", err)
	}
	found := false
	for _, id := range devices {
		if id == bobDevice.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("alice's device cache for bob after healing = %v, want it to include bob's real device %d", devices, bobDevice.ID)
	}

	// The strongest proof the heal actually reached bob's live device: a
	// real OMEMO stanza addressed to him arrives over the wire, not just
	// that healBrokenSession returned without logging an error.
	deadline := time.After(15 * time.Second)
	for {
		select {
		case ev := <-bob.client.Load().Events():
			msgEv, ok := ev.(xmpp.MessageEvent)
			// Presence bursts, and any unrelated message left queued
			// server-side by another test sharing this devtest instance
			// (e.g. an offline plaintext send to bob@localhost from a
			// different test's run) - keep waiting for the actual OMEMO
			// reply rather than failing on the first unrelated event.
			if !ok || msgEv.EncryptedV1 == nil {
				continue
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for bob to receive the healing key-transport reply")
		}
	}
}
