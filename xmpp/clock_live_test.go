//go:build integration

package xmpp

import (
	"context"
	"testing"
	"time"
)

// TestMeasureClockAgainstProsody checks the server's time reply is read: a
// server on this host shares our clock, so it measures no offset.
func TestMeasureClockAgainstProsody(t *testing.T) {
	ctx := context.Background()
	alice, err := Dial(ctx, "alice@localhost", "alicepw", devtestTLSConfig(t))
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}
	defer alice.Close()

	// Unlike Dial's, this asks after presence, which on a freshly started
	// server queues every stored PEP item ahead of the answer.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := alice.measureClock(ctx); err != nil {
		t.Fatalf("reading server time: %v", err)
	}
	if off := time.Duration(alice.clockOffset.Load()); off != 0 {
		t.Errorf("offset %v against a server on this host, want 0", off)
	}
}
