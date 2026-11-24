package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/xmpp"
)

// TestAttachedTUIKeepsContactOnlineWhenOneDeviceLeaves is the regression for
// contacts shown offline while online: a TUI attaching after the daemon
// learned a contact's presence got the aggregate but not the devices behind
// it, so the first device to go away left it computing over none.
func TestAttachedTUIKeepsContactOnlineWhenOneDeviceLeaves(t *testing.T) {
	ctx := context.Background()
	_, q, err := storage.Open(filepath.Join(t.TempDir(), "kage.db"))
	if err != nil {
		t.Fatal(err)
	}
	acct := config.Account{JID: "me@example.com", Status: "offline"}
	if err := q.UpsertRoster(ctx, storage.UpsertRosterParams{AccountJid: acct.JID, Jid: "bob@example.com", Name: "bob", Subs: "both"}); err != nil {
		t.Fatal(err)
	}
	s := &accountSession{account: acct, db: q}
	s.roster.Store(&map[string]rosterEntry{"bob@example.com": {Subs: "both"}})
	for _, res := range []string{"phone", "laptop"} {
		dispatchEvent(ctx, nil, 0, s, xmpp.PresenceEvent{From: "bob@example.com/" + res, Available: true})
	}

	d := &daemonServer{a: &adapter{cfgAccounts: []config.Account{acct}, sessions: []*accountSession{s}, queries: q}}
	accounts := d.listAccounts()
	if len(accounts) != 1 || len(accounts[0].Chats) != 1 {
		t.Fatalf("snapshot: %+v", accounts)
	}
	chat := accounts[0].Chats[0].(ui.Chat)
	if chat.Presence != ui.PresenceOnline || len(chat.Resources) != 2 {
		t.Fatalf("snapshot chat: presence %v resources %+v, want online on both devices", chat.Presence, chat.Resources)
	}

	// What the TUI does with the laptop going away (ui's PresenceMsg).
	chat.Resources = ui.WithResource(chat.Resources, "laptop", ui.PresenceOffline)
	if got := ui.AggregatePresence(chat.Resources); got != ui.PresenceOnline {
		t.Errorf("after the laptop left: %v, want still online from the phone", got)
	}
}

// TestDisconnectForgetsPresence checks a dropped connection takes every
// contact offline: their going offline meanwhile would never reach us, and
// they'd stay shown online.
func TestDisconnectForgetsPresence(t *testing.T) {
	ctx := context.Background()
	srv, events := attachedClient(t)
	s := &accountSession{account: config.Account{JID: "me@example.com"}}
	s.roster.Store(&map[string]rosterEntry{"bob@example.com": {Subs: "both"}, "carol@example.com": {Subs: "both"}})
	dispatchEvent(ctx, nil, 0, s, xmpp.PresenceEvent{From: "bob@example.com/phone", Available: true})
	dispatchEvent(ctx, nil, 0, s, xmpp.PresenceEvent{From: "carol@example.com", Available: true})

	s.forgetPresence(srv, 0)

	for contact, e := range derefRoster(s.roster.Load()) {
		if e.Presence != ui.PresenceOffline || len(e.Resources) != 0 {
			t.Errorf("%s still %v on %+v", contact, e.Presence, e.Resources)
		}
	}
	offline := map[string]bool{}
	for range 2 {
		var msg ui.PresenceMsg
		awaitEvent(t, events, &msg, 5*time.Second)
		if msg.Presence == ui.PresenceOffline {
			offline[msg.From+"/"+msg.Resource] = true
		}
	}
	if !offline["bob@example.com/phone"] || !offline["carol@example.com/"] {
		t.Errorf("broadcast offline for %v, want bob's phone and carol", offline)
	}
}
