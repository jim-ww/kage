package xmpp

import (
	"bytes"
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

// TestOmemoBundleV1RoundTrip lives in omemo_v1_live_test.go (build tag
// integration) - it needs a real Prosody instance.
