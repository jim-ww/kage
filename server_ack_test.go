//go:build integration

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

// TestServerAckMarksMessageAfterDebounce verifies the real fix end to end
// against a live server: a plain-text send (chat encryption explicitly
// "none", so no OMEMO setup is needed) gets ServerAcked once
// confirmPendingAcks's debounced ping actually round-trips - it must not be
// set immediately after adapter.send returns, only after the ping confirms
// it.
func TestServerAckMarksMessageAfterDebounce(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()

	client, err := xmpp.Dial(ctx, "alice@localhost", "alicepw", tlsConfig)
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}
	defer client.Close()

	_, q, err := storage.Open(filepath.Join(t.TempDir(), "ack-test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	if err := q.SetChatEncryptionMode(ctx, storage.SetChatEncryptionModeParams{
		AccountJid: "alice@localhost", RosterJid: "bob@localhost", Mode: "none",
	}); err != nil {
		t.Fatalf("SetChatEncryptionMode: %v", err)
	}

	sess := &accountSession{account: config.Account{JID: "alice@localhost"}, db: q}
	sess.client.Store(client)
	a := &adapter{sessions: []*accountSession{sess}}

	id, err := a.send(ctx, 0, "bob@localhost", "hello bob", ui.SendOptions{})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if id == "" {
		t.Fatal("send returned empty id")
	}

	// Immediately after send returns, ServerAcked must NOT be set yet - it's
	// only supposed to flip once confirmPendingAcks's debounced ping (fired
	// ackDebounce after the send, on its own goroutine) actually completes.
	acked, err := messageServerAcked(ctx, q, "alice@localhost", "bob@localhost", id)
	if err != nil {
		t.Fatalf("checking ServerAcked immediately after send: %v", err)
	}
	if acked {
		t.Fatal("ServerAcked = true immediately after send, want false (ack is async/debounced)")
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		acked, err := messageServerAcked(ctx, q, "alice@localhost", "bob@localhost", id)
		if err != nil {
			t.Fatalf("checking ServerAcked: %v", err)
		}
		if acked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ServerAcked never became true within 3s of sending")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestServerAckBatchesBurstIntoOnePing verifies several messages sent in
// quick succession (well within ackDebounce of each other) share a single
// ping round trip - trackForServerAck's whole reason for debouncing rather
// than confirming every message individually - by sending 5 messages and
// checking they all become ServerAcked together, shortly after the last one
// (not the first).
func TestServerAckBatchesBurstIntoOnePing(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()

	client, err := xmpp.Dial(ctx, "alice@localhost", "alicepw", tlsConfig)
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}
	defer client.Close()

	_, q, err := storage.Open(filepath.Join(t.TempDir(), "ack-batch-test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	if err := q.SetChatEncryptionMode(ctx, storage.SetChatEncryptionModeParams{
		AccountJid: "alice@localhost", RosterJid: "bob@localhost", Mode: "none",
	}); err != nil {
		t.Fatalf("SetChatEncryptionMode: %v", err)
	}

	sess := &accountSession{account: config.Account{JID: "alice@localhost"}, db: q}
	sess.client.Store(client)
	a := &adapter{sessions: []*accountSession{sess}}

	const n = 5
	ids := make([]string, n)
	for i := range n {
		id, err := a.send(ctx, 0, "bob@localhost", "burst message", ui.SendOptions{})
		if err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
		ids[i] = id
		time.Sleep(50 * time.Millisecond) // well under ackDebounce (500ms) between each
	}

	sess.ackMu.Lock()
	batchSize := len(sess.ackPending)
	sess.ackMu.Unlock()
	if batchSize != n {
		t.Fatalf("ackPending length right after the burst = %d, want %d (all %d sends should still share one pending batch)", batchSize, n, n)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		allAcked := true
		for _, id := range ids {
			acked, err := messageServerAcked(ctx, q, "alice@localhost", "bob@localhost", id)
			if err != nil {
				t.Fatalf("checking ServerAcked: %v", err)
			}
			if !acked {
				allAcked = false
			}
		}
		if allAcked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("not every message in the burst became ServerAcked within 3s")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestServerAckLeftUnsetWhenConnectionDropsBeforePing reproduces the
// original bug report at the unit level: a message is sent successfully
// (client.Send returns no error - the local write reached the socket), but
// the connection dies before confirmPendingAcks's debounced ping ever gets
// a response. ServerAcked must stay false rather than the old behavior of
// treating "Send returned nil" as proof of delivery.
func TestServerAckLeftUnsetWhenConnectionDropsBeforePing(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()

	client, err := xmpp.Dial(ctx, "alice@localhost", "alicepw", tlsConfig)
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}

	_, q, err := storage.Open(filepath.Join(t.TempDir(), "ack-drop-test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	if err := q.SetChatEncryptionMode(ctx, storage.SetChatEncryptionModeParams{
		AccountJid: "alice@localhost", RosterJid: "bob@localhost", Mode: "none",
	}); err != nil {
		t.Fatalf("SetChatEncryptionMode: %v", err)
	}

	sess := &accountSession{account: config.Account{JID: "alice@localhost"}, db: q}
	sess.client.Store(client)
	a := &adapter{sessions: []*accountSession{sess}}

	id, err := a.send(ctx, 0, "bob@localhost", "about to drop", ui.SendOptions{})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	// Kill the connection immediately, well before ackDebounce (500ms)
	// fires - by the time confirmPendingAcks runs its ping, liveClient()
	// (or the ping round trip itself) must fail.
	client.Close()

	time.Sleep(1 * time.Second) // past ackDebounce, long enough for confirmPendingAcks to have run and given up
	acked, err := messageServerAcked(ctx, q, "alice@localhost", "bob@localhost", id)
	if err != nil {
		t.Fatalf("checking ServerAcked: %v", err)
	}
	if acked {
		t.Fatal("ServerAcked = true after the connection was closed before confirmation, want false")
	}
}

// messageServerAcked looks up whether the message with idAttr id (sent by
// accountJID to rosterJID) has ServerAcked set, by re-reading it straight
// from storage the same way a fresh TUI attach would.
func messageServerAcked(ctx context.Context, q *storage.Queries, accountJID, rosterJID, id string) (bool, error) {
	rows, err := q.ListMessagesByRoster(ctx, storage.ListMessagesByRosterParams{
		AccountJid: accountJID,
		RosterJid:  nullString(rosterJID),
	})
	if err != nil {
		return false, err
	}
	for _, r := range rows {
		if r.Idattr.String == id {
			return r.Serveracked, nil
		}
	}
	return false, nil
}
