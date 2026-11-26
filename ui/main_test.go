package ui

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
)

// TestMain keeps every test's attachment cache in a temporary dir, and
// attachment fetches off the network: only a loopback httptest server is
// reachable. Link previews are off unless a
// test turns them on, and limited to public addresses anyway.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "kage-ui-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	AttachmentsDir = dir
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	dial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, _ := net.SplitHostPort(addr)
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("test dialed %s: only loopback is allowed", addr)
		}
		return dial(ctx, network, addr)
	}
	mediaFetchClient = &http.Client{Transport: transport}
	// Link previews refuse loopback outside tests; publicOnlyDial's own
	// test turns this back off.
	allowLoopbackLinks = true
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
