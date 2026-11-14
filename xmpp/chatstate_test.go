//go:build integration

package xmpp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// devtestTLSConfig loads the local Prosody test instance's self-signed cert
// (see devtest/prosody/README.md), skipping the test if it hasn't been set
// up or isn't currently running. Mirrors the same-named helper in the root
// package's pep_gpg_test.go — duplicated rather than shared because the two
// live in different packages/working directories and this one is small.
func devtestTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	certPEM, err := os.ReadFile("../devtest/prosody/certs/localhost.crt")
	if err != nil {
		t.Skipf("devtest/prosody not set up (run setup.sh + serve.sh): %v", err)
	}
	conn, err := net.DialTimeout("tcp", "localhost:5222", 500*time.Millisecond)
	if err != nil {
		t.Skipf("devtest/prosody not running (run serve.sh): %v", err)
	}
	conn.Close()
	lockDevtest(t)
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	return &tls.Config{ServerName: "localhost", RootCAs: pool}
}

// devtestLocked is held while this process's tests use the devtest server
// (see lockDevtest): how many are, and the open lock file.
var devtestLocked struct {
	sync.Mutex
	users int
	file  *os.File
}

// lockDevtest gives the test the devtest server to itself until it ends:
// every package's live tests log in as the same alice and bob, and
// `go test ./...` runs packages at once, so one's avatar removal, roster
// change or archive sync would land in the middle of another's. Held per
// process, as tests within one run one at a time.
func lockDevtest(t *testing.T) {
	t.Helper()
	devtestLocked.Lock()
	defer devtestLocked.Unlock()
	if devtestLocked.users == 0 {
		f, err := os.OpenFile(filepath.Join(os.TempDir(), "kage-devtest-prosody.lock"), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatalf("opening devtest lock: %v", err)
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			f.Close()
			t.Fatalf("taking devtest lock: %v", err)
		}
		devtestLocked.file = f
	}
	devtestLocked.users++
	t.Cleanup(func() {
		devtestLocked.Lock()
		defer devtestLocked.Unlock()
		if devtestLocked.users--; devtestLocked.users == 0 {
			devtestLocked.file.Close() // releases the lock
			devtestLocked.file = nil
		}
	})
}

// TestChatStateSendReceive verifies the XEP-0085 chat state notification
// round-trips over a real connection: alice sends "composing" to bob, bob's
// Events channel receives a ChatStateEvent with that state, and stopping
// (active) round-trips the same way.
func TestChatStateSendReceive(t *testing.T) {
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

	if err := alice.SendChatState(ctx, "bob@localhost", ChatStateComposing); err != nil {
		t.Fatalf("SendChatState(composing): %v", err)
	}
	ev := waitForChatState(t, bob, 5*time.Second)
	if ev.State != ChatStateComposing {
		t.Fatalf("got state %v, want ChatStateComposing", ev.State)
	}

	if err := alice.SendChatState(ctx, "bob@localhost", ChatStateActive); err != nil {
		t.Fatalf("SendChatState(active): %v", err)
	}
	ev = waitForChatState(t, bob, 5*time.Second)
	if ev.State != ChatStateActive {
		t.Fatalf("got state %v, want ChatStateActive", ev.State)
	}
}

// waitForChatState reads bob's Events channel until a ChatStateEvent shows
// up (other event types, like our own presence broadcast, are ignored) or
// the timeout elapses.
func waitForChatState(t *testing.T, c *Client, timeout time.Duration) ChatStateEvent {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-c.Events():
			if !ok {
				t.Fatal("events channel closed before a ChatStateEvent arrived")
			}
			if cs, ok := ev.(ChatStateEvent); ok {
				return cs
			}
		case <-deadline:
			t.Fatal("timed out waiting for a ChatStateEvent")
		}
	}
}
