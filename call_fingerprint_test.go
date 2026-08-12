package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jim-ww/kage/storage"
)

func TestComputeSASSymmetric(t *testing.T) {
	a := "AB:CD:EF:01:02:03"
	b := "11:22:33:44:55:66"
	if computeSAS(a, b) != computeSAS(b, a) {
		t.Fatal("SAS must be the same regardless of which side is local vs remote")
	}
}

func TestComputeSASDifferentForDifferentFingerprints(t *testing.T) {
	a := "AB:CD:EF:01:02:03"
	b := "11:22:33:44:55:66"
	c := "99:88:77:66:55:44"
	if computeSAS(a, b) == computeSAS(a, c) {
		t.Fatal("SAS should differ when either fingerprint changes (that's the whole point)")
	}
}

func TestNormalizeFingerprint(t *testing.T) {
	if got, want := normalizeFingerprint("ab:cd:ef"), "ABCDEF"; got != want {
		t.Fatalf("normalizeFingerprint() = %q, want %q", got, want)
	}
}

func TestCheckAndPinCallFingerprintTOFU(t *testing.T) {
	_, db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	ctx := context.Background()
	const account, peer = "me@example.com", "alice@example.com"

	if changed := checkAndPinCallFingerprint(ctx, db, account, peer, "AA:BB:CC"); changed {
		t.Fatal("first call with a contact must not report a mismatch")
	}
	if changed := checkAndPinCallFingerprint(ctx, db, account, peer, "AA:BB:CC"); changed {
		t.Fatal("same fingerprint on a later call must not report a mismatch")
	}
	if changed := checkAndPinCallFingerprint(ctx, db, account, peer, "DD:EE:FF"); !changed {
		t.Fatal("a fingerprint that differs from the pinned one must be reported as changed")
	}
	// The pin is updated to the new value, so it doesn't nag forever.
	if changed := checkAndPinCallFingerprint(ctx, db, account, peer, "DD:EE:FF"); changed {
		t.Fatal("the new fingerprint must be pinned, not just flagged once")
	}

	got, err := db.GetCallPeerFingerprint(ctx, storage.GetCallPeerFingerprintParams{AccountJid: account, Jid: peer})
	if err != nil {
		t.Fatalf("GetCallPeerFingerprint: %v", err)
	}
	if got != "DDEEFF" {
		t.Fatalf("pinned fingerprint = %q, want %q", got, "DDEEFF")
	}
}
