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

	// Not just to warm the cache: RFC 6121 §2.2 makes a resource
	// "interested" - eligible for roster pushes at all - only once it has
	// asked for the roster, and a stream that never asks receives none for
	// its whole life. connectAccountLive does this as part of building the
	// chat list, so this mirrors production rather than adding to it.
	refreshRoster(ctx, nil, 0, sess, client)
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

// resetSubscription removes the roster item for peer on both sides and waits
// until the server itself agrees it is gone.
//
// The devtest accounts are long-lived, so each run inherits whatever
// subscription state the last one left behind - and a run that starts with
// the two already subscribed never produces a subscription request at all,
// so the test would pass without exercising anything. Confirming the removal
// against the server (by re-fetching the roster, not by polling the local
// cache, which a removal push hasn't necessarily reached yet) is what makes
// the starting state deterministic: a teardown still in flight when the next
// request goes out leaves the server reconciling two overlapping changes.
func resetSubscription(ctx context.Context, t *testing.T, sess *accountSession, peer string) {
	t.Helper()
	a := &adapter{sessions: []*accountSession{sess}}
	if msg := a.RemoveContact(0, peer); msg.(ui.ContactRemovedMsg).Err != nil {
		t.Fatalf("%s RemoveContact(%s): %v", sess.account.JID, peer, msg.(ui.ContactRemovedMsg).Err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		refreshRoster(ctx, nil, 0, sess, sess.client.Load())
		if _, ok := derefRoster(sess.roster.Load())[peer]; !ok {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("%s is still in %s's roster on the server; the test cannot start from a known state", peer, sess.account.JID)
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

	const aliceJID, bobJID = "alice@localhost", "bob@localhost"
	alice := newLiveSession(ctx, t, aliceJID, "alicepw", tlsConfig)
	bob := newLiveSession(ctx, t, bobJID, "bobpw", tlsConfig)

	resetSubscription(ctx, t, alice, bobJID)
	resetSubscription(ctx, t, bob, aliceJID)

	// bob adds alice: roster set plus <presence type="subscribe">. From here
	// on everything is the production event path on both sides.
	bobAdapter := &adapter{sessions: []*accountSession{bob}}
	if msg := bobAdapter.AddContact(0, aliceJID); msg.(ui.ContactAddedMsg).Err != nil {
		t.Fatalf("bob AddContact(alice): %v", msg.(ui.ContactAddedMsg).Err)
	}

	// "from" is the lopsided state the whole fix is about - bob can see
	// alice, alice can't see bob - so reaching at least that proves the
	// inbound request was approved and the resulting roster push applied.
	if got := waitForSubs(t, alice, bobJID, 20*time.Second, "from", "both"); got != "from" && got != "both" {
		t.Fatalf("alice's subscription to bob settled at %q; %s", got, diagnoseSubs(got))
	}

	// Converging to "both" is what makes bob's presence visible. The
	// immediate reciprocation usually gets there on its own, but it is one
	// stanza sent while the server is reconciling several subscription
	// changes at once - so the guarantee is the idempotent retry on every
	// roster refresh (healLopsidedSubscriptions), which is what a reconnect
	// would do. Drive that rather than waiting on a single attempt.
	for range 5 {
		if derefRoster(alice.roster.Load())[bobJID].Subs == "both" {
			break
		}
		refreshRoster(ctx, nil, 0, alice, alice.client.Load())
		time.Sleep(200 * time.Millisecond)
	}
	if got := waitForSubs(t, alice, bobJID, 20*time.Second, "both"); got != "both" {
		t.Fatalf("alice's subscription to bob settled at %q, want \"both\"; %s", got, diagnoseSubs(got))
	}
	if got := waitForSubs(t, bob, aliceJID, 20*time.Second, "both"); got != "both" {
		t.Errorf("bob's subscription to alice settled at %q, want \"both\"", got)
	}

	// The roster push must also have reached storage, or the chat is gone
	// again after a restart.
	rows, err := alice.db.ListRoster(ctx, aliceJID)
	if err != nil {
		t.Fatalf("alice ListRoster: %v", err)
	}
	var stored bool
	for _, r := range rows {
		if r.Jid == bobJID {
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
