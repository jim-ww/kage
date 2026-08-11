package main

import (
	"context"
	"crypto/tls"
	"path/filepath"
	"testing"

	omemolib "github.com/jim-ww/omemo-go"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/xmpp"
)

// newOmemoTestSession dials jid against the local Prosody test instance and
// runs it through the same setupOmemo the real app uses, backed by a fresh
// on-disk database (so each call gets its own OMEMO identity/device, exactly
// like a fresh install would).
func newOmemoTestSession(ctx context.Context, t *testing.T, jid, pass string, tlsConfig *tls.Config) *accountSession {
	t.Helper()

	client, err := xmpp.Dial(ctx, jid, pass, tlsConfig)
	if err != nil {
		t.Fatalf("dial %s: %v", jid, err)
	}
	t.Cleanup(func() { client.Close() })

	_, q, err := storage.Open(filepath.Join(t.TempDir(), "omemo-test.db"))
	if err != nil {
		t.Fatalf("open storage for %s: %v", jid, err)
	}

	sess := &accountSession{account: config.Account{JID: jid}, db: q}
	sess.client.Store(client)
	setupOmemo(ctx, sess)
	if sess.omemoMgrV1 == nil {
		t.Fatalf("setupOmemo(%s): omemoMgrV1 is nil", jid)
	}
	return sess
}

// TestOmemoV1DeviceRotationWhilePeerOffline reproduces the real-world bug
// report: Alice and Bob are chatting; Bob "reinstalls" (fresh OMEMO
// identity, brand-new device ID, same JID) while Alice isn't connected to
// witness the live XEP-0163 PEP push. When Alice reconnects, the only thing
// standing between her and permanently encrypting to Bob's dead device is
// resyncPeerDeviceLists's explicit SyncDevices call actually fetching Bob's
// *current* device list from Prosody's PEP node - this test drives that
// exact call (not a mocked transport) and proves a message encrypted
// afterward is decryptable by Bob's new client.
//
// This exercises, against a real server, precisely the chain the bug report
// walked through: (1) server returns Bob's updated device-list item on
// request, (2) Client.FetchOmemoDeviceListV1 decodes it correctly, (3)
// Manager.SyncDevices persists it, (4) a subsequent encrypt actually reaches
// Bob's live device.
func TestOmemoV1DeviceRotationWhilePeerOffline(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()

	alice := newOmemoTestSession(ctx, t, "alice@localhost", "alicepw", tlsConfig)

	bobGen1 := newOmemoTestSession(ctx, t, "bob@localhost", "bobpw", tlsConfig)
	origBobDevice := bobGen1.omemoMgrV1.LocalDevice()

	if err := alice.omemoMgrV1.SyncDevices(ctx, "bob@localhost"); err != nil {
		t.Fatalf("alice initial SyncDevices(bob): %v", err)
	}

	msg1, _, err := alice.omemoMgrV1.EncryptMessage(ctx, "bob@localhost", []byte("hello before rotation"))
	if err != nil {
		t.Fatalf("alice encrypt (pre-rotation): %v", err)
	}
	pt1, err := bobGen1.omemoMgrV1.DecryptMessage(ctx, msg1)
	if err != nil {
		t.Fatalf("bob (gen1) decrypt (pre-rotation): %v", err)
	}
	if string(pt1) != "hello before rotation" {
		t.Fatalf("pre-rotation roundtrip = %q", pt1)
	}

	// Bob "reinstalls": a second client for the same JID, fresh identity and
	// device ID, which republishes bob's PEP device-list node. Alice does
	// NOT reconnect or receive any live push here - this is the "changed
	// while we were offline" scenario.
	bobGen2 := newOmemoTestSession(ctx, t, "bob@localhost", "bobpw", tlsConfig)
	newBobDevice := bobGen2.omemoMgrV1.LocalDevice()

	if newBobDevice.ID == origBobDevice.ID {
		t.Fatalf("expected bob's regenerated device ID to differ from %d, got the same", origBobDevice.ID)
	}

	// This is the exact call resyncPeerDeviceLists makes for every roster
	// contact on every connect - the only mechanism that can recover from a
	// rotation that happened while we were offline.
	if err := alice.omemoMgrV1.SyncDevices(ctx, "bob@localhost"); err != nil {
		t.Fatalf("alice post-rotation SyncDevices(bob): %v", err)
	}

	msg2, _, err := alice.omemoMgrV1.EncryptMessage(ctx, "bob@localhost", []byte("hello after rotation"))
	if err != nil {
		t.Fatalf("alice encrypt (post-rotation): %v", err)
	}

	var sawNewDevice bool
	for _, k := range msg2.Keys {
		if k.Device == newBobDevice.ID {
			sawNewDevice = true
		}
	}
	if !sawNewDevice {
		t.Fatalf("post-rotation encrypt didn't include bob's new device %d among keys for %v",
			newBobDevice.ID, keyDeviceIDs(msg2.Keys))
	}

	pt2, err := bobGen2.omemoMgrV1.DecryptMessage(ctx, msg2)
	if err != nil {
		t.Fatalf("bob (gen2, the new device) failed to decrypt alice's post-resync message: %v", err)
	}
	if string(pt2) != "hello after rotation" {
		t.Fatalf("post-rotation roundtrip = %q", pt2)
	}
}

func keyDeviceIDs(keys []omemolib.RecipientKey) []omemolib.DeviceID {
	ids := make([]omemolib.DeviceID, len(keys))
	for i, k := range keys {
		ids[i] = k.Device
	}
	return ids
}
