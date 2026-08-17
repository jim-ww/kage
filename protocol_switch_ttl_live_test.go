//go:build integration

package main

import (
	"context"
	"testing"
	"time"

	"github.com/jim-ww/kage/storage"
	omemolib "github.com/jim-ww/omemo-go"
)

// TestOmemoProtocolStaysPinnedUntilTTLExpiry is a live-server regression
// test for resolveOmemoProtocol (crypto_helpers.go): a peer's v1-vs-v2
// negotiation result is cached in storage for omemoPeerProtocolTTL (7 days)
// rather than re-probed on every send. The design intent (per that
// function's doc comment) is that within the TTL, the cached result wins
// even if the peer's actual published protocols have since changed - so a
// chat doesn't flip-flop protocols mid-conversation just because a probe
// raced a peer's own setup - and only a re-probe after the TTL expires can
// change it. Neither half had a test proving it actually behaves that way.
//
// bob (a real live peer, set up via newOmemoTestSession like every other
// test in this package - which unconditionally publishes both v1 and v2)
// stands in for "whatever bob currently publishes"; the cache is seeded
// directly to isolate the two behaviors under test from bob's real-time
// device-list state, which every other live test in this package also
// mutates against the same shared account.
func TestOmemoProtocolStaysPinnedUntilTTLExpiry(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()

	alice := newOmemoTestSession(ctx, t, "alice@localhost", "alicepw", tlsConfig)
	newOmemoTestSession(ctx, t, "bob@localhost", "bobpw", tlsConfig) // publishes both v1 and v2 for bob@localhost

	// Seed a fresh "already resolved to v1" cache entry, as if an earlier
	// probe had run moments ago - regardless of what bob's account
	// currently actually publishes.
	if err := alice.db.SetOmemoPeerProtocol(ctx, storage.SetOmemoPeerProtocolParams{
		AccountJid: "alice@localhost",
		PeerJid:    "bob@localhost",
		Protocol:   "v1",
		ProbedAt:   time.Now().Unix(),
	}); err != nil {
		t.Fatalf("seeding fresh cached protocol negotiation: %v", err)
	}

	protocol := resolveOmemoProtocol(ctx, alice, "bob@localhost")
	if protocol != omemolib.ProtocolV1 {
		t.Fatalf("protocol with a fresh cached v1 entry = %v, want ProtocolV1 (must not re-probe within the TTL)", protocol)
	}

	// Backdate that same entry past the TTL, simulating that much time
	// actually passing, and confirm it now re-probes bob's real (both v1
	// and v2 published) state and prefers v2.
	if err := alice.db.SetOmemoPeerProtocol(ctx, storage.SetOmemoPeerProtocolParams{
		AccountJid: "alice@localhost",
		PeerJid:    "bob@localhost",
		Protocol:   "v1",
		ProbedAt:   time.Now().Add(-(omemoPeerProtocolTTL + time.Hour)).Unix(),
	}); err != nil {
		t.Fatalf("backdating cached protocol negotiation: %v", err)
	}

	protocol = resolveOmemoProtocol(ctx, alice, "bob@localhost")
	if protocol != omemolib.ProtocolV2 {
		t.Fatalf("protocol after TTL expiry = %v, want ProtocolV2 (bob publishes both, v2 preferred)", protocol)
	}
}
