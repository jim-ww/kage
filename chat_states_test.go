package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/xmpp"
)

// TestChatStatesNegotiation checks chat states follow XEP-0085 §5.1's
// rules for a contact whose client wasn't asked: <active/> in messages
// until they reply without chat states, typing notifications only once
// they've sent chat states themselves, and nothing at all when disabled.
func TestChatStatesNegotiation(t *testing.T) {
	prev := chatStatesEnabled.Load()
	t.Cleanup(func() { chatStatesEnabled.Store(prev) })
	chatStatesEnabled.Store(true)

	ctx := context.Background()
	_, q, err := storage.Open(filepath.Join(t.TempDir(), "kage.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	s := &accountSession{account: config.Account{JID: "me@example.com"}, db: q}
	srv := ipc.NewServer()
	check := func(when string, peer string, wantMessages, wantStandalone bool) {
		t.Helper()
		if m, sa := s.chatStatesTo(peer); m != wantMessages || sa != wantStandalone {
			t.Errorf("%s: chatStatesTo(%s) = %v, %v; want %v, %v", when, peer, m, sa, wantMessages, wantStandalone)
		}
	}

	check("unknown contact", "bob@example.com", true, false)

	// A message with a chat state: first the state, then the message.
	dispatchEvent(ctx, srv, 0, s, xmpp.ChatStateEvent{From: "bob@example.com/pc", State: xmpp.ChatStateActive})
	handleIncomingMessage(ctx, srv, 0, s, xmpp.MessageEvent{ID: "m1", From: "bob@example.com/pc", Body: "hi", SentAt: time.Now(), ChatStates: true})
	check("after bob sent chat states", "bob@example.com", true, true)

	handleIncomingMessage(ctx, srv, 0, s, xmpp.MessageEvent{ID: "m2", From: "carol@example.com/pc", Body: "hi", SentAt: time.Now()})
	check("after carol replied without", "carol@example.com", false, false)

	// Our own message from another device says nothing about carol.
	handleIncomingMessage(ctx, srv, 0, s, xmpp.MessageEvent{ID: "m3", From: "me@example.com/phone", To: "dave@example.com", Outgoing: true, Body: "hi", SentAt: time.Now()})
	check("after our own message to dave", "dave@example.com", true, false)

	chatStatesEnabled.Store(false)
	check("disabled", "bob@example.com", false, false)
}
