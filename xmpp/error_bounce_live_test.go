//go:build integration

package xmpp

import (
	"context"
	"testing"
	"time"
)

// TestErrorBouncesAreNotIncoming checks a message the server bounces back to
// us as an error (an unreachable or nonexistent recipient) isn't mistaken
// for that recipient talking to us. The bounce comes "from" them and can
// carry the original payload - our own <composing/>, reaction, body - which
// read as theirs showed a contact typing whenever we typed.
func TestErrorBouncesAreNotIncoming(t *testing.T) {
	ctx := context.Background()
	alice, err := Dial(ctx, "alice@localhost", "alicepw", devtestTLSConfig(t))
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}
	defer alice.Close()

	sent := map[string]bool{}
	for _, to := range []string{"nobody-here@localhost", "someone@unresolvable.invalid", "bob@localhost/no-such-resource"} {
		if err := alice.SendChatState(ctx, to, ChatStateComposing); err != nil {
			t.Fatalf("chat state to %s: %v", to, err)
		}
		id, err := alice.Send(ctx, to, "bounce me", SendOptions{})
		if err != nil {
			t.Fatalf("message to %s: %v", to, err)
		}
		sent[id] = true
	}

	bounced := 0
	deadline := time.After(4 * time.Second)
	for {
		select {
		case ev := <-alice.Events():
			switch e := ev.(type) {
			case ChatStateEvent:
				t.Fatalf("bounce read as a chat state from %s: %+v", e.From, e)
			case MessageEvent:
				if !e.Outgoing {
					t.Fatalf("bounce read as an incoming message from %s: %+v", e.From, e)
				}
			case MessageBouncedEvent:
				if sent[e.ID] {
					bounced++
				}
			}
		case <-deadline:
			// Not every target bounces on every server (a message to an
			// offline bare JID may be stored instead), but at least one of
			// these must come back as a bounce of the message we sent.
			if bounced == 0 {
				t.Fatal("no message came back as a MessageBouncedEvent")
			}
			return
		}
	}
}
