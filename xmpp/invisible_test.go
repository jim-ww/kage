//go:build integration

package xmpp

import (
	"context"
	"testing"
	"time"
)

// TestInvisibleSupportedNoError verifies InvisibleSupported completes a
// disco#info round trip against a real server without erroring, whatever the
// server's actual answer is - the caller-facing contract is "false and no
// hang" on an unsupporting server, not any particular boolean.
func TestInvisibleSupportedNoError(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	alice, err := Dial(ctx, "alice@localhost", "alicepw", tlsConfig)
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}
	defer alice.Close()

	done := make(chan bool, 1)
	go func() { done <- alice.InvisibleSupported(ctx) }()

	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("InvisibleSupported did not return in time")
	}
}

// TestInvisibleSupportedCaches verifies the second call reuses the cached
// result instead of issuing another disco#info round trip - same contract as
// xmpp.Client.uploadService's cache (see upload_test.go's spirit, though that
// file doesn't cover its cache directly). We can't observe the wire
// directly here, so this just checks both calls agree and complete quickly,
// which would fail if the cache were bypassed and the second call had to wait
// out a slow/timing-out disco query.
func TestInvisibleSupportedCaches(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	alice, err := Dial(ctx, "alice@localhost", "alicepw", tlsConfig)
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}
	defer alice.Close()

	first := alice.InvisibleSupported(ctx)

	fastCtx, fastCancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer fastCancel()
	second := alice.InvisibleSupported(fastCtx)

	if first != second {
		t.Errorf("InvisibleSupported: first call = %v, cached call = %v, want equal", first, second)
	}
}

// TestSetInvisibleSendsWithoutError verifies SetInvisible's presence stanza
// round-trips through the server without a stream error - regardless of
// whether the server actually implements XEP-0186 (the UI is expected to
// gate on InvisibleSupported before ever calling this), an unrecognized
// presence type value must not break the connection.
func TestSetInvisibleSendsWithoutError(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	alice, err := Dial(ctx, "alice@localhost", "alicepw", tlsConfig)
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}
	defer alice.Close()

	if err := alice.SetInvisible(ctx); err != nil {
		t.Fatalf("SetInvisible: %v", err)
	}

	// The connection must survive whatever the server made of that presence
	// type - confirmed by successfully sending plain presence right after.
	if err := alice.SetPresence(ctx, ""); err != nil {
		t.Fatalf("SetPresence after SetInvisible: %v", err)
	}
}
