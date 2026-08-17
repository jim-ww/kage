package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jim-ww/kage/call"
	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/xmpp"
	"github.com/pion/webrtc/v4"
)

// skipIfNoCallNetwork skips the test when ICE gathering fails because the
// sandbox has no usable network stack to enumerate - see
// call/loopback_test.go's skipIfNoNetwork, same root cause (some sandboxes
// block AF_NETLINK entirely: "route ip+net: netlinkrib: operation not
// supported"), just probed here without a live PeerConnection since this
// package's callSession, not call.PeerConnection, is what's under test.
func skipIfNoCallNetwork(t *testing.T) {
	t.Helper()
	pc, err := call.NewPeerConnection()
	if err != nil {
		if strings.Contains(err.Error(), "netlinkrib") {
			t.Skipf("no usable network stack in this sandbox for a live call test: %v", err)
		}
		t.Fatalf("probing for a usable network stack: %v", err)
	}
	defer pc.Close()
	if _, err := pc.CreateOffer(); err != nil && strings.Contains(err.Error(), "netlinkrib") {
		t.Skipf("no usable network stack in this sandbox for a live call test: %v", err)
	}
}

// newCallTestSession dials a devtest prosody account and wires a bare
// accountSession around it - just enough state for the call/Jingle path
// (callsession.go), same shape as mam_live_race_test.go's sessions. A real
// on-disk db (rather than nil) matters here specifically because
// callSession.end -> logCall -> storage.Queries.InsertCallLog panics on a
// nil *Queries (a sql.DB method call on a nil receiver), unlike
// checkAndPinCallFingerprint elsewhere which tolerates db == nil.
func newCallTestSession(t *testing.T, ctx context.Context, jid, pw string) *accountSession {
	t.Helper()
	tlsConfig := devtestTLSConfig(t)
	client, err := xmpp.Dial(ctx, jid, pw, tlsConfig)
	if err != nil {
		t.Fatalf("dial %s: %v", jid, err)
	}
	t.Cleanup(func() { client.Close() })
	_, db, err := storage.Open(filepath.Join(t.TempDir(), "call-e2e.db"))
	if err != nil {
		t.Fatalf("open storage for %s: %v", jid, err)
	}
	sess := &accountSession{account: config.Account{JID: jid, Password: pw}, db: db}
	sess.client.Store(client)
	return sess
}

// waitCallPCState polls s's current call's peer connection until it reaches
// want or the deadline passes, returning the last-seen state (or "no call"/
// "no peer connection" if the session never got that far) on timeout.
func waitCallPCState(t *testing.T, s *accountSession, want webrtc.PeerConnectionState, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		c := s.currentCall()
		if c == nil {
			last = "no call"
		} else {
			c.mu.Lock()
			pc := c.pc
			state := c.state
			c.mu.Unlock()
			if pc == nil {
				last = "no peer connection (call state=" + state.String() + ")"
			} else {
				got := pc.ConnectionState()
				last = "pc state=" + got.String() + " (call state=" + state.String() + ")"
				if got == want {
					return
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s: peer connection never reached %s within %s - last seen: %s", s.account.JID, want, timeout, last)
}

// waitCallState polls s's current call's lifecycle state until it reaches
// want or the deadline passes.
func waitCallState(t *testing.T, s *accountSession, want callState, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last callState = callIdle
	for time.Now().Before(deadline) {
		c := s.currentCall()
		if c != nil {
			last = c.currentState()
			if last == want {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s: call never reached state %s within %s - last seen: %s", s.account.JID, want, timeout, last)
}

// TestCallVideoFromStartReachesConnected reproduces a real "accepted call,
// stuck connecting forever" incident captured live in debug.log
// (2026-08-12T14:54-14:55, sid=ffde1c1ead0c0aeacb9913fa0fee7fe7): an
// outgoing video call (startVideoCall, autoStartVideo bundled into the very
// first offer - see initiateSession) went proposing -> ringing-remote ->
// negotiating, its ICE peer connection logged "connecting", and then just
// sat there - no candidate errors, no ICE failure, nothing - until the peer
// gave up and sent a session-terminate 18 seconds later. Plain audio-only
// calls in the same logs reliably reach "connected" within a second or two,
// so this isolates whether bundling video into the initial offer specifically
// is what breaks ICE for a real two-account Jingle exchange (as opposed to
// pion in isolation - call/loopback_test.go's TestLoopbackVideoFromStart
// already proves pion itself handles a video-from-start offer/answer fine).
func TestCallVideoFromStartReachesConnected(t *testing.T) {
	skipIfNoCallNetwork(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	alice := newCallTestSession(t, ctx, "alice@localhost", "alicepw")
	bob := newCallTestSession(t, ctx, "bob@localhost", "bobpw")

	go listen(ctx, nil, 0, alice)
	go listen(ctx, nil, 0, bob)

	if _, err := alice.doStartCall(ctx, nil, 0, "bob@localhost", true /* autoVideo */, false /* useCamera */); err != nil {
		t.Fatalf("alice: starting video call: %v", err)
	}
	t.Cleanup(func() {
		if c := alice.currentCall(); c != nil {
			_ = alice.hangupCall(ctx)
		}
		if c := bob.currentCall(); c != nil {
			_ = bob.hangupCall(ctx)
		}
	})

	waitCallState(t, bob, callRingingLocal, 5*time.Second)
	if err := bob.answerCall(ctx); err != nil {
		t.Fatalf("bob: answering call: %v", err)
	}

	waitCallPCState(t, alice, webrtc.PeerConnectionStateConnected, 15*time.Second)
	waitCallPCState(t, bob, webrtc.PeerConnectionStateConnected, 15*time.Second)
}

// TestCallAudioOnlyReachesConnected is the control for
// TestCallVideoFromStartReachesConnected: a plain audio call between the
// same two accounts, which the historical logs show reliably connecting.
func TestCallAudioOnlyReachesConnected(t *testing.T) {
	skipIfNoCallNetwork(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	alice := newCallTestSession(t, ctx, "alice@localhost", "alicepw")
	bob := newCallTestSession(t, ctx, "bob@localhost", "bobpw")

	go listen(ctx, nil, 0, alice)
	go listen(ctx, nil, 0, bob)

	if err := alice.startCall(ctx, nil, 0, "bob@localhost"); err != nil {
		t.Fatalf("alice: starting call: %v", err)
	}
	t.Cleanup(func() {
		if c := alice.currentCall(); c != nil {
			_ = alice.hangupCall(ctx)
		}
		if c := bob.currentCall(); c != nil {
			_ = bob.hangupCall(ctx)
		}
	})

	waitCallState(t, bob, callRingingLocal, 5*time.Second)
	if err := bob.answerCall(ctx); err != nil {
		t.Fatalf("bob: answering call: %v", err)
	}

	waitCallPCState(t, alice, webrtc.PeerConnectionStateConnected, 15*time.Second)
	waitCallPCState(t, bob, webrtc.PeerConnectionStateConnected, 15*time.Second)
}
