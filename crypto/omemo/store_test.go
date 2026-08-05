package omemo

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	omemolib "github.com/jim-ww/omemo-go"

	"github.com/jim-ww/kage/storage"
)

// fakeTransport is an in-memory omemolib.Transport shared by both ends of a
// test conversation, standing in for XEP-0384 PEP publish/fetch.
type fakeTransport struct {
	mu      sync.Mutex
	devices map[string]omemolib.DeviceList
	bundles map[omemolib.Device]omemolib.Bundle
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{
		devices: make(map[string]omemolib.DeviceList),
		bundles: make(map[omemolib.Device]omemolib.Bundle),
	}
}

func (t *fakeTransport) FetchDeviceList(_ context.Context, jid string) (omemolib.DeviceList, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.devices[jid], nil
}

func (t *fakeTransport) PublishDeviceList(_ context.Context, list omemolib.DeviceList) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.devices[list.JID] = list
	return nil
}

func (t *fakeTransport) FetchBundle(_ context.Context, dev omemolib.Device) (omemolib.Bundle, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.bundles[dev], nil
}

func (t *fakeTransport) PublishBundle(_ context.Context, bundle omemolib.Bundle) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.bundles[bundle.Device] = bundle
	return nil
}

func openTestDB(t *testing.T) *storage.Queries {
	t.Helper()
	_, db, err := storage.Open(filepath.Join(t.TempDir(), "kage.db"))
	if err != nil {
		t.Fatalf("opening test database: %v", err)
	}
	return db
}

// TestStoreEndToEndConversation exercises Store (backed by a real sqlite
// database, via the same schema kage ships) through a full OMEMO exchange,
// for both protocols: alice sends bob a first message (X3DH + key-exchange),
// bob decrypts and replies on the now-established session.
func TestStoreEndToEndConversation(t *testing.T) {
	for _, protocol := range []omemolib.Protocol{omemolib.ProtocolV2, omemolib.ProtocolV1} {
		t.Run(protocol.String(), func(t *testing.T) { testStoreEndToEndConversation(t, protocol) })
	}
}

func testStoreEndToEndConversation(t *testing.T, protocol omemolib.Protocol) {
	ctx := context.Background()
	db := openTestDB(t)
	transport := newFakeTransport()

	aliceJID, bobJID := "alice@example.com", "bob@example.com"
	aliceStore := NewStore(db, aliceJID, protocol)
	bobStore := NewStore(db, bobJID, protocol)

	if err := omemolib.InitIdentity(ctx, aliceStore, aliceJID, 1, protocol); err != nil {
		t.Fatalf("InitIdentity(alice): %v", err)
	}
	if err := omemolib.InitIdentity(ctx, bobStore, bobJID, 1, protocol); err != nil {
		t.Fatalf("InitIdentity(bob): %v", err)
	}

	trustAll := omemolib.WithTrustResolver(func(ctx context.Context, dev omemolib.Device, identityKey []byte) error { return nil })
	aliceMgr, err := omemolib.NewManager(ctx, aliceStore, transport, protocol, trustAll)
	if err != nil {
		t.Fatalf("NewManager(alice): %v", err)
	}
	bobMgr, err := omemolib.NewManager(ctx, bobStore, transport, protocol, trustAll)
	if err != nil {
		t.Fatalf("NewManager(bob): %v", err)
	}

	if err := aliceMgr.PublishBundle(ctx); err != nil {
		t.Fatalf("PublishBundle(alice): %v", err)
	}
	if err := bobMgr.PublishBundle(ctx); err != nil {
		t.Fatalf("PublishBundle(bob): %v", err)
	}
	if err := transport.PublishDeviceList(ctx, omemolib.DeviceList{JID: bobJID, Devices: []omemolib.DeviceID{bobMgr.LocalDevice().ID}}); err != nil {
		t.Fatalf("publishing bob's device list: %v", err)
	}
	if err := transport.PublishDeviceList(ctx, omemolib.DeviceList{JID: aliceJID, Devices: []omemolib.DeviceID{aliceMgr.LocalDevice().ID}}); err != nil {
		t.Fatalf("publishing alice's device list: %v", err)
	}

	msg, deviceErrs, err := aliceMgr.EncryptMessage(ctx, bobJID, []byte("hello bob"))
	if err != nil {
		t.Fatalf("EncryptMessage: %v (device errors: %v)", err, deviceErrs)
	}

	plaintext, err := bobMgr.DecryptMessage(ctx, msg)
	if err != nil {
		t.Fatalf("DecryptMessage: %v", err)
	}
	if string(plaintext) != "hello bob" {
		t.Fatalf("plaintext = %q, want %q", plaintext, "hello bob")
	}

	// Reply on the now-established session (no KeyExchange this time).
	reply, _, err := bobMgr.EncryptMessage(ctx, aliceJID, []byte("hi alice"))
	if err != nil {
		t.Fatalf("EncryptMessage (reply): %v", err)
	}
	if len(reply.Keys) != 1 || reply.Keys[0].KeyExchange != nil {
		t.Fatalf("reply should reuse the established session, got KeyExchange = %+v", reply.Keys)
	}

	replyPlaintext, err := aliceMgr.DecryptMessage(ctx, reply)
	if err != nil {
		t.Fatalf("DecryptMessage (reply): %v", err)
	}
	if string(replyPlaintext) != "hi alice" {
		t.Fatalf("reply plaintext = %q, want %q", replyPlaintext, "hi alice")
	}
}
