package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	omemolib "github.com/jim-ww/omemo-go"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/xmpp"
)

// encryptedV1Stanza builds a syntactically valid legacy OMEMO stanza element.
// Its contents are never decrypted by these tests - what matters is that it
// reaches handleIncomingMessage's "omemo isn't ready" branch rather than
// being rejected earlier as undecodable.
func encryptedV1Stanza() *xmpp.OmemoEncryptedElemV1 {
	return xmpp.EncodeOmemoMessageV1(&omemolib.EncryptedMessage{
		Sender:  omemolib.Device{JID: "bob@example.com", ID: 343125113},
		Keys:    []omemolib.RecipientKey{{Device: 1161941825, Data: []byte("wrapped-key")}},
		Payload: []byte("ciphertext"),
		IV:      make([]byte, 12),
	})
}

func newNotReadySession(t *testing.T) *accountSession {
	t.Helper()
	_, q, err := storage.Open(filepath.Join(t.TempDir(), "kage.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	// omemoMgrV1/V2 deliberately left unset: the state an account is in
	// before setupOmemo has ever succeeded.
	return &accountSession{account: config.Account{JID: "alice@example.com"}, db: q}
}

// TestIncomingOmemoMessageNotReadyIsStoredNotDropped is the regression for a
// message vanishing without trace. The live path used to log "omemo isn't
// ready" and return, storing nothing - while the MAM path recorded the exact
// same condition as a visible "[could not be decrypted]" row. A message that
// silently never arrives is indistinguishable from one never sent.
func TestIncomingOmemoMessageNotReadyIsStoredNotDropped(t *testing.T) {
	ctx := context.Background()
	s := newNotReadySession(t)

	handleIncomingMessage(ctx, nil, 0, s, xmpp.MessageEvent{
		ID:          "m1",
		From:        "bob@example.com/phone",
		EncryptedV1: encryptedV1Stanza(),
		SentAt:      time.Now(),
	})

	msgs, _, _ := loadHistoryWindow(ctx, s, "bob@example.com", "bob", nil, 50)
	if len(msgs) != 1 {
		t.Fatalf("stored %d messages, want 1 - the message was dropped", len(msgs))
	}
	// Asserted on the body, not on Message.DecryptFailed: that flag is
	// live-only (it exists to keep a placeholder out of the unread badge)
	// and is deliberately not a stored column, so it reads false here.
	if !strings.Contains(msgs[0].Content, "omemo isn't ready") {
		t.Errorf("stored body = %q, want it to say why it couldn't be decrypted", msgs[0].Content)
	}
}

// The chat has to exist too, or the row is stored against a roster entry
// nothing renders - see ensureChat.
func TestIncomingOmemoMessageNotReadyStillCreatesTheChat(t *testing.T) {
	ctx := context.Background()
	s := newNotReadySession(t)

	handleIncomingMessage(ctx, nil, 0, s, xmpp.MessageEvent{
		ID:          "m1",
		From:        "bob@example.com/phone",
		EncryptedV1: encryptedV1Stanza(),
		SentAt:      time.Now(),
	})

	if _, ok := derefRoster(s.roster.Load())["bob@example.com"]; !ok {
		t.Error("no roster entry was created, so the chat has no row in the list")
	}
}

// TestWaitOmemoReadyReturnsOnceSetupSignals covers the window this wait
// exists for: setupOmemo is several PEP round trips, and a message arriving
// while they're in flight should wait rather than be recorded as a failure.
func TestWaitOmemoReadyReturnsOnceSetupSignals(t *testing.T) {
	s := &accountSession{account: config.Account{JID: "alice@example.com"}, omemoReady: make(chan struct{})}

	go func() {
		time.Sleep(20 * time.Millisecond)
		s.signalOmemoReady()
	}()

	start := time.Now()
	s.waitOmemoReady(context.Background(), 5*time.Second)
	if elapsed := time.Since(start); elapsed >= 5*time.Second {
		t.Fatalf("waited %v; it should have returned as soon as setup signalled", elapsed)
	}
	select {
	case <-s.omemoReady:
	default:
		t.Fatal("waitOmemoReady returned before setup signalled")
	}

	// Idempotent: setupOmemo runs again on every reconnect, and closing an
	// already-closed channel would panic the reconnect goroutine.
	s.signalOmemoReady()
	s.waitOmemoReady(context.Background(), time.Millisecond)
}

func TestWaitOmemoReadySkippedWithoutAChannel(t *testing.T) {
	// A session nothing will ever signal must not wait out the full grace
	// period on every message.
	s := &accountSession{account: config.Account{JID: "alice@example.com"}}
	start := time.Now()
	s.waitOmemoReady(context.Background(), 10*time.Second)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("waited %v on a session with no readiness channel, want an immediate return", elapsed)
	}
	s.signalOmemoReady() // must not panic on a nil channel
}

func TestWaitOmemoReadyHonoursContext(t *testing.T) {
	s := &accountSession{account: config.Account{JID: "alice@example.com"}, omemoReady: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	s.waitOmemoReady(ctx, 10*time.Second)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("waited %v after ctx was cancelled, want an immediate return", elapsed)
	}
}
