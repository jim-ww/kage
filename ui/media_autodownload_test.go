package ui

import (
	"bytes"
	"image"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// pngServer serves one PNG at any path, counting requests.
func pngServer(t *testing.T, w, h int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var buf bytes.Buffer
	if err := encodePNG(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		rw.Header().Set("Content-Type", "image/png")
		rw.Write(buf.Bytes())
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// runBatch runs cmd, a tea.Batch or a single command, feeding every
// resulting message to m and running what that returns in turn - only
// media work, which is all these tests' Updates return.
func runBatch(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			m = runBatch(t, m, c)
		}
		return m
	}
	next, cmd := m.Update(msg)
	return runBatch(t, next.(Model), cmd)
}

// TestAutoDownloadFetchesThenPreviews: a received image that isn't cached
// is downloaded once, then decoded and drawn.
func TestAutoDownloadFetchesThenPreviews(t *testing.T) {
	withMediaCache(t)
	srv, hits := pngServer(t, 32, 32)
	url := srv.URL + "/cat.png"
	m := newTestModelWithMessages([]Message{{ID: "a", Content: url, Attachments: []string{url}}}, &stubDaemon{})
	m = runBatch(t, m, takeMediaThumbRequests()) // download, then decode
	if _, err := os.Stat(attachmentLocalPath(url, "bob@example.test")); err != nil {
		t.Fatalf("not cached after auto-download: %v", err)
	}
	if !strings.Contains(strings.Join(m.viewportLines, "\n"), upperHalfBlock) {
		t.Fatal("auto-downloaded image not previewed")
	}
	m.refreshViewport()
	if takeMediaThumbRequests() != nil || hits.Load() != 1 {
		t.Fatalf("fetched %d times, or more work queued", hits.Load())
	}
}

// TestAutoDownloadSizeCap: a file over the cap isn't cached, and isn't
// asked for again.
func TestAutoDownloadSizeCap(t *testing.T) {
	withMediaCache(t)
	srv, hits := pngServer(t, 32, 32)
	url := srv.URL + "/big.png"
	if err := autoDownload(url, "bob@example.test", 10); err == nil {
		t.Fatal("file over the cap was downloaded")
	}
	if _, err := os.Stat(attachmentLocalPath(url, "bob@example.test")); err == nil {
		t.Fatal("file over the cap left in the cache")
	}
	queueMediaDownload(url, "bob@example.test")
	queueMediaDownload(url, "bob@example.test")
	if n := len(takeMediaDownloadRequests()); n != 1 || hits.Load() != 1 {
		t.Fatalf("queued %d downloads after %d fetches", n, hits.Load())
	}
}

// TestAutoDownloadDisabled: with auto_download_disabled nothing is fetched.
func TestAutoDownloadDisabled(t *testing.T) {
	withMediaCache(t)
	srv, hits := pngServer(t, 8, 8)
	url := srv.URL + "/cat.png"
	m := newTestModelWithMessages(nil, &stubDaemon{})
	setAutoDownload(false, 0) // what New does for AutoDownloadDisabled
	t.Cleanup(func() { setAutoDownload(true, 0) })
	m.accounts[0].Messages[0] = []Message{{ID: "a", Content: url, Attachments: []string{url}}}
	m.refreshViewport()
	if takeMediaThumbRequests() != nil || hits.Load() != 0 {
		t.Fatal("auto-download disabled, yet something was fetched or queued")
	}
}
