//go:build integration

package xmpp

import (
	"context"
	"testing"
	"time"
)

// TestMessageDeliveryReceipt verifies XEP-0184 round-trips over a real
// connection: alice sends bob a chat message (which requests a receipt),
// bob's client auto-acknowledges it, and alice's Events channel receives a
// MessageDeliveredEvent for that message's ID.
func TestMessageDeliveryReceipt(t *testing.T) {
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

	id, err := alice.Send(ctx, "bob@localhost", "hi bob", SendOptions{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	waitForMessageDelivered(t, alice, id, 5*time.Second)
}

// waitForMessageDelivered reads c's Events channel until the receipt for
// the message sent with id shows up, or the timeout elapses. Anything else
// is passed over, receipts for other messages included: devtest's server
// hands an account those stored while it was offline, earlier tests' too.
func waitForMessageDelivered(t *testing.T, c *Client, id string, timeout time.Duration) MessageDeliveredEvent {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-c.Events():
			if !ok {
				t.Fatal("events channel closed before a MessageDeliveredEvent arrived")
			}
			if d, ok := ev.(MessageDeliveredEvent); ok && d.ID == id {
				return d
			}
		case <-deadline:
			t.Fatal("timed out waiting for a MessageDeliveredEvent")
		}
	}
}
