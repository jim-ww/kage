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

// TestReconnectResyncsOmemoDeviceListsForNewPeerDevice is a full end-to-end
// regression test, against a real Prosody instance, for the bug fixed in
// reconnectWithBackoff (account.go): s.omemoMgrV1/V2 kept a Transport bound
// to whatever *xmpp.Client was live at setupOmemo time, and reconnectWithBackoff
// swapped in a brand-new *xmpp.Client on every reconnect without ever
// rebuilding them - so any OMEMO device-list resync after a reconnect
// silently tried to write to a dead session and did nothing.
//
// It drives both sides through the real production code paths (adapter.send
// for alice's outgoing message, listen/dispatchEvent for bob's incoming
// one) rather than calling omemolib.Manager methods directly, so it also
// exercises the wire encoding/decoding and the real OMEMO setup/resync
// plumbing an actual running kage process uses:
//
//  1. alice sends bob a message; bob (gen1 device) receives and decrypts it.
//  2. bob "reinstalls" - a second live client for the same JID, fresh OMEMO
//     identity and device ID (gen2) - while alice stays connected, so no
//     live XEP-0163 push about it is guaranteed to have been processed.
//  3. alice's connection is dropped and reconnectWithBackoff redials, the
//     exact path that used to leave the OMEMO managers stuck on the dead
//     client.
//  4. alice sends a second message. It must be encrypted for bob's gen2
//     device, and bob's gen2 client must be able to decrypt it - proving
//     the post-reconnect resync actually reached the live server.
func TestReconnectResyncsOmemoDeviceListsForNewPeerDevice(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()

	// --- alice: wired the same way connectAccountLive wires a live account,
	// so reconnectWithBackoff (which reads sess.account/sess.tlsConfig/
	// sess.useKeyring directly) has everything it needs.
	aliceClient, err := xmpp.Dial(ctx, "alice@localhost", "alicepw", tlsConfig)
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}
	t.Cleanup(func() { aliceClient.Close() })

	_, aliceDB, err := storage.Open(filepath.Join(t.TempDir(), "alice-reconnect.db"))
	if err != nil {
		t.Fatalf("open alice storage: %v", err)
	}

	aliceSess := &accountSession{
		account:   config.Account{JID: "alice@localhost", Password: "alicepw"},
		db:        aliceDB,
		tlsConfig: tlsConfig,
	}
	aliceSess.client.Store(aliceClient)
	aliceSess.roster.Store(&map[string]rosterEntry{"bob@localhost": {Subs: "both"}})
	setupOmemo(ctx, aliceSess)
	if aliceSess.omemoMgrV1 == nil {
		t.Fatal("setupOmemo(alice): omemoMgrV1 is nil")
	}
	a := &adapter{sessions: []*accountSession{aliceSess}}

	// --- bob, gen1: a normal live client/session, same as any real contact.
	bobGen1 := newOmemoTestSession(ctx, t, "bob@localhost", "bobpw", tlsConfig)
	bobGen1Device := bobGen1.omemoMgrV1.LocalDevice()

	// 1. alice -> bob, pre-reconnect: cache is empty, so EncryptMessage's own
	// devicesFor auto-sync picks up bob's gen1 device with no help needed.
	if _, err := a.send(ctx, 0, "bob@localhost", "hello before reconnect", ui.SendOptions{}); err != nil {
		t.Fatalf("alice send #1: %v", err)
	}
	body1, err := receiveOneChatMessage(ctx, t, bobGen1)
	if err != nil {
		t.Fatalf("bob (gen1) receive #1: %v", err)
	}
	if body1 != "hello before reconnect" {
		t.Fatalf("bob (gen1) decrypted body #1 = %q, want %q", body1, "hello before reconnect")
	}

	// 2. bob "reinstalls": second live client, same JID, fresh identity/
	// device ID - alice never disconnects or gets a chance to react to any
	// live push about it before her own reconnect below.
	bobGen2 := newOmemoTestSession(ctx, t, "bob@localhost", "bobpw", tlsConfig)
	bobGen2Device := bobGen2.omemoMgrV1.LocalDevice()
	if bobGen2Device.ID == bobGen1Device.ID {
		t.Fatalf("expected bob's regenerated device ID to differ from %d, got the same", bobGen1Device.ID)
	}

	// 3. alice's connection drops and reconnectWithBackoff redials - the
	// exact path the fix targets. aliceClient (captured above) is now dead;
	// reconnectWithBackoff must both replace it AND rebuild the OMEMO
	// managers against the replacement, or step 4 silently keeps using the
	// stale one.
	aliceClient.Close()
	reconnectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	reconnectWithBackoff(reconnectCtx, a, aliceSess)
	if reconnectCtx.Err() != nil {
		t.Fatalf("reconnectWithBackoff did not complete in time: %v", reconnectCtx.Err())
	}

	// 4. alice -> bob, post-reconnect: bob's device cache already has gen1
	// cached from step 1 (non-empty), so EncryptMessage's own auto-sync
	// won't fire - only the connect-time resync reconnectWithBackoff is
	// supposed to trigger can pick up gen2 here.
	if _, err := a.send(ctx, 0, "bob@localhost", "hello after reconnect", ui.SendOptions{}); err != nil {
		t.Fatalf("alice send #2: %v", err)
	}
	body2, err := receiveOneChatMessage(ctx, t, bobGen2)
	if err != nil {
		t.Fatalf("bob (gen2, the new device) receive #2: %v", err)
	}
	if body2 != "hello after reconnect" {
		t.Fatalf("bob (gen2) decrypted body #2 = %q, want %q", body2, "hello after reconnect")
	}
}

// receiveOneChatMessage waits for the next incoming chat-message event on
// sess's live client and runs it through the real dispatchEvent/
// handleIncomingMessage pipeline (the same code the running daemon uses),
// then returns whatever ended up stored as that message's body - "[message
// could not be decrypted: ...]" on an OMEMO failure, same as a real client
// would show.
func receiveOneChatMessage(ctx context.Context, t *testing.T, sess *accountSession) (string, error) {
	t.Helper()
	events := sess.client.Load().Events()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("client event channel closed while waiting for a message")
			}
			msgEv, ok := ev.(xmpp.MessageEvent)
			if !ok {
				continue // presence bursts, receipts, etc. - keep waiting
			}
			dispatchEvent(ctx, nil, 0, sess, msgEv)
			rows, err := sess.db.ListMessagesByRoster(ctx, storage.ListMessagesByRosterParams{
				AccountJid: sess.account.JID,
				RosterJid:  nullString(bareJID(msgEv.From)),
			})
			if err != nil {
				return "", err
			}
			if len(rows) == 0 {
				continue // a non-content event (e.g. key-transport) - keep waiting
			}
			return rows[len(rows)-1].Body.String, nil
		case <-time.After(15 * time.Second):
			t.Fatalf("timed out waiting for a chat message")
		}
	}
}
