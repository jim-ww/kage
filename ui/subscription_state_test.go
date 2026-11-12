package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
)

func TestSubscriptionLabel(t *testing.T) {
	for _, tc := range []struct {
		chat Chat
		want string
	}{
		{Chat{Subscription: "both"}, ""},
		{Chat{Subscription: ""}, ""},
		{Chat{Subscription: "none", SubscriptionPending: true}, "request pending"},
		{Chat{Subscription: "from", SubscriptionPending: true}, "request pending"},
		{Chat{Subscription: "none"}, "not subscribed"},
		{Chat{Subscription: "to"}, "you see them, they don't see you"},
		{Chat{Subscription: "from"}, "they see you, you don't see them"},
	} {
		if got := subscriptionLabel(tc.chat); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.chat, got, tc.want)
		}
	}
}

// TestChatAddedUpdatesSubscription checks a roster push re-sent as a
// ChatAddedMsg upsert updates a known chat's subscription state in place -
// e.g. a pending request getting approved.
func TestChatAddedUpdatesSubscription(t *testing.T) {
	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	chat := Chat{Name: "Bob", Address: "bob@example.test", Subscription: "none", SubscriptionPending: true}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	m.chats.SetItems(m.accounts[0].Chats)

	next, _ := m.Update(ChatAddedMsg{Chat: Chat{Name: "Bob", Address: "bob@example.test", Subscription: "both"}})
	m = next.(Model)
	got := m.accounts[0].Chats[0].(Chat)
	if got.Subscription != "both" || got.SubscriptionPending || len(m.accounts[0].Chats) != 1 {
		t.Fatalf("after approval: %+v (%d chats)", got, len(m.accounts[0].Chats))
	}

	m = openContacts(t, m)
	if strings.Contains(m.contactsOpen().View(), "pending") {
		t.Fatal("contact manager still shows the request as pending")
	}
	m.accounts[0].Chats[0] = Chat{Name: "Bob", Address: "bob@example.test", Subscription: "none", SubscriptionPending: true}
	m.refreshContacts()
	if !strings.Contains(m.contactsOpen().View(), "Bob <bob@example.test> (request pending)") {
		t.Fatalf("contact manager row:\n%s", m.contactsOpen().View())
	}
}
