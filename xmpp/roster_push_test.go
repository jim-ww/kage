package xmpp

import (
	"encoding/xml"
	"strings"
	"testing"
)

// decodeRosterPush parses the <query/> payload of a roster push the way
// handleRosterPush does, minus the stanza plumbing.
func decodeRosterPush(t *testing.T, payload string) rosterPushQuery {
	t.Helper()
	var q rosterPushQuery
	if err := xml.NewDecoder(strings.NewReader(payload)).Decode(&q); err != nil {
		t.Fatalf("decoding roster push: %v", err)
	}
	return q
}

func TestRosterPushDecodesSubscriptionAndAsk(t *testing.T) {
	// The push a server sends right after we send <presence type="subscribe">:
	// the item exists but the request is still pending, which must not be
	// confused with "not subscribed".
	q := decodeRosterPush(t, `<query xmlns="jabber:iq:roster" ver="42">
		<item jid="bob@example.test" subscription="none" ask="subscribe"/>
	</query>`)

	if len(q.Items) != 1 {
		t.Fatalf("decoded %d items, want 1", len(q.Items))
	}
	item := q.Items[0]
	if item.JID != "bob@example.test" {
		t.Errorf("JID = %q", item.JID)
	}
	if item.Subscription != "none" {
		t.Errorf("Subscription = %q, want none", item.Subscription)
	}
	if item.Ask != "subscribe" {
		t.Errorf("Ask = %q, want subscribe", item.Ask)
	}
}

func TestRosterPushDecodesNameGroupsAndRemoval(t *testing.T) {
	q := decodeRosterPush(t, `<query xmlns="jabber:iq:roster">
		<item jid="alice@example.test" name="Alice" subscription="both">
			<group>Friends</group>
			<group>Work</group>
		</item>
	</query>`)
	if len(q.Items) != 1 {
		t.Fatalf("decoded %d items, want 1", len(q.Items))
	}
	if got := q.Items[0].Name; got != "Alice" {
		t.Errorf("Name = %q, want Alice", got)
	}
	if got := q.Items[0].Subscription; got != "both" {
		t.Errorf("Subscription = %q, want both", got)
	}
	if got := q.Items[0].Groups; len(got) != 2 || got[0] != "Friends" || got[1] != "Work" {
		t.Errorf("Groups = %v, want [Friends Work]", got)
	}

	q = decodeRosterPush(t, `<query xmlns="jabber:iq:roster">
		<item jid="gone@example.test" subscription="remove"/>
	</query>`)
	if len(q.Items) != 1 {
		t.Fatalf("decoded %d items, want 1", len(q.Items))
	}
	if got := q.Items[0].Subscription; got != "remove" {
		t.Errorf("Subscription = %q, want remove - a deletion would otherwise read as a plain update", got)
	}
}
