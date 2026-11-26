//go:build integration

package xmpp

import (
	"context"
	"slices"
	"testing"
	"time"
)

// TestBlocklistRoundTrip blocks and unblocks a JID, checking the
// blocklist and the push each change brings to another of our streams.
func TestBlocklistRoundTrip(t *testing.T) {
	ctx := context.Background()
	tlsConfig := devtestTLSConfig(t)
	alice, err := Dial(ctx, "alice@localhost", "alicepw", tlsConfig)
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}
	defer alice.Close()
	other, err := Dial(ctx, "alice@localhost", "alicepw", tlsConfig)
	if err != nil {
		t.Fatalf("dial alice's second stream: %v", err)
	}
	defer other.Close()

	const peer = "blocklist-test@localhost"
	if _, err := alice.Blocklist(ctx); err != nil {
		t.Skipf("server without XEP-0191 (re-run devtest/prosody/setup.sh and restart serve.sh): %v", err)
	}
	// Subscribes the second stream to pushes.
	if _, err := other.Blocklist(ctx); err != nil {
		t.Fatal(err)
	}

	waitPush := func(blocked bool) {
		t.Helper()
		timeout := time.After(5 * time.Second)
		for {
			select {
			case ev := <-other.Events():
				if p, ok := ev.(BlocklistPushEvent); ok && p.Blocked == blocked && slices.Contains(p.JIDs, peer) {
					return
				}
			case <-timeout:
				t.Fatalf("no push of %s blocked=%t", peer, blocked)
			}
		}
	}

	if err := alice.Block(ctx, peer); err != nil {
		t.Fatalf("block: %v", err)
	}
	defer alice.Unblock(ctx, peer)
	waitPush(true)
	if jids, err := alice.Blocklist(ctx); err != nil || !slices.Contains(jids, peer) {
		t.Fatalf("blocklist after blocking: %v, %v", jids, err)
	}

	if err := alice.Unblock(ctx, peer); err != nil {
		t.Fatalf("unblock: %v", err)
	}
	waitPush(false)
	if jids, err := alice.Blocklist(ctx); err != nil || slices.Contains(jids, peer) {
		t.Fatalf("blocklist after unblocking: %v, %v", jids, err)
	}
}
