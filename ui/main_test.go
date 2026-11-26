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
// auto-download - on in every Model New builds - off the network: only a
// loopback httptest server is reachable.
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
	autoDownloadClient = &http.Client{Transport: transport}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
