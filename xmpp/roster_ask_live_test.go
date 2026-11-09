//go:build integration

package xmpp

import (
	"context"
	"testing"
)

// TestRosterReportsPendingRequest checks the roster fetch keeps the ask
// attribute: a contact we sent a request to who hasn't answered must read
// as pending, not just "none".
func TestRosterReportsPendingRequest(t *testing.T) {
	ctx := context.Background()
	alice, err := Dial(ctx, "alice@localhost", "alicepw", devtestTLSConfig(t))
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}
	defer alice.Close()

	const peer = "pending-roster-test@localhost" // never registered, so never answers
	if err := alice.AddContact(ctx, peer, ""); err != nil {
		t.Fatalf("add contact: %v", err)
	}
	defer alice.RemoveContact(ctx, peer)

	contacts, err := alice.Roster(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range contacts {
		if c.JID == peer {
			if !c.Ask {
				t.Fatalf("pending contact read as %+v, want Ask", c)
			}
			return
		}
	}
	t.Fatalf("%s not in the roster: %+v", peer, contacts)
}
