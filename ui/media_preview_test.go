package ui

import (
	"image"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
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
	if got := len(m.viewportLines) - before; got != DefaultMediaPreviewHeight {
		t.Fatalf("row grew by %d lines, want %d", got, DefaultMediaPreviewHeight)
	}
	if takeMediaThumbRequests() != nil {
		t.Fatal("decoded image queued again")
	}
}

// TestMediaPreviewSkipsNonMedia: only images and videos are previewed, or
// auto-downloaded for it.
func TestMediaPreviewSkipsNonMedia(t *testing.T) {
	withMediaCache(t)
	const doc = "https://upload.example.test/report.pdf"
	writeMediaPNG(t, attachmentLocalPath(doc, "bob@example.test"), 8, 8)
	msgs := []Message{
		{ID: "a", Content: doc, Attachments: []string{doc}},
		{ID: "b", Content: "https://upload.example.test/notes.txt", Attachments: []string{"https://upload.example.test/notes.txt"}},
	}
	_ = newTestModelWithMessages(msgs, &stubDaemon{})
	if takeMediaThumbRequests() != nil {
		t.Fatal("queued work for a non-media attachment")
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

// TestMediaPreviewVideoFrame: a cached video's preview is a frame ffmpeg
// pulls from it, at the video's aspect ratio - including one too short to
// seek a second into.
func TestMediaPreviewVideoFrame(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skipf("no ffmpeg: %v", err)
	}
	for _, duration := range []string{"2", "0.2"} {
		t.Run(duration+"s", func(t *testing.T) {
			withMediaCache(t)
			const url = "https://upload.example.test/clip.mp4"
			path := attachmentLocalPath(url, "bob@example.test")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin",
				"-f", "lavfi", "-i", "testsrc=size=320x180:rate=10:duration="+duration,
				"-pix_fmt", "yuv420p", path).CombinedOutput()
			if err != nil {
				t.Skipf("ffmpeg can't make a test clip: %v: %s", err, out)
			}
			m := newTestModelWithMessages([]Message{{ID: "a", Content: url, Attachments: []string{url}}}, &stubDaemon{})
			msg := takeMediaThumbRequests()().(mediaThumbMsg)
			if msg.thumb.failed {
				t.Fatal("no frame from the video")
			}
			if d := msg.thumb.width*9 - msg.thumb.height*16; d < -16 || d > 16 {
				t.Fatalf("frame is %d×%d, not the video's 16:9", msg.thumb.width, msg.thumb.height)
			}
			next, _ := m.Update(msg)
			if !strings.Contains(strings.Join(next.(Model).viewportLines, "\n"), upperHalfBlock) {
				t.Fatal("video preview not drawn")
			}
		})
	}
}

// TestMediaPreviewHeightSetting: media_preview_height caps the picture.
func TestMediaPreviewHeightSetting(t *testing.T) {
	withMediaCache(t)
	const url = "https://upload.example.test/cat.png"
	writeMediaPNG(t, attachmentLocalPath(url, "bob@example.test"), 128, 64)
	m := newTestModelWithMessages(nil, &stubDaemon{})
	setMediaPreviewHeight(4)
	t.Cleanup(func() { setMediaPreviewHeight(0) })
	before := len(m.viewportLines)
	m.accounts[0].Messages[0] = []Message{{ID: "a", Content: url, Attachments: []string{url}}}
	m.refreshViewport()
	withoutPicture := len(m.viewportLines)
	next, _ := m.Update(takeMediaThumbRequests()().(mediaThumbMsg))
	m = next.(Model)
	if got := len(m.viewportLines) - withoutPicture; got != 4 {
		t.Fatalf("preview is %d rows, want 4 (before=%d)", got, before)
	}
}

// TestMediaPreviewNamesHidden: with media_preview_names_hidden a previewed
// attachment is its picture alone, and one without a preview keeps its
// name.
func TestMediaPreviewNamesHidden(t *testing.T) {
	withMediaCache(t)
	const shown, missing = "https://upload.example.test/cat.png", "https://upload.example.test/dog.png"
	writeMediaPNG(t, attachmentLocalPath(shown, "bob@example.test"), 32, 32)
	m := newTestModelWithMessages(nil, &stubDaemon{})
	mediaPreviewNamesHidden.Store(true) // what New does for MediaPreviewNamesHidden
	t.Cleanup(func() { mediaPreviewNamesHidden.Store(false) })
	m.accounts[0].Messages[0] = []Message{
		{ID: "a", Content: shown, Attachments: []string{shown}},
		{ID: "b", Content: missing, Attachments: []string{missing}},
	}
	m.refreshViewport()
	if view := ansi.Strip(strings.Join(m.viewportLines, "\n")); !strings.Contains(view, "cat.png") {
		t.Fatalf("name hidden before its preview is drawn:\n%s", view)
	}
	next, _ := m.Update(takeMediaThumbRequests()().(mediaThumbMsg))
	view := ansi.Strip(strings.Join(next.(Model).viewportLines, "\n"))
	if strings.Contains(view, "cat.png") || !strings.Contains(view, upperHalfBlock) {
		t.Fatalf("previewed attachment still named, or not drawn:\n%s", view)
	}
	if !strings.Contains(view, "dog.png") {
		t.Fatalf("attachment without a preview lost its name:\n%s", view)
	}
}
