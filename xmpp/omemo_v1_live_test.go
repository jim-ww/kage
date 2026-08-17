//go:build integration

package xmpp

import (
	"bytes"
	"context"
	"testing"

	omemolib "github.com/jim-ww/omemo-go"
)

// TestOmemoBundleV1RoundTrip verifies against a real Prosody instance that
// publishing and fetching a legacy bundle preserves the raw 32-byte keys
// omemolib deals in, through the wire's 33-byte DJB-type-prefixed encoding
// (see serializeLegacyKey/parseLegacyKey).
func TestOmemoBundleV1RoundTrip(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()

	alice, err := Dial(ctx, "alice@localhost", "alicepw", tlsConfig)
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}
	defer alice.Close()
	bob, err := Dial(ctx, "bob@localhost", "bobpw", tlsConfig)
	if err != nil {
		t.Fatalf("dial bob: %v", err)
	}
	defer bob.Close()

	dev := omemolib.Device{JID: "alice@localhost", ID: 42}
	want := omemolib.Bundle{
		Device:      dev,
		IdentityKey: bytes.Repeat([]byte{0x11}, 32),
		SignedPreKey: omemolib.SignedPreKey{
			ID:        7,
			Public:    bytes.Repeat([]byte{0x22}, 32),
			Signature: bytes.Repeat([]byte{0x33}, 64),
		},
		PreKeys: []omemolib.PreKey{
			{ID: 1, Public: bytes.Repeat([]byte{0x44}, 32)},
			{ID: 2, Public: bytes.Repeat([]byte{0x55}, 32)},
		},
	}

	if err := alice.PublishOmemoBundleV1(ctx, want); err != nil {
		t.Fatalf("PublishOmemoBundleV1: %v", err)
	}

	got, err := bob.FetchOmemoBundleV1(ctx, dev)
	if err != nil {
		t.Fatalf("FetchOmemoBundleV1: %v", err)
	}

	if !bytes.Equal(got.IdentityKey, want.IdentityKey) {
		t.Errorf("IdentityKey = %x, want %x", got.IdentityKey, want.IdentityKey)
	}
	if got.SignedPreKey.ID != want.SignedPreKey.ID ||
		!bytes.Equal(got.SignedPreKey.Public, want.SignedPreKey.Public) ||
		!bytes.Equal(got.SignedPreKey.Signature, want.SignedPreKey.Signature) {
		t.Errorf("SignedPreKey = %+v, want %+v", got.SignedPreKey, want.SignedPreKey)
	}
	if len(got.PreKeys) != len(want.PreKeys) {
		t.Fatalf("got %d prekeys, want %d", len(got.PreKeys), len(want.PreKeys))
	}
	for i, pk := range want.PreKeys {
		if got.PreKeys[i].ID != pk.ID || !bytes.Equal(got.PreKeys[i].Public, pk.Public) {
			t.Errorf("prekey %d = %+v, want %+v", i, got.PreKeys[i], pk)
		}
	}
}
