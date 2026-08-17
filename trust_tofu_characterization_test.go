package main

import (
	"context"
	"path/filepath"
	"testing"

	kageomemo "github.com/jim-ww/kage/crypto/omemo"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/xmpp"
	omemolib "github.com/jim-ww/omemo-go"
)

// TestTrustIsSilentlyAcceptedOnIdentityKeyChange is a characterization test,
// not a bug-fix regression test: kage's OMEMO trust policy is TOFU with
// every device auto-trusted forever (crypto_helpers.go's
// omemolib.WithTrustResolver always returns nil - see setupOmemoProtocol),
// and there is no UI path that ever calls TrustUntrusted (ui/omemo_devices.go
// has no trust-state mutation at all). This pins down exactly what that
// means in practice: if the identity key behind an already-trusted device ID
// changes - e.g. the account was compromised and an attacker republishes a
// forged bundle under the same device ID kage already trusts - kage
// re-establishes a session with the new key and keeps encrypting to it with
// zero warning, log line, or user-visible signal.
//
// This exists so that if trust verification (fingerprint comparison, a
// "device changed" warning, etc.) is ever added, there's a test that must
// be deliberately changed rather than one that silently starts failing -
// today's answer is "yes, silently accepted", and that needs to stay a
// conscious decision.
func TestTrustIsSilentlyAcceptedOnIdentityKeyChange(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()

	alice := newOmemoTestSession(ctx, t, "alice@localhost", "alicepw", tlsConfig)
	bobReal := newOmemoTestSession(ctx, t, "bob@localhost", "bobpw", tlsConfig)
	bobDevice := bobReal.omemoMgrV1.LocalDevice()

	if err := alice.omemoMgrV1.SyncDevices(ctx, "bob@localhost"); err != nil {
		t.Fatalf("alice initial SyncDevices(bob): %v", err)
	}
	msg1, _, err := alice.omemoMgrV1.EncryptMessage(ctx, "bob@localhost", []byte("before key swap"))
	if err != nil {
		t.Fatalf("alice encrypt (before key swap): %v", err)
	}
	if _, err := bobReal.omemoMgrV1.DecryptMessage(ctx, msg1); err != nil {
		t.Fatalf("bob (real key) decrypt (before key swap): %v", err)
	}

	// An "attacker" who has compromised bob's account logs in as bob@localhost
	// and republishes a forged bundle under bob's *same* device ID, with a
	// brand new identity key bobReal never generated. A real attacker doing
	// this wouldn't need bob's actual client - just his account credentials,
	// which is exactly the threat TOFU-without-verification can't catch.
	attackerClient, err := xmpp.Dial(ctx, "bob@localhost", "bobpw", tlsConfig)
	if err != nil {
		t.Fatalf("dial attacker (as bob@localhost): %v", err)
	}
	t.Cleanup(func() { attackerClient.Close() })
	_, attackerDB, err := storage.Open(filepath.Join(t.TempDir(), "attacker.db"))
	if err != nil {
		t.Fatalf("open attacker storage: %v", err)
	}
	attackerStore := kageomemo.NewStore(attackerDB, "bob@localhost", omemolib.ProtocolV1)
	if err := omemolib.InitIdentity(ctx, attackerStore, "bob@localhost", bobDevice.ID, omemolib.ProtocolV1); err != nil {
		t.Fatalf("forging attacker identity for bob's device %d: %v", bobDevice.ID, err)
	}
	attackerMgr, err := omemolib.NewManager(ctx, attackerStore, attackerClient.OmemoTransportV1(), omemolib.ProtocolV1,
		omemolib.WithTrustResolver(func(context.Context, omemolib.Device, []byte) error { return nil }))
	if err != nil {
		t.Fatalf("attacker NewManager: %v", err)
	}
	if err := attackerMgr.PublishBundle(ctx); err != nil {
		t.Fatalf("attacker publishing forged bundle over bob's real device %d: %v", bobDevice.ID, err)
	}

	// Force alice to rebuild her session with bob's device from scratch, the
	// same as any legitimate session-repair path (healBrokenSession) would -
	// this is what makes her fetch the (now forged) bundle again instead of
	// reusing the still-valid old session.
	if err := alice.omemoMgrV1.ResetSession(ctx, bobDevice); err != nil {
		t.Fatalf("alice ResetSession(bob): %v", err)
	}

	msg2, _, err := alice.omemoMgrV1.EncryptMessage(ctx, "bob@localhost", []byte("after key swap"))
	if err != nil {
		// This is the behavior actually under test: today, this must NOT
		// error - there is no trust check capable of rejecting an unknown
		// new identity key for an already-known device ID.
		t.Fatalf("alice encrypt (after key swap) unexpectedly failed - trust policy caught the swap: %v", err)
	}

	if _, err := attackerMgr.DecryptMessage(ctx, msg2); err != nil {
		t.Fatalf("attacker (forged key) failed to decrypt alice's message - expected the swap to have succeeded: %v", err)
	}
	if _, err := bobReal.omemoMgrV1.DecryptMessage(ctx, msg2); err == nil {
		t.Fatal("bob (real, original key) unexpectedly decrypted alice's post-swap message - it should now be going to the attacker's key instead")
	}
}
