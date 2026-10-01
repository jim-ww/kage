package main

import "testing"

// TestMarkTypingBookkeeping covers what clearTyping relies on: the daemon
// remembers exactly the peers it has an uncleared XEP-0085 "composing" state
// with, so a TUI that quits mid-keystroke doesn't leave a typing indicator
// stuck on the peer's client forever.
func TestMarkTypingBookkeeping(t *testing.T) {
	a := &adapter{}

	a.markTyping(0, "bob@localhost", true)
	a.markTyping(1, "carol@localhost", true)

	// An "active" for a chat we're not composing to must not forget the one
	// we are.
	a.markTyping(0, "dave@localhost", false)

	pending := a.drainTyping()
	if got, want := len(pending), 2; got != want {
		t.Fatalf("drainTyping() has %d entries (%v), want %d", got, pending, want)
	}
	if got, want := pending[0], "bob@localhost"; got != want {
		t.Errorf("account 0 pending = %q, want %q", got, want)
	}
	if got, want := pending[1], "carol@localhost"; got != want {
		t.Errorf("account 1 pending = %q, want %q", got, want)
	}

	if pending := a.drainTyping(); len(pending) != 0 {
		t.Errorf("second drainTyping() = %v, want empty", pending)
	}
}

func TestMarkTypingClearsComposing(t *testing.T) {
	a := &adapter{}
	a.markTyping(0, "bob@localhost", true)
	a.markTyping(0, "bob@localhost", false)
	if pending := a.drainTyping(); len(pending) != 0 {
		t.Errorf("drainTyping() after clearing = %v, want empty", pending)
	}
}

// Switching chats mid-compose replaces the tracked peer rather than
// accumulating one entry per chat - only the latest can still be showing a
// typing indicator for this account.
func TestMarkTypingSwitchChat(t *testing.T) {
	a := &adapter{}
	a.markTyping(0, "bob@localhost", true)
	a.markTyping(0, "carol@localhost", true)
	pending := a.drainTyping()
	if got, want := len(pending), 1; got != want {
		t.Fatalf("drainTyping() has %d entries (%v), want %d", got, pending, want)
	}
	if got, want := pending[0], "carol@localhost"; got != want {
		t.Errorf("account 0 pending = %q, want %q", got, want)
	}
}
