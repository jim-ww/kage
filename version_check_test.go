package main

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jim-ww/kage/ipc"
)

// daemonAnswering serves handler on a fresh socket and dials it.
func daemonAnswering(t *testing.T, handler ipc.Handler) *ipc.Conn {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "kage.sock")
	ln, err := ipc.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go ipc.NewServer().Accept(ln, handler)
	conn, err := ipc.Dial(sock, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestCheckDaemonVersion(t *testing.T) {
	versionCheckTimeout = 200 * time.Millisecond

	same := daemonAnswering(t, func(ipc.ClientID, any) (any, error) { return thisBuild, nil })
	if notice, err := checkDaemonVersion(same); notice != "" || err != nil {
		t.Errorf("same build: notice %q, err %v", notice, err)
	}

	other := buildVersion{Version: "0.1.0", Protocol: "000000000000"}
	different := daemonAnswering(t, func(ipc.ClientID, any) (any, error) { return other, nil })
	if notice, err := checkDaemonVersion(different); err != nil || !strings.Contains(notice, other.String()) {
		t.Errorf("different build: notice %q, err %v", notice, err)
	}

	silent := daemonAnswering(t, func(ipc.ClientID, any) (any, error) { select {} })
	if _, err := checkDaemonVersion(silent); err == nil {
		t.Error("a daemon that never answers passed the check")
	}
}

// TestProtocolHashTracksWireTypes checks every wire type counts toward the
// protocol hash.
func TestProtocolHashTracksWireTypes(t *testing.T) {
	before := protocolHash()
	type extra struct{ Field int }
	saved := wireTypes
	t.Cleanup(func() { wireTypes = saved })
	wireTypes = append(append([]reflect.Type(nil), saved...), reflect.TypeFor[extra]())
	if protocolHash() == before {
		t.Fatal("adding a wire type didn't change the protocol hash")
	}
	if thisBuild.Protocol != before {
		t.Fatalf("thisBuild.Protocol = %s, want %s", thisBuild.Protocol, before)
	}
}
