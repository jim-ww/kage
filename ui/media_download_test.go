package ui

import (
	"bytes"
	"image"
	"net/http"
	"net/http/httptest"
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

// TestRenderNeverFetches: an uncached image is drawn as its name alone,
// with nothing asked of the network.
func TestRenderNeverFetches(t *testing.T) {
	withMediaCache(t)
	srv, hits := pngServer(t, 8, 8)
	url := srv.URL + "/cat.png"
	m := newTestModelWithMessages([]Message{{ID: "a", Content: url, Attachments: []string{url}}}, &stubDaemon{})
	m.refreshViewport()
	if takeMediaThumbRequests() != nil || hits.Load() != 0 {
		t.Fatal("render fetched or queued an uncached attachment")
	}
}

// TestAttachmentCachedDrawsPreview: the daemon's report of an
// auto-downloaded attachment gets the open chat's preview drawn.
func TestAttachmentCachedDrawsPreview(t *testing.T) {
	withMediaCache(t)
	srv, _ := pngServer(t, 32, 32)
	url := srv.URL + "/cat.png"
	m := newTestModelWithMessages([]Message{{ID: "a", Content: url, Attachments: []string{url}}}, &stubDaemon{})
	if err := DownloadToCache(url, "bob@example.test", 1<<20); err != nil {
		t.Fatal(err)
	}
	next, cmd := m.Update(AttachmentCachedMsg{From: "bob@example.test", Target: url})
	m = runBatch(t, next.(Model), cmd)
	if !strings.Contains(strings.Join(m.viewportLines, "\n"), upperHalfBlock) {
		t.Fatal("cached attachment not previewed")
	}
}

// TestDownloadToCacheSizeCap: a file over the cap isn't cached.
func TestDownloadToCacheSizeCap(t *testing.T) {
	withMediaCache(t)
	srv, _ := pngServer(t, 32, 32)
	url := srv.URL + "/big.png"
	if err := DownloadToCache(url, "bob@example.test", 10); err == nil {
		t.Fatal("file over the cap was downloaded")
	}
	if isAttachmentDownloaded(url, "bob@example.test") {
		t.Fatal("file over the cap left in the cache")
	}
}

// TestPreviewMessageAction: "Show preview" is in the menu of a message with
// an uncached image, and fetches it; it's gone once there's nothing left
// to fetch.
func TestPreviewMessageAction(t *testing.T) {
	withMediaCache(t)
	srv, hits := pngServer(t, 32, 32)
	url := srv.URL + "/cat.png"
	m := newTestModelWithMessages([]Message{{ID: "a", Content: url, Attachments: []string{url}}}, &stubDaemon{})
	m.selectedMsg = 0
	hasPreview := func() bool {
		for _, it := range m.messageContextMenuItems(0) {
			if it.label == "Show preview" {
				return true
			}
		}
		return false
	}
	if !hasPreview() {
		t.Fatal("no Show preview for an uncached image")
	}
	m = runBatch(t, m, m.actionPreviewMessage())
	if hits.Load() != 1 || !strings.Contains(strings.Join(m.viewportLines, "\n"), upperHalfBlock) {
		t.Fatalf("hits=%d, preview drawn=%v", hits.Load(), strings.Contains(strings.Join(m.viewportLines, "\n"), upperHalfBlock))
	}
	if hasPreview() {
		t.Fatal("Show preview still offered for a cached image")
	}
}
