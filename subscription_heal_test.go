package main

import (
	"context"
	"errors"
	"testing"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/xmpp"
)

// TestHealLopsidedSubscriptionsOnlyTargetsFrom pins the selection rule.
//
// "from" is the broken state - they can see us, we can't see them, so they
// show as permanently offline. Every other state must be left alone: "none"
// is where a contact who actually declined us ends up, and re-asking them on
// every connect and reconnect would re-prompt them indefinitely.
func TestHealLopsidedSubscriptionsOnlyTargetsFrom(t *testing.T) {
	var asked []string
	resubscribe := func(_ context.Context, jid string) error {
		asked = append(asked, jid)
		return nil
	}

	contacts := []xmpp.Contact{
		{JID: "lopsided@x", Subscription: "from"},
		{JID: "declined@x", Subscription: "none"},
		{JID: "pending@x", Subscription: ""},
		{JID: "oneway@x", Subscription: "to"},
		{JID: "fine@x", Subscription: "both"},
		{JID: "lopsided2@x", Subscription: "from"},
	}

	s := &accountSession{account: config.Account{JID: "me@example.com"}}
	healLopsidedSubscriptions(context.Background(), s, contacts, resubscribe)

	want := []string{"lopsided@x", "lopsided2@x"}
	if len(asked) != len(want) {
		t.Fatalf("re-requested %v, want %v", asked, want)
	}
	for i := range want {
		if asked[i] != want[i] {
			t.Fatalf("re-requested %v, want %v", asked, want)
		}
	}
}

// One contact's failure must not stop the rest being healed.
func TestHealLopsidedSubscriptionsContinuesPastFailure(t *testing.T) {
	var asked []string
	resubscribe := func(_ context.Context, jid string) error {
		asked = append(asked, jid)
		if jid == "first@x" {
			return errors.New("not-authorized")
		}
		return nil
	}

	contacts := []xmpp.Contact{
		{JID: "first@x", Subscription: "from"},
		{JID: "second@x", Subscription: "from"},
	}
	s := &accountSession{account: config.Account{JID: "me@example.com"}}
	healLopsidedSubscriptions(context.Background(), s, contacts, resubscribe)

	if len(asked) != 2 {
		t.Fatalf("re-requested %v, want both contacts attempted", asked)
	}
}
