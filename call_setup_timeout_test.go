package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/storage"
)

func newSetupTimeoutCall(t *testing.T, state callState) (*accountSession, *callSession) {
	t.Helper()
	_, db, err := storage.Open(filepath.Join(t.TempDir(), "calls.db"))
	if err != nil {
		t.Fatal(err)
	}
	sess := &accountSession{account: config.Account{JID: "alice@example.test"}, db: db}
	c := &callSession{sess: sess, peer: "bob@example.test", incoming: true, state: state, done: make(chan struct{})}
	sess.call = c
	return sess, c
}

func shortSetupTimeouts(t *testing.T, ring, setup time.Duration) {
	t.Helper()
	oldRing, oldSetup := ringTimeout, setupTimeout
	ringTimeout, setupTimeout = ring, setup
	t.Cleanup(func() { ringTimeout, setupTimeout = oldRing, oldSetup })
}

func waitCallEnded(t *testing.T, sess *accountSession, c *callSession) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
		t.Fatalf("call still %s, want it timed out", c.currentState())
	}
	// end closes done first, clearing the slot last.
	for deadline := time.Now().Add(2 * time.Second); sess.currentCall() != nil; {
		if time.Now().After(deadline) {
			t.Fatal("timed-out call still in the account's slot")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestUnansweredCallTimesOut checks an incoming call nobody answers stops
// ringing on its own.
func TestUnansweredCallTimesOut(t *testing.T) {
	shortSetupTimeouts(t, 50*time.Millisecond, time.Hour)
	sess, c := newSetupTimeoutCall(t, callRingingLocal)
	c.watchSetup()
	waitCallEnded(t, sess, c)
}

// TestAnsweredCallGetsSetupTimeout checks answering restarts the clock with
// the connect limit, and a call that then never connects still ends.
func TestAnsweredCallGetsSetupTimeout(t *testing.T) {
	shortSetupTimeouts(t, 200*time.Millisecond, 600*time.Millisecond)
	sess, c := newSetupTimeoutCall(t, callRingingLocal)
	c.watchSetup()
	time.Sleep(20 * time.Millisecond)
	c.mu.Lock()
	c.state = callNegotiating
	c.mu.Unlock()
	time.Sleep(350 * time.Millisecond)
	if c.currentState() == callEnded {
		t.Fatal("answered call ended at the ring timeout")
	}
	waitCallEnded(t, sess, c)
}

// TestConnectedCallNotTimedOut checks the watchdog leaves a connected call
// alone.
func TestConnectedCallNotTimedOut(t *testing.T) {
	shortSetupTimeouts(t, 30*time.Millisecond, 30*time.Millisecond)
	sess, c := newSetupTimeoutCall(t, callRingingLocal)
	c.watchSetup()
	c.mu.Lock()
	c.state = callConnected
	c.mu.Unlock()
	time.Sleep(120 * time.Millisecond)
	if sess.currentCall() == nil {
		t.Fatal("connected call ended by the setup watchdog")
	}
}

// TestLostConnectionEnds checks a connected call whose connection never
// comes back ends rather than waiting on an ICE restart forever.
func TestLostConnectionEnds(t *testing.T) {
	old := recoveryTimeout
	recoveryTimeout = 50 * time.Millisecond
	t.Cleanup(func() { recoveryTimeout = old })
	sess, c := newSetupTimeoutCall(t, callConnected)
	c.armRecoveryDeadline()
	waitCallEnded(t, sess, c)
}

// TestRecoveredConnectionKeepsCall checks connecting again in time stops
// the recovery deadline.
func TestRecoveredConnectionKeepsCall(t *testing.T) {
	old := recoveryTimeout
	recoveryTimeout = 50 * time.Millisecond
	t.Cleanup(func() { recoveryTimeout = old })
	sess, c := newSetupTimeoutCall(t, callConnected)
	c.mediaStarted = true
	c.armRecoveryDeadline()
	c.onConnectionState(context.Background(), webrtc.PeerConnectionStateConnected)
	time.Sleep(120 * time.Millisecond)
	if sess.currentCall() == nil {
		t.Fatal("call that reconnected in time was ended")
	}
}
