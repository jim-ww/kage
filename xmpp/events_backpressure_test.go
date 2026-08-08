package xmpp

import (
	"context"
	"testing"
	"time"
)

// TestUndrainedEventsChannelDoesNotDeadlockClient is a regression test for a
// real deadlock: Client.events is a fixed 32-slot buffered channel
// (client.go), and handleStanza used to send directly onto it with a plain
// blocking "events <- ...", no select/default fallback. Session.Serve reads
// the wire in a single goroutine and calls handleStanza synchronously per
// stanza, so once nobody was draining Events() and more than 32 events had
// arrived, the 33rd blocking send stalled that single read loop forever -
// and since it's the same loop that reads EVERY subsequent incoming stanza,
// including IQ *responses* to our own outgoing requests, the entire
// connection went silently, permanently dead: no error, no reconnect
// (nothing ever returned to trigger the supervisor), not even a clean
// Close(). This is a real window in the app, not just a test artifact: see
// account.go's connectAccountLive, which does GPG/OMEMO setup and a roster
// fetch - all while stanzas can already be arriving - before its listener
// ever starts draining Events().
//
// The fix (Client.enqueue/evBuf/forwardEvents in client.go) decouples
// serve()'s stream-reading loop from the pace of whatever reads Events():
// handleStanza now only ever appends to an unbounded internal buffer, which
// can never block it, and a separate forwardEvents goroutine is the only
// thing that ever blocks sending on the public channel.
//
// This test proves the fix holds: flood a connection with more than 32
// events while never reading Events(), then confirm a subsequent unrelated
// IQ round-trip (fetching a device list) on that same connection still
// completes instead of hanging.
func TestUndrainedEventsChannelDoesNotDeadlockClient(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()

	alice, err := Dial(ctx, "alice@localhost", "alicepw", tlsConfig)
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}
	defer alice.Close()
	bob, err := Dial(ctx, "bob@localhost", "bobpw", tlsConfig)
	if err != nil {
		t.Fatalf("dial bob: %v", err)
	}
	defer bob.Close()

	// alice never reads alice.Events() from here on - exactly the window
	// that exists in the real app between Dial and superviseAccount/listen
	// starting (see account.go's connectAccountLive comment at "Start
	// listening... right away, concurrently with syncArchive below - not
	// after it").
	const flood = 40 // > the 32-slot buffer
	for i := 0; i < flood; i++ {
		if _, err := bob.Send(ctx, "alice@localhost", "flood", SendOptions{}); err != nil {
			t.Fatalf("bob send #%d: %v", i, err)
		}
	}

	// Give the stanzas time to actually arrive and back up alice's internal
	// event buffer.
	time.Sleep(500 * time.Millisecond)

	done := make(chan error, 1)
	go func() {
		_, err := alice.FetchOmemoDeviceListV1(ctx, "bob@localhost")
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("FetchOmemoDeviceListV1 after flood: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("FetchOmemoDeviceListV1 never returned - the connection deadlocked " +
			"on an undrained Events() channel (regression)")
	}
}
