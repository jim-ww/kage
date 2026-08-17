//go:build integration

package xmpp

import (
	"context"
	"testing"
	"time"
)

// TestReplyToIDSurvivesEncryptedMessage verifies the <reply/> element (sent
// unencrypted alongside the OMEMO payload, see Send's opts.Encrypted case)
// is still parsed into MessageEvent.ReplyToID for an encrypted message.
// Previously handleStanza's msg.Encrypted != nil branch never looked at
// msg.Reply at all, so an OMEMO reply's original could never be resolved
// locally even after the reply body itself decrypted fine.
func TestReplyToIDSurvivesEncryptedMessage(t *testing.T) {
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

	// A real OMEMO payload isn't needed here - handleStanza's parsing of
	// <reply/> happens before any decryption is attempted, so an empty
	// envelope is enough to exercise the code path under test.
	if _, err := alice.Send(ctx, "bob@localhost", "", SendOptions{
		Encrypted: &OmemoEncryptedElem{},
		ReplyToID: "original-msg-id",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-bob.Events():
			if !ok {
				t.Fatal("events channel closed before a MessageEvent arrived")
			}
			if me, ok := ev.(MessageEvent); ok {
				if me.Encrypted == nil {
					t.Fatal("MessageEvent.Encrypted is nil, want the OMEMO envelope")
				}
				if me.ReplyToID != "original-msg-id" {
					t.Fatalf("ReplyToID = %q, want %q", me.ReplyToID, "original-msg-id")
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for a MessageEvent")
		}
	}
}
