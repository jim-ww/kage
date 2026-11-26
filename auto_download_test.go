package main

import (
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jim-ww/kage/ui"
)

// autoDownloadSite serves a PNG at any path, counting requests.
func autoDownloadSite(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		png.Encode(rw, image.NewRGBA(image.Rect(0, 0, 4, 4)))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func withAutoDownload(t *testing.T, on bool) {
	t.Helper()
	old := ui.AttachmentsDir
	ui.AttachmentsDir = t.TempDir()
	autoDownloadEnabled.Store(on)
	autoDownloadMaxBytes.Store(1 << 20)
	t.Cleanup(func() { ui.AttachmentsDir = old; autoDownloadEnabled.Store(false) })
}

// waitAutoDownloads waits for every auto-download in flight to finish.
func waitAutoDownloads(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		busy := false
		autoDownloading.Range(func(any, any) bool { busy = true; return false })
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("auto-download still running")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestAutoDownloadAttachments: an arriving message's image is cached, its
// other files aren't touched, and nothing is fetched with it disabled or
// for a message that didn't decrypt.
func TestAutoDownloadAttachments(t *testing.T) {
	const peer = "bob@example.test"
	t.Run("on", func(t *testing.T) {
		withAutoDownload(t, true)
		srv, hits := autoDownloadSite(t)
		img, doc := srv.URL+"/cat.png", srv.URL+"/report.pdf"
		autoDownloadAttachments(nil, 0, peer, []ui.Message{{Attachments: []string{img, doc}}})
		waitAutoDownloads(t)
		if _, err := os.Stat(ui.AttachmentLocalPath(img, peer)); err != nil {
			t.Fatalf("image not cached: %v", err)
		}
		if hits.Load() != 1 {
			t.Fatalf("%d fetches, want just the image", hits.Load())
		}
	})
	t.Run("off", func(t *testing.T) {
		withAutoDownload(t, false)
		srv, hits := autoDownloadSite(t)
		autoDownloadAttachments(nil, 0, peer, []ui.Message{{Attachments: []string{srv.URL + "/cat.png"}}})
		waitAutoDownloads(t)
		if hits.Load() != 0 {
			t.Fatal("fetched with auto-download disabled")
		}
	})
	t.Run("undecrypted", func(t *testing.T) {
		withAutoDownload(t, true)
		srv, hits := autoDownloadSite(t)
		autoDownloadAttachments(nil, 0, peer, []ui.Message{{DecryptFailed: true, Attachments: []string{srv.URL + "/cat.png"}}})
		waitAutoDownloads(t)
		if hits.Load() != 0 {
			t.Fatal("fetched for a message that didn't decrypt")
		}
	})
}
