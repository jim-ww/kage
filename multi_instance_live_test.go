//go:build integration

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/xmpp"
)

// attachTestIPCClient dials sockPath and returns an ipcClient wired to
// capture every broadcast Event verbatim on the returned channel, bypassing
// the real dispatchLoop/tea.Program plumbing (see ipc_client.go) - this test
// only needs to inspect what the daemon broadcasts, not run a real TUI.
func attachTestIPCClient(t *testing.T, sockPath string) (*ipcClient, chan ipc.Event) {
	t.Helper()
	events := make(chan ipc.Event, 32)
	client := &ipcClient{events: events}
	conn, err := ipc.Dial(sockPath, client.handleEvent)
	if err != nil {
		t.Fatalf("dialing daemon socket: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	client.conn = conn
	return client, events
}

// awaitEvent reads from events until it finds one of kind, decoding its Data
// into out - or fails the test after timeout. Events of other kinds (e.g.
// this same message's own broadcasts arriving in an order the test doesn't
// care about) are skipped rather than treated as a mismatch.
func awaitEvent(t *testing.T, events chan ipc.Event, kind string, out any, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-events:
			if ev.Kind != kind {
				continue
			}
			if err := json.Unmarshal(ev.Data, out); err != nil {
				t.Fatalf("unmarshaling %s event: %v", kind, err)
			}
			return
		case <-deadline:
			t.Fatalf("timed out waiting for a %s event", kind)
		}
	}
}

// newTestDaemon spins up a real adapter+daemonServer+ipc.Server (the same
// wiring --background mode builds, see daemon_ipc_test.go) around a live
// alice@localhost session, listening on a fresh Unix socket. Returns the
// socket path for attachTestIPCClient to dial, and the accountSession itself
// so callers can seed storage (e.g. chat encryption mode) before connecting
// clients.
func newTestDaemon(t *testing.T, aliceClient *xmpp.Client) (sockPath string, sess *accountSession) {
	t.Helper()
	dir := t.TempDir()
	dbConn, aliceDB, err := storage.Open(filepath.Join(dir, "alice.db"))
	if err != nil {
		t.Fatalf("open alice storage: %v", err)
	}
	t.Cleanup(func() { dbConn.Close() })

	srv := ipc.NewServer()
	aliceSess := &accountSession{
		account:    config.Account{JID: "alice@localhost", Password: "alicepw"},
		db:         aliceDB,
		accountIdx: 0,
	}
	aliceSess.client.Store(aliceClient)

	a := &adapter{sessions: []*accountSession{aliceSess}, srv: srv}
	ds := &daemonServer{a: a, srv: srv}

	sockPath = filepath.Join(dir, "kage.sock")
	ln, err := ipc.Listen(sockPath)
	if err != nil {
		t.Fatalf("listening on socket: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go srv.Accept(ln, ds.handle)
	t.Cleanup(srv.Close)

	return sockPath, aliceSess
}

// TestSendNotifiesOtherAttachedClients is a live-server regression test for
// kage's daemon+multiple-TUI-client architecture (cmd_daemon.go: "the one
// process per user session that owns every account's XMPP connection"). More
// than one `kage` process can attach to the same background daemon over its
// Unix socket at once, all sharing one XMPP connection per account.
//
// Before the fix, adapter.go's send() persisted a successful, immediate
// (non-outbox) send to storage and returned - broadcasting nothing. The
// client that issued the Send RPC renders its own local optimistic echo the
// instant the RPC returns (see ui/message_actions.go's sendCurrentInput), so
// it never noticed; every OTHER attached client just never learned the
// message existed until it happened to reload from storage (e.g. restart).
//
// This drives two attached IPC clients against one real daemon+live account:
// client A sends, client B - which never touched that call - must still see
// an IncomingMessage broadcast with IsMe true and the right content. Client
// A must too (the fix broadcasts unconditionally to every attached client,
// including the sender - see adapter.go's send() and the ID-dedup this adds
// to ui/update_messages.go's IncomingMessageMsg handling, which is what
// stops that from double-rendering the sender's own optimistic echo).
func TestSendNotifiesOtherAttachedClients(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()

	aliceClient, err := xmpp.Dial(ctx, "alice@localhost", "alicepw", tlsConfig)
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}
	t.Cleanup(func() { aliceClient.Close() })

	sockPath, aliceSess := newTestDaemon(t, aliceClient)

	// This test is about the daemon->attached-clients broadcast, not
	// encryption - keep the send unencrypted so it doesn't also need a real
	// OMEMO setup (resolveEncryptionMode otherwise defaults new chats to
	// omemo-v1, which send() refuses to fall back to plaintext for).
	if err := aliceSess.db.SetChatEncryptionMode(ctx, storage.SetChatEncryptionModeParams{
		AccountJid: "alice@localhost", RosterJid: "bob@localhost", Mode: "none",
	}); err != nil {
		t.Fatalf("SetChatEncryptionMode: %v", err)
	}

	clientA, eventsA := attachTestIPCClient(t, sockPath)
	_, eventsB := attachTestIPCClient(t, sockPath)

	var res sendResult
	if err := clientA.conn.Call(rpcSend, sendParams{
		AccountIdx: 0, To: "bob@localhost", Body: "hello from instance A",
	}, &res); err != nil {
		t.Fatalf("Send RPC: %v", err)
	}
	if res.Queued || res.ID == "" {
		t.Fatalf("send result = %+v, want a real (non-queued) message ID", res)
	}

	for name, events := range map[string]chan ipc.Event{"B (sibling)": eventsB, "A (sender)": eventsA} {
		var got ui.IncomingMessageMsg
		awaitEvent(t, events, evIncomingMessage, &got, 10*time.Second)
		if got.AccountIdx != 0 || got.From != "bob@localhost" {
			t.Errorf("client %s: AccountIdx/From = %d/%q, want 0/bob@localhost", name, got.AccountIdx, got.From)
		}
		if !got.Message.IsMe {
			t.Errorf("client %s: Message.IsMe = false, want true", name)
		}
		if got.Message.Content != "hello from instance A" {
			t.Errorf("client %s: Message.Content = %q, want %q", name, got.Message.Content, "hello from instance A")
		}
		if got.Message.ID != res.ID {
			t.Errorf("client %s: Message.ID = %q, want %q (the RPC's own returned ID)", name, got.Message.ID, res.ID)
		}
	}
}

// TestFileTransferDoneReachesOtherAttachedClients is a live-server
// regression test for the second, related daemon+multi-client bug: an
// upload's progress (adapter.go's progressCallback) already broadcasts to
// every attached client, but before this fix nothing told a client other
// than the one that made the SendFile/UploadFile RPC when the transfer
// actually finished - ui/transfer.go's clearTransfer only ever ran on the
// initiating client, off its own direct RPC return. Every other attached
// client's progress bar for that transfer therefore stuck at whatever
// percentage it last saw, forever (in practice: 100%, since the last
// progress broadcast a real upload sends is always the final chunk).
//
// devtest/prosody has no XEP-0363 upload service configured, so the upload
// itself fails fast during slot discovery - deliberately: this test isn't
// about the upload succeeding, it's about the daemon telling every attached
// client the transfer reached ITS terminal state (whatever that state is)
// unconditionally, on the exact same line for both outcomes (see adapter.go's
// SendFile/UploadFile) - so exercising the failure path here covers the
// success path's broadcast too.
func TestFileTransferDoneReachesOtherAttachedClients(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()

	aliceClient, err := xmpp.Dial(ctx, "alice@localhost", "alicepw", tlsConfig)
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}
	t.Cleanup(func() { aliceClient.Close() })

	sockPath, _ := newTestDaemon(t, aliceClient)

	clientA, _ := attachTestIPCClient(t, sockPath)
	_, eventsB := attachTestIPCClient(t, sockPath)

	path := filepath.Join(t.TempDir(), "attachment.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	// Expected to fail (no upload service on devtest/prosody) - what matters
	// is client B still hears about it.
	var res ui.FileUploadResultMsg
	_ = clientA.conn.Call(rpcUploadFile, uploadFileParams{
		AccountIdx: 0, To: "bob@localhost", Path: path,
	}, &res)

	var done ui.FileTransferDoneMsg
	awaitEvent(t, eventsB, evFileTransferDone, &done, 15*time.Second)
	if done.ID != path {
		t.Errorf("client B: FileTransferDoneMsg.ID = %q, want %q", done.ID, path)
	}
}
