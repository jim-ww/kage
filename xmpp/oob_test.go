//go:build integration

package xmpp

import (
	"context"
	"testing"
	"time"
)

// TestOOBURLsRoundTrip verifies SendOptions.OOBURLs is written as XEP-0066
// <x xmlns='jabber:x:oob'> elements and parsed back into
// MessageEvent.OOBURLs on the receiving side.
func TestOOBURLsRoundTrip(t *testing.T) {
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

	urls := []string{"https://example.com/a.jpg", "https://example.com/b.png"}
	body := urls[0] + "\n" + urls[1]
	id, err := alice.Send(ctx, "bob@localhost", body, SendOptions{OOBURLs: urls})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	me := waitForMessage(t, bob, id, 5*time.Second)
	if len(me.OOBURLs) != len(urls) || me.OOBURLs[0] != urls[0] || me.OOBURLs[1] != urls[1] {
		t.Fatalf("OOBURLs = %v, want %v", me.OOBURLs, urls)
	}
}
