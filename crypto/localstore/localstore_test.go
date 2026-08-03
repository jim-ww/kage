package localstore

import "testing"

func testKey(t *testing.T) []byte {
	t.Helper()
	salt, err := NewSalt()
	if err != nil {
		t.Fatal(err)
	}
	return DeriveKey("test password", salt)
}

func TestSealOpenRoundTrip(t *testing.T) {
	key := testKey(t)
	const plaintext = "Wherefore art thou, Romeo?"

	sealed, err := Seal(key, plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if sealed == plaintext {
		t.Fatal("sealed blob equals plaintext")
	}

	got, err := Open(key, sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got != plaintext {
		t.Fatalf("got %q, want %q", got, plaintext)
	}
}

func TestOpenWrongKeyFails(t *testing.T) {
	key1 := testKey(t)
	key2 := testKey(t)

	sealed, err := Seal(key1, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(key2, sealed); err == nil {
		t.Fatal("expected Open with the wrong key to fail")
	}
}

func TestOpenTamperedFails(t *testing.T) {
	key := testKey(t)
	sealed, err := Seal(key, "secret")
	if err != nil {
		t.Fatal(err)
	}
	tampered := []byte(sealed)
	tampered[len(tampered)-1] ^= 1
	if _, err := Open(key, string(tampered)); err == nil {
		t.Fatal("expected Open on tampered ciphertext to fail")
	}
}

func TestDeriveKeySameInputsSameKey(t *testing.T) {
	salt, err := NewSalt()
	if err != nil {
		t.Fatal(err)
	}
	k1 := DeriveKey("hunter2", salt)
	k2 := DeriveKey("hunter2", salt)
	if string(k1) != string(k2) {
		t.Fatal("DeriveKey isn't deterministic for the same password+salt")
	}

	k3 := DeriveKey("different", salt)
	if string(k1) == string(k3) {
		t.Fatal("DeriveKey produced the same key for different passwords")
	}
}
