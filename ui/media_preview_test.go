package ui

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withMediaCache points the attachment cache at a fresh dir and forgets
// every preview, for the duration of t.
func withMediaCache(t *testing.T) {
	t.Helper()
	old := AttachmentsDir
	AttachmentsDir = t.TempDir()
	reset := func() {
		mediaThumbMu.Lock()
		mediaThumbs, mediaThumbPending, mediaThumbQueue = map[string]mediaThumb{}, map[string]bool{}, nil
		clear(mediaBlockCache)
		mediaThumbMu.Unlock()
	}
	reset()
	t.Cleanup(func() { AttachmentsDir = old; reset() })
}

func writeMediaPNG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x * 255 / w), G: 80, B: uint8(y * 255 / h), A: 255})
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := encodePNG(f, img); err != nil {
		t.Fatal(err)
	}
}

// TestMediaPreviewDrawsCachedImageOnceDecoded: an image attachment in the
// cache is decoded off the UI goroutine and drawn under its name once that
// lands, growing the row by the picture's height.
func TestMediaPreviewDrawsCachedImageOnceDecoded(t *testing.T) {
	withMediaCache(t)
	const url = "https://upload.example.test/cat.png"
	writeMediaPNG(t, attachmentLocalPath(url, "bob@example.test"), 64, 64)

	m := newTestModelWithMessages([]Message{{ID: "a", Content: url, Attachments: []string{url}}}, &stubDaemon{})
	before := len(m.viewportLines)
	if strings.Contains(strings.Join(m.viewportLines, "\n"), upperHalfBlock) {
		t.Fatal("preview drawn before it was decoded")
	}

	cmd := takeMediaThumbRequests()
	if cmd == nil {
		t.Fatal("render didn't queue the cached image for decoding")
	}
	msg, ok := cmd().(mediaThumbMsg)
	if !ok || msg.thumb.failed {
		t.Fatalf("decode result = %#v", msg)
	}
	next, _ := m.Update(msg)
	m = next.(Model)

	if !strings.Contains(strings.Join(m.viewportLines, "\n"), upperHalfBlock) {
		t.Fatal("decoded preview not drawn")
	}
	// Square picture: 20 columns, 10 rows.
	if got := len(m.viewportLines) - before; got != mediaPreviewMaxRows {
		t.Fatalf("row grew by %d lines, want %d", got, mediaPreviewMaxRows)
	}
	if takeMediaThumbRequests() != nil {
		t.Fatal("decoded image queued again")
	}
}

// TestMediaPreviewSkipsUncachedAndNonImages: nothing is fetched for a
// preview, and only images are tried.
func TestMediaPreviewSkipsUncachedAndNonImages(t *testing.T) {
	withMediaCache(t)
	const doc = "https://upload.example.test/report.pdf"
	writeMediaPNG(t, attachmentLocalPath(doc, "bob@example.test"), 8, 8)
	msgs := []Message{
		{ID: "a", Content: "https://upload.example.test/remote.png", Attachments: []string{"https://upload.example.test/remote.png"}},
		{ID: "b", Content: doc, Attachments: []string{doc}},
	}
	_ = newTestModelWithMessages(msgs, &stubDaemon{})
	if takeMediaThumbRequests() != nil {
		t.Fatal("queued a decode for an uncached or non-image attachment")
	}
}

// TestMediaPreviewDisabled: the config switch turns previews off.
func TestMediaPreviewDisabled(t *testing.T) {
	withMediaCache(t)
	const url = "https://upload.example.test/cat.png"
	writeMediaPNG(t, attachmentLocalPath(url, "bob@example.test"), 16, 16)
	m := newTestModelWithMessages(nil, &stubDaemon{})
	setMediaPreviewsEnabled(false) // what New does for MediaPreviewsDisabled
	t.Cleanup(func() { setMediaPreviewsEnabled(true) })
	m.accounts[0].Messages[0] = []Message{{ID: "a", Content: url, Attachments: []string{url}}}
	m.refreshViewport()
	if takeMediaThumbRequests() != nil {
		t.Fatal("previews disabled, yet a decode was queued")
	}
}

// TestMediaPreviewUndecodable: a broken file just shows no picture, and
// isn't retried on every render.
func TestMediaPreviewUndecodable(t *testing.T) {
	withMediaCache(t)
	const url = "https://upload.example.test/broken.png"
	path := attachmentLocalPath(url, "bob@example.test")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a png"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newTestModelWithMessages([]Message{{ID: "a", Content: url, Attachments: []string{url}}}, &stubDaemon{})
	msg := takeMediaThumbRequests()().(mediaThumbMsg)
	if !msg.thumb.failed {
		t.Fatal("broken file decoded")
	}
	next, _ := m.Update(msg)
	m = next.(Model)
	m.refreshViewport()
	if takeMediaThumbRequests() != nil {
		t.Fatal("failed decode retried on render")
	}
}
