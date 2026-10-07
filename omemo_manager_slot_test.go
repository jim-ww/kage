package main

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	omemolib "github.com/jim-ww/omemo-go"

	"github.com/jim-ww/kage/config"
	kageomemo "github.com/jim-ww/kage/crypto/omemo"
	"github.com/jim-ww/kage/storage"
)

func newTestManager(t *testing.T, jid string) *omemolib.Manager {
	t.Helper()
	ctx := context.Background()
	_, q, err := storage.Open(filepath.Join(t.TempDir(), "omemo.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	store := kageomemo.NewStore(q, jid, omemolib.ProtocolV1)
	id, err := omemolib.GenerateDeviceID()
	if err != nil {
		t.Fatalf("GenerateDeviceID: %v", err)
	}
	if err := omemolib.InitIdentity(ctx, store, jid, id, omemolib.ProtocolV1); err != nil {
		t.Fatalf("InitIdentity: %v", err)
	}
	mgr, err := omemolib.NewManager(ctx, store, nil, omemolib.ProtocolV1)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr
}

// TestStoreOmemoManagerKeepsWorkingManagerOnFailure is the regression for a
// transient reconnect failure permanently disabling encryption: setupOmemo
// runs on every reconnect and setupOmemoProtocol returns nil when it can't
// build a Manager, which used to be assigned straight over a working one.
// Nothing rebuilds it until the next reconnect, so until then every incoming
// OMEMO message is dropped, every MAM item is stored as a permanent
// "[could not be decrypted]" row, and every send is refused.
func TestStoreOmemoManagerKeepsWorkingManagerOnFailure(t *testing.T) {
	s := &accountSession{account: config.Account{JID: "me@example.test"}}
	good := newTestManager(t, "me@example.test")

	storeOmemoManager(s, &s.omemoMgrV1, omemolib.ProtocolV1, good)
	if s.omemoV1() != good {
		t.Fatal("a successful setup did not publish its manager")
	}

	storeOmemoManager(s, &s.omemoMgrV1, omemolib.ProtocolV1, nil)
	if s.omemoV1() != good {
		t.Error("a failed setup wiped the previously working manager; the account can no longer send or decrypt until the next reconnect")
	}
}

func TestStoreOmemoManagerReplacesOnSuccess(t *testing.T) {
	s := &accountSession{account: config.Account{JID: "me@example.test"}}
	first := newTestManager(t, "me@example.test")
	second := newTestManager(t, "me@example.test")

	storeOmemoManager(s, &s.omemoMgrV1, omemolib.ProtocolV1, first)
	storeOmemoManager(s, &s.omemoMgrV1, omemolib.ProtocolV1, second)
	if got := s.omemoV1(); got != second {
		t.Error("a reconnect's freshly built manager did not replace the one bound to the dead client")
	}
}

// TestOmemoManagerSlotIsRaceFree is meaningful only under -race: it pins the
// reason these are atomics rather than plain fields. reconnectWithBackoff
// republishes both managers from its own goroutine while the event loop,
// the MAM backfill goroutines and every Send RPC read them.
func TestOmemoManagerSlotIsRaceFree(t *testing.T) {
	s := &accountSession{account: config.Account{JID: "me@example.test"}}
	mgrs := []*omemolib.Manager{newTestManager(t, "me@example.test"), newTestManager(t, "me@example.test")}

	var wg sync.WaitGroup
	var stop atomic.Bool
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			_ = s.omemoManagerFor(omemolib.ProtocolV1)
			_ = s.omemoManagerFor(omemolib.ProtocolV2)
		}
	}()
	for i := range 2000 {
		storeOmemoManager(s, &s.omemoMgrV1, omemolib.ProtocolV1, mgrs[i%len(mgrs)])
		storeOmemoManager(s, &s.omemoMgrV2, omemolib.ProtocolV2, mgrs[i%len(mgrs)])
	}
	stop.Store(true)
	wg.Wait()
}
