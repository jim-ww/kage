//go:build integration

package main

import (
	"context"
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
func attachTestIPCClient(t *testing.T, sockPath string) (*ipcClient, chan any) {
	t.Helper()
	events := make(chan any, 32)
	client := &ipcClient{events: events}
	conn, err := ipc.Dial(sockPath, client.handleEvent)
	if err != nil {
		t.Fatalf("dialing daemon socket: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	client.conn = conn
	return client, events
}

// newTestDaemon spins up a real adapter+daemonServer+ipc.Server (the same
// wiring --background mode builds, see daemon_ipc_test.go) around a live
// alice@localhost session, listening on a fresh Unix socket. Returns the
// socket path for attachTestIPCClient to dial, and the accountSession itself
// so callers can seed storage (e.g. chat encryption mode) before connecting
// clients.
func newTestDaemon(t *testing.T, aliceClient *xmpp.Client) (sockPath string, sess *accountSession) {
	t.Helper()
	sockPath, sess, _ = newTestDaemonWithServer(t, aliceClient)
	return sockPath, sess
}

// newTestDaemonWithServer is newTestDaemon also handing back the ipc.Server,
// for a test that runs the account's event loop (listen) against it.
func newTestDaemonWithServer(t *testing.T, aliceClient *xmpp.Client) (sockPath string, sess *accountSession, srv *ipc.Server) {
	t.Helper()
	dir := t.TempDir()
	dbConn, aliceDB, err := storage.Open(filepath.Join(dir, "alice.db"))
	if err != nil {
		t.Fatalf("open alice storage: %v", err)
	}
	t.Cleanup(func() { dbConn.Close() })

	srv = ipc.NewServer()
	aliceSess := &accountSession{
		account:    config.Account{JID: "alice@localhost", Password: "alicepw"},
		db:         aliceDB,
		accountIdx: 0,
	}
	aliceSess.client.Store(aliceClient)

	a := &adapter{sessions: []*accountSession{aliceSess}, srv: srv, cfgPath: filepath.Join(dir, "config.toml")}
	ds := &daemonServer{a: a, srv: srv}

	sockPath = filepath.Join(dir, "kage.sock")
	ln, err := ipc.Listen(sockPath)
	if err != nil {
		t.Fatalf("listening on socket: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go srv.Accept(ln, ds.serve)
	t.Cleanup(srv.Close)

	return sockPath, aliceSess, srv
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

	res, err := do(clientA.conn, sendReq{AccountIdx: 0, To: "bob@localhost", Body: "hello from instance A"})
	if err != nil {
		t.Fatalf("Send RPC: %v", err)
	}
	if res.Queued || res.ID == "" {
		t.Fatalf("send result = %+v, want a real (non-queued) message ID", res)
	}

	for name, events := range map[string]chan any{"B (sibling)": eventsB, "A (sender)": eventsA} {
		var got ui.IncomingMessageMsg
		awaitEvent(t, events, &got, 10*time.Second)
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
	do(clientA.conn, uploadFileReq{AccountIdx: 0, To: "bob@localhost", Path: path})

	var done ui.FileTransferDoneMsg
	awaitEvent(t, eventsB, &done, 15*time.Second)
	if done.ID != path {
		t.Errorf("client B: FileTransferDoneMsg.ID = %q, want %q", done.ID, path)
	}
}

// TestOwnEditsReachOtherAttachedClients is the same multi-TUI regression as
// TestSendNotifiesOtherAttachedClients, for what's done to a message after
// it's sent: an edit, a reaction, a delete (retraction) and the local-only
// delete of someone else's message all used to update storage and the TUI
// that did it, and nothing else - a second attached TUI kept showing the
// old text until it reloaded from storage.
func TestOwnEditsReachOtherAttachedClients(t *testing.T) {
	ctx := context.Background()
	aliceClient, err := xmpp.Dial(ctx, "alice@localhost", "alicepw", devtestTLSConfig(t))
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}
	t.Cleanup(func() { aliceClient.Close() })

	sockPath, aliceSess := newTestDaemon(t, aliceClient)
	if err := aliceSess.db.SetChatEncryptionMode(ctx, storage.SetChatEncryptionModeParams{
		AccountJid: "alice@localhost", RosterJid: "bob@localhost", Mode: "none",
	}); err != nil {
		t.Fatalf("SetChatEncryptionMode: %v", err)
	}
	clientA, _ := attachTestIPCClient(t, sockPath)
	_, eventsB := attachTestIPCClient(t, sockPath)

	sent, err := do(clientA.conn, sendReq{AccountIdx: 0, To: "bob@localhost", Body: "typo"})
	if err != nil || sent.ID == "" {
		t.Fatalf("Send: %+v %v", sent, err)
	}

	if _, err := do(clientA.conn, sendReq{AccountIdx: 0, To: "bob@localhost", Body: "fixed",
		Opts: ui.SendOptions{ReplaceID: sent.ID}}); err != nil {
		t.Fatalf("correction: %v", err)
	}
	var corrected ui.MessageCorrectedMsg
	awaitEvent(t, eventsB, &corrected, 10*time.Second)
	if corrected.ReplaceID != sent.ID || corrected.NewContent != "fixed" || corrected.From != "bob@localhost" {
		t.Fatalf("correction seen by B: %+v", corrected)
	}

	if _, err := do(clientA.conn, sendReq{AccountIdx: 0, To: "bob@localhost",
		Opts: ui.SendOptions{ReactionTargetID: sent.ID, Reactions: []string{"👍"}}}); err != nil {
		t.Fatalf("reaction: %v", err)
	}
	var reactions ui.MessageReactionsMsg
	awaitEvent(t, eventsB, &reactions, 10*time.Second)
	if reactions.MessageID != sent.ID || len(reactions.Reactions) == 0 {
		t.Fatalf("reaction seen by B: %+v", reactions)
	}

	if _, err := do(clientA.conn, sendReq{AccountIdx: 0, To: "bob@localhost",
		Opts: ui.SendOptions{RetractID: sent.ID}}); err != nil {
		t.Fatalf("retraction: %v", err)
	}
	var retracted ui.MessageRetractedMsg
	awaitEvent(t, eventsB, &retracted, 10*time.Second)
	if retracted.RetractID != sent.ID {
		t.Fatalf("retraction seen by B: %+v", retracted)
	}

	if err := clientA.MarkRetracted(0, "bob@localhost", "someone-elses-message"); err != nil {
		t.Fatalf("MarkRetracted: %v", err)
	}
	awaitEvent(t, eventsB, &retracted, 10*time.Second)
	if retracted.RetractID != "someone-elses-message" {
		t.Fatalf("local delete seen by B: %+v", retracted)
	}
}

// TestSettingsReachOtherAttachedClients: a chat's encryption mode, pin,
// mute and hidden state, and the account's own status, changed from one
// attached TUI must show in the others too.
func TestSettingsReachOtherAttachedClients(t *testing.T) {
	ctx := context.Background()
	aliceClient, err := xmpp.Dial(ctx, "alice@localhost", "alicepw", devtestTLSConfig(t))
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}
	t.Cleanup(func() { aliceClient.Close() })
	// SetAccountStatus signals "the" daemon to reload, found through the
	// config dir's lock file - keep that away from any real one.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	sockPath, _ := newTestDaemon(t, aliceClient)

	clientA, _ := attachTestIPCClient(t, sockPath)
	_, eventsB := attachTestIPCClient(t, sockPath)

	if err := clientA.SetChatEncryption(0, "bob@localhost", "none"); err != nil {
		t.Fatal(err)
	}
	var setting ui.ChatSettingMsg
	awaitEvent(t, eventsB, &setting, 5*time.Second)
	if setting.Setting != ui.ChatEncryption || setting.Mode != "none" || setting.ChatAddress != "bob@localhost" {
		t.Fatalf("encryption seen by B: %+v", setting)
	}

	if msg := clientA.SetAccountStatus(0, ui.PresenceAway).(ui.AccountStatusSetMsg); msg.Err != nil {
		t.Fatalf("SetAccountStatus: %v", msg.Err)
	}
	var status ui.AccountStatusChangedMsg
	awaitEvent(t, eventsB, &status, 5*time.Second)
	if status.Status != ui.PresenceAway {
		t.Fatalf("status seen by B: %+v", status)
	}
}

// TestBouncedMessageMarkedFailed checks a message the recipient's side
// bounces is marked failed, with the reason, in every attached TUI - not
// left looking sent, and not read as the recipient talking to us.
func TestBouncedMessageMarkedFailed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	aliceClient, err := xmpp.Dial(ctx, "alice@localhost", "alicepw", devtestTLSConfig(t))
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}
	t.Cleanup(func() { aliceClient.Close() })
	sockPath, aliceSess, srv := newTestDaemonWithServer(t, aliceClient)
	go listen(ctx, srv, 0, aliceSess)

	const to = "nobody-here@localhost"
	if err := aliceSess.db.SetChatEncryptionMode(ctx, storage.SetChatEncryptionModeParams{
		AccountJid: "alice@localhost", RosterJid: to, Mode: "none",
	}); err != nil {
		t.Fatal(err)
	}
	clientA, _ := attachTestIPCClient(t, sockPath)
	_, eventsB := attachTestIPCClient(t, sockPath)

	sent, err := do(clientA.conn, sendReq{AccountIdx: 0, To: to, Body: "anyone there?"})
	if err != nil {
		t.Fatal(err)
	}
	var failed ui.MessageSendFailedMsg
	awaitEvent(t, eventsB, &failed, 10*time.Second)
	if failed.MessageID != sent.ID || failed.Reason == "" {
		t.Fatalf("failure seen by B: %+v (sent %s)", failed, sent.ID)
	}
}
