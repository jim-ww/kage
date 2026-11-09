package ui

import (
	"testing"

	"charm.land/bubbles/v2/list"
)

// newTypingTestModel returns a model with one chat whose contact is currently
// marked as typing.
func newTypingTestModel(t *testing.T) Model {
	t.Helper()
	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	m.accounts = []Account{{Chats: []list.Item{Chat{Address: "bob@example.test"}}, Messages: map[int][]Message{}}}
	m.currentAccount = 0
	m.chats.SetItems(m.accounts[0].Chats)

	next, _ := m.Update(TypingMsg{AccountIdx: 0, From: "bob@example.test", Typing: true})
	m = next.(Model)
	if !m.accounts[0].Chats[0].(Chat).Typing {
		t.Fatal("chat not marked typing after a composing state")
	}
	return m
}

// A contact's "composing" must not outlive peerTypingTimeout: the "active"
// that would clear it is just another stanza, and a peer that quits, crashes
// or drops off the network never sends one.
func TestPeerTypingExpires(t *testing.T) {
	m := newTypingTestModel(t)

	gen := m.peerTypingGen[peerTypingKey(0, "bob@example.test")]
	next, _ := m.Update(peerTypingExpiredMsg{accountIdx: 0, from: "bob@example.test", gen: gen})
	m = next.(Model)
	if m.accounts[0].Chats[0].(Chat).Typing {
		t.Error("chat still marked typing after the expiry timer fired")
	}
}

// A newer "composing" rearms the timer, so the earlier timer firing must not
// clear an indicator that's current.
func TestPeerTypingExpiryIgnoresStaleTimer(t *testing.T) {
	m := newTypingTestModel(t)
	staleGen := m.peerTypingGen[peerTypingKey(0, "bob@example.test")]

	next, _ := m.Update(TypingMsg{AccountIdx: 0, From: "bob@example.test", Typing: true})
	m = next.(Model)

	next, _ = m.Update(peerTypingExpiredMsg{accountIdx: 0, from: "bob@example.test", gen: staleGen})
	m = next.(Model)
	if !m.accounts[0].Chats[0].(Chat).Typing {
		t.Error("stale expiry timer cleared a typing indicator a newer composing had rearmed")
	}
}

// The message they were composing arriving clears the indicator - not every
// client pairs the message with an "active" chat state.
func TestPeerTypingClearedByIncomingMessage(t *testing.T) {
	m := newTypingTestModel(t)

	next, _ := m.Update(IncomingMessageMsg{
		AccountIdx: 0,
		From:       "bob@example.test",
		Message:    Message{ID: "m1", Content: "hi"},
	})
	m = next.(Model)
	if m.accounts[0].Chats[0].(Chat).Typing {
		t.Error("chat still marked typing after the message arrived")
	}
}

// Nor can they be typing from a resource that just went offline.
func TestPeerTypingClearedWhenOffline(t *testing.T) {
	m := newTypingTestModel(t)

	next, _ := m.Update(PresenceMsg{AccountIdx: 0, From: "bob@example.test", Presence: PresenceOffline})
	m = next.(Model)
	if m.accounts[0].Chats[0].(Chat).Typing {
		t.Error("chat still marked typing after the contact went offline")
	}
}

// TestOwnMessageKeepsPeerTyping checks our own sent message (echoed back by
// the daemon to every attached TUI) doesn't clear the contact's typing
// indicator - they may well still be typing - while theirs does.
func TestOwnMessageKeepsPeerTyping(t *testing.T) {
	m := newTypingTestModel(t)
	next, _ := m.Update(IncomingMessageMsg{AccountIdx: 0, From: "bob@example.test", Message: Message{ID: "mine", IsMe: true, Content: "hi"}})
	m = next.(Model)
	if !m.accounts[0].Chats[0].(Chat).Typing {
		t.Fatal("our own message cleared the contact's typing indicator")
	}
	next, _ = m.Update(IncomingMessageMsg{AccountIdx: 0, From: "bob@example.test", Message: Message{ID: "theirs", Content: "yo"}})
	m = next.(Model)
	if m.accounts[0].Chats[0].(Chat).Typing {
		t.Fatal("their message didn't clear their typing indicator")
	}
}
