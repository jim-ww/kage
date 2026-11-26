package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jim-ww/kage/crypto/aesgcm"
)

// Auto-download: a received image or video attachment is fetched into the
// attachment cache when its message is rendered, so it gets a preview
// without being opened first. On by default - see
// DisplayOptions.AutoDownloadDisabled - and capped at a size, past which
// the file is left for opening by hand.

// DefaultAutoDownloadMaxMB is the largest file auto-download fetches.
const DefaultAutoDownloadMaxMB = 20

var (
	autoDownloadOn       atomic.Bool
	autoDownloadMaxBytes atomic.Int64
)

func init() {
	autoDownloadOn.Store(true)
	autoDownloadMaxBytes.Store(DefaultAutoDownloadMaxMB << 20)
}

// setAutoDownload applies the config; maxMB <= 0 is the default.
func setAutoDownload(on bool, maxMB int) {
	if maxMB <= 0 {
		maxMB = DefaultAutoDownloadMaxMB
	}
	autoDownloadOn.Store(on)
	autoDownloadMaxBytes.Store(int64(maxMB) << 20)
}

type mediaDownload struct{ target, jid string }

// mediaDownloadTried is every attachment auto-download has been asked for
// this session, successful or not: one that failed or was too large isn't
// retried on every render.
var (
	mediaDownloadMu    sync.Mutex
	mediaDownloadTried = map[string]bool{}
	mediaDownloadQueue []mediaDownload
)

// queueMediaDownload asks for target to be fetched, once per session.
func queueMediaDownload(target, jid string) {
	if !autoDownloadOn.Load() || attachmentDownloadURL(target) == "" {
		return
	}
	mediaDownloadMu.Lock()
	defer mediaDownloadMu.Unlock()
	if mediaDownloadTried[target] {
		return
	}
	mediaDownloadTried[target] = true
	mediaDownloadQueue = append(mediaDownloadQueue, mediaDownload{target, jid})
}

func takeMediaDownloadRequests() []tea.Cmd {
	mediaDownloadMu.Lock()
	queue := mediaDownloadQueue
	mediaDownloadQueue = nil
	mediaDownloadMu.Unlock()
	cmds := make([]tea.Cmd, len(queue))
	for i, d := range queue {
		cmds[i] = autoDownloadCmd(d.target, d.jid)
	}
	return cmds
}

// mediaDownloadedMsg is a finished auto-download; ok when it's now cached.
type mediaDownloadedMsg struct {
	target string
	ok     bool
}

// autoDownloadClient is what auto-download fetches with; tests keep it off
// the network.
var autoDownloadClient = http.DefaultClient

// mediaDownloadSlots bounds concurrent auto-downloads.
var mediaDownloadSlots = make(chan struct{}, 2)

func autoDownloadCmd(target, jid string) tea.Cmd {
	return func() tea.Msg {
		mediaDownloadSlots <- struct{}{}
		defer func() { <-mediaDownloadSlots }()
		err := autoDownload(target, jid, autoDownloadMaxBytes.Load())
		return mediaDownloadedMsg{target: target, ok: err == nil}
	}
}

// autoDownload fetches target into the attachment cache, refusing one over
// maxBytes - by its Content-Length when the server sends one, else once
// that much has been read. Written under a temporary name and renamed, so
// a preview never decodes half a file.
func autoDownload(target, jid string, maxBytes int64) error {
	dest, err := attachmentCachePath(target, jid)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dest); err == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, attachmentDownloadURL(target), nil)
	if err != nil {
		return err
	}
	resp, err := autoDownloadClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxBytes {
		return fmt.Errorf("%s, over the auto-download limit", humanFileSize(resp.ContentLength))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > maxBytes {
		return fmt.Errorf("over the auto-download limit")
	}
	if strings.HasPrefix(target, "aesgcm://") {
		_, iv, key, err := aesgcm.ParseAESGCMURL(target)
		if err != nil {
			return err
		}
		if data, err = aesgcm.Decrypt(data, iv, key); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".autodownload-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("writing %s: %w", dest, errors.Join(werr, cerr))
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
