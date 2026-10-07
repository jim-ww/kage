//go:build integration

package main

import (
	"context"
	"crypto/tls"
	"path/filepath"
	"testing"
	"time"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/xmpp"
)

// newLiveSession dials jid against the local Prosody test instance and runs
// its event loop, so the production dispatchEvent path (not a hand-written
// substitute) is what reacts to presence, subscription requests and roster
// pushes.
func newLiveSession(ctx context.Context, t *testing.T, jid, pass string, tlsConfig *tls.Config) *accountSession {
	t.Helper()

	client, err := xmpp.Dial(ctx, jid, pass, tlsConfig)
	if err != nil {
		t.Fatalf("dial %s: %v", jid, err)
	}
	t.Cleanup(func() { client.Close() })

	_, q, err := storage.Open(filepath.Join(t.TempDir(), "sub-roster.db"))
	if err != nil {
		t.Fatalf("open storage for %s: %v", jid, err)
	}

	sess := &accountSession{
		account:   config.Account{JID: jid, Password: pass},
		db:        q,
		tlsConfig: tlsConfig,
	}
	sess.client.Store(client)
	sess.roster.Store(&map[string]rosterEntry{})

	listenCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go listen(listenCtx, nil, 0, sess)
	return sess
}

// waitForSubs polls sess's cached roster entry for peer until its
// subscription state is one of want, or the deadline passes. Polling rather
// than waiting on an event: the state we care about is the one the
// production code settled on, whichever stanza got it there.
func waitForSubs(t *testing.T, sess *accountSession, peer string, d time.Duration, want ...string) string {
	t.Helper()
	deadline := time.Now().Add(d)
	var last string
	for time.Now().Before(deadline) {
		last = derefRoster(sess.roster.Load())[peer].Subs
		for _, w := range want {
			if last == w {
				return last
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return last
}

// TestSubscriptionIsReciprocatedAndRosterPushApplied is the end-to-end
// regression for the two halves of "the peer's presence was never
// subscribed", against a real server.
//
// Approving an inbound request only sends <presence type="subscribed">,
// which grants the peer our presence and establishes nothing in the other
// direction - the subscription settles at "from" and we never see whether
// they are online. And with no roster-push handling, even a subscription
// that did complete stayed invisible until the next full roster fetch at
// connect time, which is why restarting the daemon appeared to fix it.
//
// The assertion is deliberately on subscription="both": reaching it requires
// alice to have sent her own request back (reciprocation) AND to have
// learned the final state from a push, with alice never reconnecting.
func TestSubscriptionIsReciprocatedAndRosterPushApplied(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()

	alice := newLiveSession(ctx, t, "alice@localhost", "alicepw", tlsConfig)
	bob := newLiveSession(ctx, t, "bob@localhost", "bobpw", tlsConfig)

	// The devtest accounts are long-lived and may already know each other,
	// which would make the whole test vacuous. Tear both roster items down
	// and wait for the pushes that confirm it.
	aliceAdapter := &adapter{sessions: []*accountSession{alice}}
	bobAdapter := &adapter{sessions: []*accountSession{bob}}
	if msg := aliceAdapter.RemoveContact(0, "bob@localhost"); msg.(ui.ContactRemovedMsg).Err != nil {
		t.Fatalf("alice RemoveContact(bob): %v", msg.(ui.ContactRemovedMsg).Err)
	}
	if msg := bobAdapter.RemoveContact(0, "alice@localhost"); msg.(ui.ContactRemovedMsg).Err != nil {
		t.Fatalf("bob RemoveContact(alice): %v", msg.(ui.ContactRemovedMsg).Err)
	}
	time.Sleep(time.Second)

	// bob adds alice: roster set plus <presence type="subscribe">. From here
	// on everything is the production event path on both sides.
	if msg := bobAdapter.AddContact(0, "alice@localhost"); msg.(ui.ContactAddedMsg).Err != nil {
		t.Fatalf("bob AddContact(alice): %v", msg.(ui.ContactAddedMsg).Err)
	}

	if got := waitForSubs(t, alice, "bob@localhost", 20*time.Second, "both"); got != "both" {
		t.Fatalf("alice's subscription to bob settled at %q, want \"both\"; %s", got, diagnoseSubs(got))
	}
	if got := waitForSubs(t, bob, "alice@localhost", 20*time.Second, "both"); got != "both" {
		t.Errorf("bob's subscription to alice settled at %q, want \"both\"", got)
	}

	// The roster push must also have reached storage, or the chat is gone
	// again after a restart.
	rows, err := alice.db.ListRoster(ctx, "alice@localhost")
	if err != nil {
		t.Fatalf("alice ListRoster: %v", err)
	}
	var stored bool
	for _, r := range rows {
		if r.Jid == "bob@localhost" {
			stored = true
			if r.Subs != "both" {
				t.Errorf("stored subscription for bob = %q, want both", r.Subs)
			}
		}
	}
	if !stored {
		t.Error("roster push was applied in memory but never persisted; the chat would vanish on restart")
	}
}

// diagnoseSubs names which half of the fix a failure points at, so a red
// test says what broke rather than just what it wanted.
func diagnoseSubs(got string) string {
	switch got {
	case "from":
		return "\"from\" means the inbound request was approved but no request was sent back (reciprocation)"
	case "":
		return "an empty state means no roster push was applied at all"
	default:
		return "the subscription did not complete in both directions"
	}
}
