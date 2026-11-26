package main

import (
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/ui"
)

// autoDownloadEnabled and autoDownloadMaxBytes mirror
// cfg.AutoDownloadDisabled/AutoDownloadMaxMB - same reasoning as
// notifyEnabled.
var (
	autoDownloadEnabled  atomic.Bool
	autoDownloadMaxBytes atomic.Int64
)

// autoDownloadSlots bounds concurrent auto-downloads: an offline catch-up
// can bring many at once.
var autoDownloadSlots = make(chan struct{}, 2)

// autoDownloading is the attachments being fetched right now, so a message
// seen twice (a live copy and its archived one) isn't fetched twice.
var autoDownloading sync.Map

// autoDownloadAttachments fetches the image and video attachments of
// messages that just arrived in the chat peer - live, or caught up after
// being offline, never older history - into the attachment cache, so every
// TUI can draw their previews; each one cached is broadcast as
// ui.AttachmentCachedMsg. Runs in the background: the caller is the
// message path.
func autoDownloadAttachments(srv *ipc.Server, accountIdx int, peer string, msgs []ui.Message) {
	if !autoDownloadEnabled.Load() {
		return
	}
	maxBytes := autoDownloadMaxBytes.Load()
	for _, msg := range msgs {
		if msg.DecryptFailed || msg.Retracted {
			continue
		}
		for _, target := range msg.Attachments {
			if !ui.IsPreviewable(target) {
				continue
			}
			if _, busy := autoDownloading.LoadOrStore(target, true); busy {
				continue
			}
			go recoverAndLog("", "auto-download", func() {
				defer autoDownloading.Delete(target)
				autoDownloadSlots <- struct{}{}
				defer func() { <-autoDownloadSlots }()
				if err := ui.DownloadToCache(target, peer, maxBytes); err != nil {
					slog.Info("auto-download skipped", "peer", peer, "file", ui.AttachmentDisplayName(target), "err", err)
					return
				}
				broadcast(srv, ui.AttachmentCachedMsg{AccountIdx: accountIdx, From: peer, Target: target})
			})
		}
	}
}
