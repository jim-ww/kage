package main

import (
	"context"
	"path/filepath"
	"testing"

	omemolib "github.com/jim-ww/omemo-go"

	"github.com/jim-ww/kage/config"
	kageomemo "github.com/jim-ww/kage/crypto/omemo"
	"github.com/jim-ww/kage/storage"
)

// legacyDeviceID is an out-of-range OMEMO device ID of the kind kage used to
// generate by taking four random bytes as a uint32 - this exact value was
// observed in the wild, and is unparseable by every libsignal-derived client
// (see omemolib.MaxDeviceID).
const legacyDeviceID = omemolib.DeviceID(3876150189)

func TestPruneOwnDeviceList(t *testing.T) {
	const jid = "me@example.test"
	for _, tc := range []struct {
		name        string
		devices     []omemolib.DeviceID
		local       omemolib.DeviceID
		rotatedFrom omemolib.DeviceID
		want        []omemolib.DeviceID
		wantHas     bool
	}{
		{
			name:    "all valid, local listed",
			devices: []omemolib.DeviceID{111, 222},
			local:   111,
			want:    []omemolib.DeviceID{111, 222},
			wantHas: true,
		},
		{
			name:    "local absent is reported, list untouched",
			devices: []omemolib.DeviceID{222},
			local:   111,
			want:    []omemolib.DeviceID{222},
			wantHas: false,
		},
		{
			// The whole point: one of our own other clients still advertises
			// an unaddressable ID, which would break every message we send
			// for every libsignal peer in the chat.
			name:    "out-of-range sibling device dropped",
			devices: []omemolib.DeviceID{111, legacyDeviceID, 222},
			local:   111,
			want:    []omemolib.DeviceID{111, 222},
			wantHas: true,
		},
		{
			name:        "rotated-away id dropped",
			devices:     []omemolib.DeviceID{legacyDeviceID, 222},
			local:       333,
			rotatedFrom: legacyDeviceID,
			want:        []omemolib.DeviceID{222},
			wantHas:     false,
		},
		{
			// Rotation failed but we still have to be reachable: publishing
			// a list that omits the device about to send is worse than
			// publishing one libsignal peers can't parse.
			name:    "out-of-range local kept",
			devices: []omemolib.DeviceID{legacyDeviceID, 222},
			local:   legacyDeviceID,
			want:    []omemolib.DeviceID{legacyDeviceID, 222},
			wantHas: true,
		},
		{
			name:    "zero id dropped",
			devices: []omemolib.DeviceID{0, 222},
			local:   222,
			want:    []omemolib.DeviceID{222},
			wantHas: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, hasLocal := pruneOwnDeviceList(tc.devices, tc.local, tc.rotatedFrom, omemolib.ProtocolV1, jid)
			if hasLocal != tc.wantHas {
				t.Errorf("hasLocal = %v, want %v", hasLocal, tc.wantHas)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("kept = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("kept = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// newLegacyIdentityManager builds a Manager over a real sqlite store whose
// stored device ID is out of range - the state every kage install created
// before device IDs were constrained. InitIdentity now refuses such an ID,
// so it's written straight to the store afterwards, which is exactly how
// those rows got there.
func newLegacyIdentityManager(t *testing.T, ctx context.Context, jid string, protocol omemolib.Protocol) (*omemolib.Manager, *kageomemo.Store) {
	t.Helper()

	_, q, err := storage.Open(filepath.Join(t.TempDir(), "omemo.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	store := kageomemo.NewStore(q, jid, protocol)
	if err := omemolib.InitIdentity(ctx, store, jid, 111, protocol); err != nil {
		t.Fatalf("InitIdentity: %v", err)
	}
	if err := store.SetLocalDevice(ctx, omemolib.Device{JID: jid, ID: legacyDeviceID}); err != nil {
		t.Fatalf("SetLocalDevice(legacy): %v", err)
	}
	mgr, err := omemolib.NewManager(ctx, store, nil, protocol,
		omemolib.WithTrustResolver(func(context.Context, omemolib.Device, []byte) error { return nil }))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr, store
}

func TestRotateOutOfRangeDeviceID(t *testing.T) {
	ctx := context.Background()
	const jid = "me@example.test"
	mgr, store := newLegacyIdentityManager(t, ctx, jid, omemolib.ProtocolV1)

	keyBefore, err := store.IdentityKeyPair(ctx)
	if err != nil {
		t.Fatalf("IdentityKeyPair before rotation: %v", err)
	}

	sess := &accountSession{account: config.Account{JID: jid}}
	rotatedFrom := rotateOutOfRangeDeviceID(ctx, sess, omemolib.ProtocolV1, mgr)
	if rotatedFrom != legacyDeviceID {
		t.Fatalf("rotatedFrom = %d, want %d", rotatedFrom, legacyDeviceID)
	}

	got := mgr.LocalDevice().ID
	if !got.Valid() {
		t.Fatalf("rotated to %d, which is still out of range", got)
	}
	// The store, not just the in-memory Manager: a rotation that doesn't
	// survive a restart would re-run on every connect, churning device IDs
	// and forcing peers to rebuild sessions each time.
	dev, err := store.LocalDevice(ctx)
	if err != nil {
		t.Fatalf("LocalDevice after rotation: %v", err)
	}
	if dev.ID != got {
		t.Errorf("store holds device %d, Manager holds %d", dev.ID, got)
	}

	keyAfter, err := store.IdentityKeyPair(ctx)
	if err != nil {
		t.Fatalf("IdentityKeyPair after rotation: %v", err)
	}
	if string(keyAfter) != string(keyBefore) {
		t.Error("rotation changed the identity key; every peer's verified fingerprint would be invalidated")
	}
}

func TestRotateOutOfRangeDeviceIDLeavesValidIDAlone(t *testing.T) {
	ctx := context.Background()
	const jid = "me@example.test"

	_, q, err := storage.Open(filepath.Join(t.TempDir(), "omemo.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	store := kageomemo.NewStore(q, jid, omemolib.ProtocolV1)
	if err := omemolib.InitIdentity(ctx, store, jid, 343125113, omemolib.ProtocolV1); err != nil {
		t.Fatalf("InitIdentity: %v", err)
	}
	mgr, err := omemolib.NewManager(ctx, store, nil, omemolib.ProtocolV1)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	sess := &accountSession{account: config.Account{JID: jid}}
	if rotatedFrom := rotateOutOfRangeDeviceID(ctx, sess, omemolib.ProtocolV1, mgr); rotatedFrom != 0 {
		t.Fatalf("rotatedFrom = %d, want 0 (nothing to rotate)", rotatedFrom)
	}
	if got := mgr.LocalDevice().ID; got != 343125113 {
		t.Errorf("device id changed to %d; a valid id must be left alone", got)
	}
}

// TestSetupOmemoGeneratesAddressableDeviceIDs guards the generator kage
// actually calls on first run, rather than only omemo-go's own helper:
// the original bug was entirely in how kage drew the number.
func TestSetupOmemoGeneratesAddressableDeviceIDs(t *testing.T) {
	ctx := context.Background()
	for i := range 64 {
		_, q, err := storage.Open(filepath.Join(t.TempDir(), "omemo.db"))
		if err != nil {
			t.Fatalf("open storage: %v", err)
		}
		store := kageomemo.NewStore(q, "me@example.test", omemolib.ProtocolV1)
		id, err := omemolib.GenerateDeviceID()
		if err != nil {
			t.Fatalf("GenerateDeviceID: %v", err)
		}
		if err := omemolib.InitIdentity(ctx, store, "me@example.test", id, omemolib.ProtocolV1); err != nil {
			t.Fatalf("iteration %d: InitIdentity(%d): %v", i, id, err)
		}
	}
}
