package xmpp

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestCarbonsDeliverToSecondResource reproduces the exact bug this file's
// carbon support fixes: with two resources of the same account online at
// equal priority (e.g. two kage instances, or another client), RFC 6121 §8
// doesn't require the server to deliver a bare-JID-addressed message to
// both - most servers pick just one (whichever they consider "most active"),
// so the second resource saw nothing at all despite being fully connected.
// XEP-0280 carbons (enabled in Dial) is what makes the second resource see
// the message anyway, via a <forwarded/> copy.
func TestCarbonsDeliverToSecondResource(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()

	// alice1 is the "primary" resource - analogous to the TUI.
	alice1, err := Dial(ctx, "alice@localhost", "alicepw", tlsConfig)
	if err != nil {
		t.Fatalf("dial alice1: %v", err)
	}
	defer alice1.Close()

	// alice2 is a second simultaneous resource for the same bare JID. It
	// never sends or receives directly; it should only ever see bob's
	// message as a carbon copy.
	alice2, err := Dial(ctx, "alice@localhost", "alicepw", tlsConfig)
	if err != nil {
		t.Fatalf("dial alice2: %v", err)
	}
	defer alice2.Close()

	bob, err := Dial(ctx, "bob@localhost", "bobpw", tlsConfig)
	if err != nil {
		t.Fatalf("dial bob: %v", err)
	}
	defer bob.Close()

	const body = "carbons regression check"
	if _, err := bob.Send(ctx, "alice@localhost", body, SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	ev := waitForMessage(t, alice2, 5*time.Second)
	if ev.Body != body {
		t.Fatalf("alice2 (second resource) got body %q, want %q", ev.Body, body)
	}
	from, _, _ := strings.Cut(ev.From, "/")
	if from != "bob@localhost" {
		t.Fatalf("alice2 got From %q, want bob@localhost", ev.From)
	}
}

// waitForMessage reads c's Events channel until a MessageEvent with a
// non-empty Body shows up (other event types, and our own presence/caps
// churn, are ignored) or the timeout elapses.
func waitForMessage(t *testing.T, c *Client, timeout time.Duration) MessageEvent {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-c.Events():
			if !ok {
				t.Fatal("events channel closed before a MessageEvent arrived")
			}
			if me, ok := ev.(MessageEvent); ok && me.Body != "" {
				return me
			}
		case <-deadline:
			t.Fatal("timed out waiting for a MessageEvent")
		}
	}
}
