package xmpp

import (
	"context"
	"testing"
	"time"
)

// TestReplaceIDSurvivesEncryptedMessage verifies the <replace/> element (sent
// unencrypted alongside the OMEMO payload, same as <reply/>, see Send's
// opts.Encrypted case) is still parsed into MessageEvent.ReplaceID for an
// encrypted message. Previously handleStanza's msg.Encrypted != nil branch
// never looked at msg.Replace at all, so an OMEMO edit's ReplaceID was lost
// on the live path and got stored as a brand-new message instead of
// correcting the original (see events.go's handleIncomingMessage).
func TestReplaceIDSurvivesEncryptedMessage(t *testing.T) {
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
	// <replace/> happens before any decryption is attempted, so an empty
	// envelope is enough to exercise the code path under test.
	if _, err := alice.Send(ctx, "bob@localhost", "", SendOptions{
		Encrypted: &OmemoEncryptedElem{},
		ReplaceID: "original-msg-id",
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
				if me.ReplaceID != "original-msg-id" {
					t.Fatalf("ReplaceID = %q, want %q", me.ReplaceID, "original-msg-id")
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for a MessageEvent")
		}
	}
}
