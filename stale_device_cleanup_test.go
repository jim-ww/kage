package main

import (
	"context"
	"testing"

	omemolib "github.com/jim-ww/omemo-go"
)

// TestStaleDeviceDroppedAfterRemovalFromPeerDeviceList is a live-server
// regression test complementing TestOmemoV1DeviceRotationWhilePeerOffline
// (omemo_devicelist_live_test.go), which only proves a *newly added* device
// gets picked up. It doesn't check the other direction: once a resync
// happens, does a device the peer's own published list no longer contains
// actually get dropped from our cache, or does it linger and keep getting
// encrypted to forever?
//
// crypto/omemo/store.go's SetDevices is delete-then-insert (a full
// replace, not a merge) each time Manager.SyncDevices runs, so this should
// already work - this proves it does, against a real Prosody devicelist
// publish/fetch round trip, not just by reading the SQL.
func TestStaleDeviceDroppedAfterRemovalFromPeerDeviceList(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()

	alice := newOmemoTestSession(ctx, t, "alice@localhost", "alicepw", tlsConfig)
	bobGen1 := newOmemoTestSession(ctx, t, "bob@localhost", "bobpw", tlsConfig)
	oldDevice := bobGen1.omemoMgrV1.LocalDevice()

	if err := alice.omemoMgrV1.SyncDevices(ctx, "bob@localhost"); err != nil {
		t.Fatalf("alice initial SyncDevices(bob): %v", err)
	}
	msg1, _, err := alice.omemoMgrV1.EncryptMessage(ctx, "bob@localhost", []byte("before removal"))
	if err != nil {
		t.Fatalf("alice encrypt (before removal): %v", err)
	}
	if !keysInclude(msg1.Keys, oldDevice.ID) {
		t.Fatalf("expected alice's pre-removal encrypt to target bob's device %d, keys = %v", oldDevice.ID, keyDeviceIDs(msg1.Keys))
	}

	// A second real device for bob (own identity/bundle, so alice can
	// actually establish a session with it) - setupOmemo's own publish
	// step appends it alongside oldDevice, same as gen1/gen2 rotation
	// elsewhere in this package. Then bob manually removes the old device
	// entry from his own published list (e.g. via Conversations' "OMEMO
	// Fingerprints" screen), republishing with only the new one - unlike
	// plain rotation, this simulates the old ID actually disappearing from
	// the published list entirely, not just a second one being added.
	bobGen2 := newOmemoTestSession(ctx, t, "bob@localhost", "bobpw", tlsConfig)
	newDevice := bobGen2.omemoMgrV1.LocalDevice()
	client := bobGen2.client.Load()
	if err := client.PublishOmemoDeviceListV1(ctx, omemolib.DeviceList{
		JID:     "bob@localhost",
		Devices: []omemolib.DeviceID{newDevice.ID},
	}); err != nil {
		t.Fatalf("publishing bob's replacement device list: %v", err)
	}

	if err := alice.omemoMgrV1.SyncDevices(ctx, "bob@localhost"); err != nil {
		t.Fatalf("alice post-removal SyncDevices(bob): %v", err)
	}

	msg2, _, err := alice.omemoMgrV1.EncryptMessage(ctx, "bob@localhost", []byte("after removal"))
	if err != nil {
		t.Fatalf("alice encrypt (after removal): %v", err)
	}
	if keysInclude(msg2.Keys, oldDevice.ID) {
		t.Fatalf("alice's post-removal encrypt still targets bob's removed device %d, keys = %v", oldDevice.ID, keyDeviceIDs(msg2.Keys))
	}
	if !keysInclude(msg2.Keys, newDevice.ID) {
		t.Fatalf("alice's post-removal encrypt doesn't target bob's replacement device %d, keys = %v", newDevice.ID, keyDeviceIDs(msg2.Keys))
	}
}

func keysInclude(keys []omemolib.RecipientKey, id omemolib.DeviceID) bool {
	for _, k := range keys {
		if k.Device == id {
			return true
		}
	}
	return false
}
