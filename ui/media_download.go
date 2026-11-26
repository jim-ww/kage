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
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jim-ww/kage/crypto/aesgcm"
)

// Fetching attachments for their previews. A render never does: the daemon
// auto-downloads a new message's images and videos as it arrives (see
// autoDownloadAttachments in package main) and reports each with an
// AttachmentCachedMsg; an older one is fetched on asking, from the message
// menu (actionPreviewMessage).

// IsPreviewable reports whether target, an attachment URL, is an image or
// video a preview can be drawn for.
func IsPreviewable(target string) bool {
	return previewKind(attachmentDisplayName(target)) != mediaNone
}

// mediaFetchClient is what DownloadToCache fetches with; tests keep it off
// the network.
var mediaFetchClient = http.DefaultClient

// DownloadToCache fetches target into the attachment cache for jid's chat,
// refusing one over maxBytes - by its Content-Length when the server sends
// one, else once that much has been read. Written under a temporary name
// and renamed, so a preview never decodes half a file. A file already
// cached is left alone.
func DownloadToCache(target, jid string, maxBytes int64) error {
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
	resp, err := mediaFetchClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxBytes {
		return fmt.Errorf("%s, over the %s limit", humanFileSize(resp.ContentLength), humanFileSize(maxBytes))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > maxBytes {
		return fmt.Errorf("over the %s limit", humanFileSize(maxBytes))
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
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".download-*")
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

// AttachmentCachedMsg is the daemon reporting an attachment of the chat
// From just auto-downloaded into the cache, for its preview to be drawn.
type AttachmentCachedMsg struct {
	AccountIdx int
	From       string // bare JID of the chat
	Target     string
}

// previewMaxBytes caps an attachment fetched on asking for its preview:
// it's read whole into memory to be decrypted.
const previewMaxBytes = 200 << 20

// previewableUncached is msg's image and video attachments that have no
// cached copy yet.
func previewableUncached(msg Message, jid string) []string {
	if msg.Retracted {
		return nil
	}
	var out []string
	for _, a := range msg.Attachments {
		if IsPreviewable(a) && !isAttachmentDownloaded(a, jid) {
			out = append(out, a)
		}
	}
	return out
}

// mediaPreviewFetchedMsg is actionPreviewMessage's result, one per file.
type mediaPreviewFetchedMsg struct {
	target string
	err    error
}

// actionPreviewMessage fetches the selected message's images and videos
// that aren't cached, for their previews: what auto-download does for a
// new message, for one that arrived before it, or over its size limit.
func (m *Model) actionPreviewMessage() tea.Cmd {
	msgs := m.currentMessages()
	chat, ok := m.currentChat()
	if !ok || m.selectedMsg < 0 || m.selectedMsg >= len(msgs) {
		return m.showNotification("no message selected")
	}
	targets := previewableUncached(msgs[m.selectedMsg], chat.Address)
	if len(targets) == 0 {
		return m.showNotification("nothing to preview")
	}
	cmds := make([]tea.Cmd, len(targets))
	for i, target := range targets {
		jid := chat.Address
		cmds[i] = func() tea.Msg {
			return mediaPreviewFetchedMsg{target: target, err: DownloadToCache(target, jid, previewMaxBytes)}
		}
	}
	return tea.Batch(cmds...)
}
