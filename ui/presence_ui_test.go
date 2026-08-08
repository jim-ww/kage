package ui

import (
	"testing"

	"charm.land/bubbles/v2/list"
)

// Reproduces the reported bug at the UI layer: account[1] ("bob") has a chat
// for account[0]'s JID ("alice"). A PresenceMsg arrives for account 1 saying
// alice is online, while the *currently viewed* account is account 0. Then
// the user switches to account 1 - does its chat list show alice as online?
func TestPresenceMsgUpdatesNonCurrentAccount(t *testing.T) {
	m := newTestModel(nil)
	m.accounts = []Account{
		{Name: "alice@localhost", Chats: []list.Item{Chat{Name: "bob", Address: "bob@localhost"}}},
		{Name: "bob@localhost", Chats: []list.Item{Chat{Name: "alice", Address: "alice@localhost"}}},
	}
	m.currentAccount = 0 // viewing alice's account when bob's client learns alice is online

	next, _, _ := m.handleEventMsg(PresenceMsg{AccountIdx: 1, From: "alice@localhost", Presence: PresenceOnline})
	m = next

	chat := m.accounts[1].Chats[0].(Chat)
	if chat.Presence != PresenceOnline {
		t.Fatalf("account[1]'s chat for alice: got Presence %v, want PresenceOnline", chat.Presence)
	}

	// Now actually switch to account 1, as the user would, and confirm the
	// visible chats list reflects it too.
	m.switchAccount(1)
	if len(m.chats.Items()) != 1 {
		t.Fatalf("expected 1 chat item after switching accounts, got %d", len(m.chats.Items()))
	}
	visible := m.chats.Items()[0].(Chat)
	if visible.Presence != PresenceOnline {
		t.Fatalf("visible chat list after switching to account 1: got Presence %v, want PresenceOnline", visible.Presence)
	}
}
