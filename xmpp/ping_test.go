//go:build integration

package xmpp

import (
	"context"
	"testing"
	"time"
)

// TestPingRoundTrip verifies Client.Ping actually gets a response from a
// real server - the round trip account.go's confirmPendingAcks relies on to
// tell "the local write succeeded" apart from "the server actually got it".
func TestPingRoundTrip(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()

	alice, err := Dial(ctx, "alice@localhost", "alicepw", tlsConfig)
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}
	defer alice.Close()

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := alice.Ping(pingCtx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

// TestPingFailsAfterClose verifies Ping actually reports a failure on a
// dead connection instead of hanging or falsely succeeding - the exact case
// that a bare client.Send() returning nil can't distinguish from a real
// delivery (see Client.Ping's doc comment).
func TestPingFailsAfterClose(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()

	alice, err := Dial(ctx, "alice@localhost", "alicepw", tlsConfig)
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}
	alice.Close()

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := alice.Ping(pingCtx); err == nil {
		t.Fatal("Ping on a closed connection succeeded, want an error")
	}
}
