package main

import (
	"testing"

	"github.com/jim-ww/kage/ipc"
)

// TestMarkTypingBookkeeping covers what clearTyping relies on: the daemon
// remembers exactly the peers each attached TUI has an uncleared XEP-0085
// "composing" state with, so a client that quits mid-keystroke doesn't leave a
// typing indicator stuck on the peer's client forever.
func TestMarkTypingBookkeeping(t *testing.T) {
	a := &adapter{}

	a.markTyping(1, 0, "bob@localhost", true)
	a.markTyping(1, 1, "carol@localhost", true)

	// An "active" for a chat we're not composing to must not forget the one
	// we are.
	a.markTyping(1, 0, "dave@localhost", false)

	pending := a.drainTyping(1)
	if got, want := len(pending), 2; got != want {
		t.Fatalf("drainTyping() has %d entries (%v), want %d", got, pending, want)
	}
	if got, want := pending[0], "bob@localhost"; got != want {
		t.Errorf("account 0 pending = %q, want %q", got, want)
	}
	if got, want := pending[1], "carol@localhost"; got != want {
		t.Errorf("account 1 pending = %q, want %q", got, want)
	}

	if pending := a.drainTyping(1); len(pending) != 0 {
		t.Errorf("second drainTyping() = %v, want empty", pending)
	}
}

func TestMarkTypingClearsComposing(t *testing.T) {
	a := &adapter{}
	a.markTyping(1, 0, "bob@localhost", true)
	a.markTyping(1, 0, "bob@localhost", false)
	if pending := a.drainTyping(1); len(pending) != 0 {
		t.Errorf("drainTyping() after clearing = %v, want empty", pending)
	}
}

// Switching chats mid-compose replaces the tracked peer rather than
// accumulating one entry per chat - only the latest can still be showing a
// typing indicator for this account.
func TestMarkTypingSwitchChat(t *testing.T) {
	a := &adapter{}
	a.markTyping(1, 0, "bob@localhost", true)
	a.markTyping(1, 0, "carol@localhost", true)
	pending := a.drainTyping(1)
	if got, want := len(pending), 1; got != want {
		t.Fatalf("drainTyping() has %d entries (%v), want %d", got, pending, want)
	}
	if got, want := pending[0], "carol@localhost"; got != want {
		t.Errorf("account 0 pending = %q, want %q", got, want)
	}
}

// One TUI quitting must not clear another's chat state (nor leave its own
// behind): with two clients attached, each tracks its own composing peer.
func TestMarkTypingPerClient(t *testing.T) {
	a := &adapter{}
	a.markTyping(1, 0, "bob@localhost", true)
	a.markTyping(2, 0, "carol@localhost", true)

	pending := a.drainTyping(1)
	if got, want := pending[0], "bob@localhost"; got != want {
		t.Errorf("client 1 pending = %q, want %q", got, want)
	}

	pending = a.drainTyping(2)
	if got, want := pending[0], "carol@localhost"; got != want {
		t.Errorf("client 2 pending = %q, want %q", got, want)
	}
}

// Nothing is ever recorded for a client that didn't type - drainTyping on an
// unknown client is a no-op, not a nil-map panic.
func TestDrainTypingUnknownClient(t *testing.T) {
	a := &adapter{}
	if pending := a.drainTyping(ipc.ClientID(7)); len(pending) != 0 {
		t.Errorf("drainTyping(unknown) = %v, want empty", pending)
	}
	a.markTyping(7, 0, "bob@localhost", false) // "active" with nothing tracked
}
