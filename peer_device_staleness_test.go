package main

import (
	"context"
	"errors"
	"testing"
	"time"

	omemolib "github.com/jim-ww/omemo-go"

	"github.com/jim-ww/kage/config"
)

// countingSync stands in for Manager.SyncDevices.
type countingSync struct {
	calls int
	err   error
}

func (c *countingSync) sync(context.Context, string) error {
	c.calls++
	return c.err
}

func syncKey(protocol omemolib.Protocol, peer string) string {
	return protocol.String() + "\x00" + peer
}

// TestRefreshPeerDevicesThrottles pins the throttle: without it a busy
// conversation puts a device-list PEP IQ in front of every single message.
func TestRefreshPeerDevicesThrottles(t *testing.T) {
	ctx := context.Background()
	s := &accountSession{account: config.Account{JID: "me@example.test"}}
	sync := &countingSync{}

	// First send on a cold cache must fetch - this is what closes the gap
	// omemo-go leaves: it only auto-fetches when the cache is completely
	// empty, so a stale-but-non-empty list silently encrypts to the wrong
	// devices and reports success.
	refreshPeerDevicesIfStale(ctx, s, omemolib.ProtocolV1, "bob@example.test", sync.sync)
	if sync.calls != 1 {
		t.Fatalf("cold cache: %d syncs, want 1", sync.calls)
	}

	for range 10 {
		refreshPeerDevicesIfStale(ctx, s, omemolib.ProtocolV1, "bob@example.test", sync.sync)
	}
	if sync.calls != 1 {
		t.Errorf("after 10 further sends: %d syncs, want 1 - the throttle is not holding", sync.calls)
	}

	// Each protocol keeps its own device list, so one being fresh says
	// nothing about the other.
	refreshPeerDevicesIfStale(ctx, s, omemolib.ProtocolV2, "bob@example.test", sync.sync)
	if sync.calls != 2 {
		t.Errorf("v2 on a fresh v1: %d syncs, want 2", sync.calls)
	}

	// So does each peer.
	refreshPeerDevicesIfStale(ctx, s, omemolib.ProtocolV1, "carol@example.test", sync.sync)
	if sync.calls != 3 {
		t.Errorf("a second peer: %d syncs, want 3", sync.calls)
	}
}

func TestRefreshPeerDevicesRefetchesOnceStale(t *testing.T) {
	ctx := context.Background()
	s := &accountSession{account: config.Account{JID: "me@example.test"}}
	sync := &countingSync{}
	const peer = "bob@example.test"

	s.peerDeviceSyncedAt = map[string]time.Time{
		syncKey(omemolib.ProtocolV1, peer): time.Now().Add(-peerDeviceSyncInterval - time.Minute),
	}
	refreshPeerDevicesIfStale(ctx, s, omemolib.ProtocolV1, peer, sync.sync)
	if sync.calls != 1 {
		t.Fatalf("a list older than peerDeviceSyncInterval was not re-fetched (%d syncs)", sync.calls)
	}
	if last := s.peerDeviceSyncedAt[syncKey(omemolib.ProtocolV1, peer)]; time.Since(last) > time.Minute {
		t.Errorf("sync timestamp was not refreshed (last = %v)", last)
	}
}

// A server that is slow or refusing must not turn every subsequent message
// into another doomed IQ, so the timestamp is recorded before the fetch
// rather than on success.
func TestRefreshPeerDevicesThrottlesFailedFetchToo(t *testing.T) {
	ctx := context.Background()
	s := &accountSession{account: config.Account{JID: "me@example.test"}}
	sync := &countingSync{err: errors.New("service-unavailable")}

	refreshPeerDevicesIfStale(ctx, s, omemolib.ProtocolV1, "bob@example.test", sync.sync)
	refreshPeerDevicesIfStale(ctx, s, omemolib.ProtocolV1, "bob@example.test", sync.sync)
	if sync.calls != 1 {
		t.Errorf("%d syncs after a failure, want 1", sync.calls)
	}
}
