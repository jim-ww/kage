package main

import (
	"context"
	"testing"
	"time"

	"github.com/jim-ww/kage/config"
)

// TestKeepAliveStopsWithContext pins the two properties keepAlive has to hold
// as a long-lived per-account goroutine: it exits when its account's context
// does (rather than outliving the session it belongs to), and an account that
// is currently offline is skipped quietly instead of panicking on the absent
// client - reconnectWithBackoff, not keepAlive, owns getting back online.
func TestKeepAliveStopsWithContext(t *testing.T) {
	s := &accountSession{account: config.Account{JID: "me@example.com"}}
	// client is left unset: an account that has dropped and not yet redialed.
	if _, err := s.liveClient(); err == nil {
		t.Fatal("liveClient on a session with no client should report offline")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		keepAlive(ctx, s, time.Millisecond)
	}()

	// Let it tick over the offline path many times over.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("keepAlive did not return after its context was canceled")
	}
}
