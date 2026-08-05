package xmpp

import (
	"bytes"
	"context"
	"testing"

	omemolib "github.com/jim-ww/omemo-go"
)

// TestEncodeDecodeOmemoMessageV1RoundTrip checks the wire encoding
// contract directly: no ik/ek/spkid/pkid attributes, just rid/prekey plus
// an opaque base64 blob (see omemo_v1.go's package doc for why - the
// blob is a self-describing PreKeyWhisperMessage/WhisperMessage produced
// upstream by internal/signal.Session.Encrypt).
func TestEncodeDecodeOmemoMessageV1RoundTrip(t *testing.T) {
	for _, isPreKey := range []bool{false, true} {
		msg := &omemolib.EncryptedMessage{
			Sender:  omemolib.Device{ID: 12321},
			Payload: []byte("payload bytes"),
			IV:      []byte("123456789012"),
			Keys: []omemolib.RecipientKey{
				{Device: 31415, Data: []byte("wrapped key bytes")},
			},
		}
		if isPreKey {
			msg.Keys[0].KeyExchange = &omemolib.KeyExchange{}
		}

		elem := EncodeOmemoMessageV1(msg)
		if elem.Header.Keys[0].PreKey != isPreKey {
			t.Fatalf("prekey=%v: encoded PreKey attribute = %v", isPreKey, elem.Header.Keys[0].PreKey)
		}

		got, err := DecodeOmemoMessageV1(elem, "alice@example.com")
		if err != nil {
			t.Fatalf("prekey=%v: DecodeOmemoMessageV1: %v", isPreKey, err)
		}
		if got.Sender.ID != msg.Sender.ID {
			t.Errorf("prekey=%v: sender ID = %d, want %d", isPreKey, got.Sender.ID, msg.Sender.ID)
		}
		if !bytes.Equal(got.Payload, msg.Payload) {
			t.Errorf("prekey=%v: payload mismatch", isPreKey)
		}
		if !bytes.Equal(got.IV, msg.IV) {
			t.Errorf("prekey=%v: iv mismatch", isPreKey)
		}
		if len(got.Keys) != 1 || !bytes.Equal(got.Keys[0].Data, msg.Keys[0].Data) {
			t.Fatalf("prekey=%v: key data mismatch: %+v", isPreKey, got.Keys)
		}
		if (got.Keys[0].KeyExchange != nil) != isPreKey {
			t.Errorf("prekey=%v: decoded KeyExchange non-nil = %v", isPreKey, got.Keys[0].KeyExchange != nil)
		}
	}
}

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
