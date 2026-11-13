package ui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jim-ww/kage/crypto/aesgcm"
)

var urlPattern = regexp.MustCompile(`https?://\S+|aesgcm://\S+`)

// AttachmentsDir overrides where decrypted/downloaded attachments are cached
// for viewing (see attachmentRoot) — set once at startup from config.
// Empty means the os.UserCacheDir()-based default.
var AttachmentsDir string

// openableItems is msg's attachments, then the links in its body, deduped:
// an attachment's URL is usually its body too.
func openableItems(msg Message) []string {
	items := make([]string, 0, len(msg.Attachments))
	seen := make(map[string]bool, len(msg.Attachments))
	for _, a := range msg.Attachments {
		if !seen[a] {
			seen[a] = true
			items = append(items, a)
		}
	}
	for _, link := range urlPattern.FindAllString(msg.Content, -1) {
		if !seen[link] {
			seen[link] = true
			items = append(items, link)
		}
	}
	return items
}

// attachmentDownloadURL returns the plain http(s) URL data is fetched from
// for target, unwrapping aesgcm:// (XEP-0454) URLs. Returns "" if target
// isn't a recognized attachment scheme.
func attachmentDownloadURL(target string) string {
	if strings.HasPrefix(target, "aesgcm://") {
		downloadURL, _, _, err := aesgcm.ParseAESGCMURL(target)
		if err != nil {
			return ""
		}
		return downloadURL
	}
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		return target
	}
	return ""
}

// attachmentBaseName extracts the filename an attachment URL was uploaded
// with, stripping any query/fragment — mirrors the naming used when actually
// downloading (openWithXDGOpen, saveURLToDownloads).
func attachmentBaseName(downloadURL string) string {
	base := filepath.Base(downloadURL)
	if idx := strings.IndexAny(base, "?#"); idx >= 0 {
		base = base[:idx]
	}
	if base == "" || base == "." || base == "/" {
		base = "download"
	}
	return base
}

// AttachmentDisplayName is the exported form of attachmentDisplayName, for
// callers outside ui (e.g. desktop notifications) that need the normalized
// filename of an attachment URL rather than the raw aesgcm:///https:// link.
func AttachmentDisplayName(target string) string {
	return attachmentDisplayName(target)
}

// attachmentDisplayName returns a human-readable, percent-decoded filename
// for an attachment URL, e.g. "aesgcm://host/photo%20booth.jpg" -> "photo booth.jpg".
func attachmentDisplayName(target string) string {
	downloadURL := attachmentDownloadURL(target)
	if downloadURL == "" {
		return target
	}
	base := attachmentBaseName(downloadURL)
	if decoded, err := url.PathUnescape(base); err == nil {
		base = decoded
	}
	return base
}

// emojiFileIcons maps file extensions to emoji icons. Only used when the
// user has opted in via the icons config setting.
var emojiFileIcons = map[string]string{
	".jpg": "🖼", ".jpeg": "🖼", ".png": "🖼", ".gif": "🖼", ".webp": "🖼", ".bmp": "🖼", ".svg": "🖼", ".heic": "🖼",
	".mp4": "🎬", ".mkv": "🎬", ".webm": "🎬", ".mov": "🎬", ".avi": "🎬",
	".mp3": "🎵", ".wav": "🎵", ".flac": "🎵", ".ogg": "🎵", ".m4a": "🎵",
	".pdf": "📄",
	".zip": "📦", ".tar": "📦", ".gz": "📦", ".xz": "📦", ".7z": "📦", ".rar": "📦", ".bz2": "📦",
	".doc": "📝", ".docx": "📝", ".odt": "📝", ".rtf": "📝",
	".xls": "📊", ".xlsx": "📊", ".ods": "📊", ".csv": "📊",
	".txt": "📄", ".md": "📄", ".log": "📄",
}

const emojiFileIconDefault = "📎"

// plainFileIcons is the fallback used when icons is off (the default) —
// short bracketed tags that render correctly in any terminal.
var plainFileIcons = map[string]string{
	".jpg": "[img]", ".jpeg": "[img]", ".png": "[img]", ".gif": "[img]", ".webp": "[img]", ".bmp": "[img]", ".svg": "[img]", ".heic": "[img]",
	".mp4": "[vid]", ".mkv": "[vid]", ".webm": "[vid]", ".mov": "[vid]", ".avi": "[vid]",
	".mp3": "[aud]", ".wav": "[aud]", ".flac": "[aud]", ".ogg": "[aud]", ".m4a": "[aud]",
	".pdf": "[pdf]",
	".zip": "[zip]", ".tar": "[zip]", ".gz": "[zip]", ".xz": "[zip]", ".7z": "[zip]", ".rar": "[zip]", ".bz2": "[zip]",
	".doc": "[doc]", ".docx": "[doc]", ".odt": "[doc]", ".rtf": "[doc]",
	".xls": "[sheet]", ".xlsx": "[sheet]", ".ods": "[sheet]", ".csv": "[sheet]",
	".txt": "[txt]", ".md": "[txt]", ".log": "[txt]",
}

const plainFileIconDefault = "[file]"

// attachmentIcon picks an icon for the attachment's file extension: an emoji
// when icons is true, otherwise a plain-text [tag] that's guaranteed to
// render in any terminal.
func attachmentIcon(name string, icons bool) string {
	ext := strings.ToLower(filepath.Ext(name))
	if icons {
		if icon, ok := emojiFileIcons[ext]; ok {
			return icon
		}
		return emojiFileIconDefault
	}
	if icon, ok := plainFileIcons[ext]; ok {
		return icon
	}
	return plainFileIconDefault
}

// sanitizeJIDForPath makes a bare JID one safe path component.
func sanitizeJIDForPath(jid string) string {
	if slash := strings.IndexByte(jid, '/'); slash >= 0 {
		jid = jid[:slash]
	}
	jid = strings.ReplaceAll(jid, string(filepath.Separator), "_")
	jid = strings.ReplaceAll(jid, "\\", "_")
	if jid == "" || jid == "." || jid == ".." {
		jid = "unknown"
	}
	return jid
}

// AttachmentLocalPath is attachmentLocalPath, for the daemon to cache an
// uploaded file where its download would go.
func AttachmentLocalPath(target, jid string) string {
	return attachmentLocalPath(target, jid)
}

// attachmentLocalPath is where opening target caches it, "" if it isn't a
// download URL. Runs on render, so creates nothing.
func attachmentLocalPath(target, jid string) string {
	downloadURL := attachmentDownloadURL(target)
	if downloadURL == "" {
		return ""
	}
	root, err := attachmentRoot()
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(target))
	return filepath.Join(root, sanitizeJIDForPath(jid), hex.EncodeToString(sum[:8])+"-"+attachmentBaseName(downloadURL))
}

// attachmentRoot is the attachments cache: AttachmentsDir, or under the
// user cache dir.
func attachmentRoot() (string, error) {
	if AttachmentsDir != "" {
		return AttachmentsDir, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("finding cache directory: %w", err)
	}
	return filepath.Join(base, "kage", "attachments"), nil
}

// isAttachmentDownloaded reports whether target already has a local copy —
// see attachmentLocalPath.
func isAttachmentDownloaded(target, jid string) bool {
	path := attachmentLocalPath(target, jid)
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// attachmentLocalSize is the size of target's cached copy, if any.
func attachmentLocalSize(target, jid string) (int64, bool) {
	path := attachmentLocalPath(target, jid)
	if path == "" {
		return 0, false
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	return info.Size(), true
}

// humanFileSize formats a byte count the way file managers do: "4.2 KB",
// "1.1 MB", falling back to a plain byte count under 1 KB.
func humanFileSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// attachmentSizeLabel is the info popup's size for target: the cached
// copy's, else what fetchAttachmentSizeCmd found, else a status.
func (m Model) attachmentSizeLabel(target, jid string) string {
	if size, ok := attachmentLocalSize(target, jid); ok {
		return humanFileSize(size)
	}
	if size, ok := m.attachmentSizes[target]; ok {
		return humanFileSize(size)
	}
	if m.attachmentSizeFetching[target] {
		return "fetching…"
	}
	// m.attachmentSizeFailed[target] and "never even tried" (e.g. no
	// download URL) both land here - either way, unknown is unknown.
	return "unknown"
}

// renderAttachmentLine renders "<icon> <name>", marked if cached. No size:
// that's a HEAD request telling the host we looked; message info asks.
func renderAttachmentLine(target string, icons bool, jid string) string {
	name := attachmentDisplayName(target)
	line := attachmentIcon(name, icons) + " " + name
	if isAttachmentDownloaded(target, jid) {
		line += " (downloaded)"
	}
	return line
}

// openItemMenu asks which of items (openableItems) to act on. The first
// attachCount, attachments, show by name: an aesgcm:// URL holds the key.
func (m *Model) openItemMenu(title string, items []string, attachCount int, act func(m *Model, target string, isAttachment bool) tea.Cmd) tea.Cmd {
	menu := make([]contextMenuItem, len(items))
	for i, target := range items {
		isAttachment := i < attachCount
		label := target
		if isAttachment {
			label = attachmentDisplayName(target)
		}
		var key rune
		if i < 9 {
			key = rune('1' + i)
		}
		menu[i] = contextMenuItem{label: previewText(label, previewLen), key: key, run: func(m *Model) tea.Cmd {
			return act(m, target, isAttachment)
		}}
	}
	m.openContextMenu(title, menu)
	return nil
}

type openResultMsg struct {
	target       string
	isAttachment bool // true when target is a real attachment (downloaded via downloadAndOpen), not a plain link handed straight to xdg-open - lets the UI show the decoded filename instead of the raw URL
	err          error
}

// attachmentSizeMsg is fetchAttachmentSizeCmd's result; ok false is not
// retried (see Model.attachmentSizeFailed).
type attachmentSizeMsg struct {
	target string
	size   int64
	ok     bool
}

// fetchAttachmentSizeCmd HEAD-requests target's size, only on asking for a
// message's info (see renderAttachmentLine).
func fetchAttachmentSizeCmd(target string) tea.Cmd {
	return func() tea.Msg {
		downloadURL := attachmentDownloadURL(target)
		if downloadURL == "" {
			return attachmentSizeMsg{target: target}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, downloadURL, nil)
		if err != nil {
			return attachmentSizeMsg{target: target}
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return attachmentSizeMsg{target: target}
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 || resp.ContentLength < 0 {
			return attachmentSizeMsg{target: target}
		}
		return attachmentSizeMsg{target: target, size: resp.ContentLength, ok: true}
	}
}

// openWithXDGOpen opens target with xdg-open. A local path or a plain link
// goes as is; an attachment is downloaded (and decrypted) first, so the
// file's viewer opens it rather than the browser.
func openWithXDGOpen(target string, isAttachment bool, jid string) tea.Cmd {
	isRemoteURL := strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "aesgcm://")
	downloadFirst := strings.HasPrefix(target, "aesgcm://") || (isAttachment && isRemoteURL)
	if !downloadFirst {
		return func() tea.Msg { return openResultMsg{target: target, err: xdgOpen(target)} }
	}

	ch := make(chan tea.Msg, 8)
	go func() {
		ch <- downloadAndOpen(target, jid, ch)
	}()
	return listenForTransferChan(ch)
}

// downloadAndOpen downloads an attachment into the cache, keyed by the whole
// target (an aesgcm:// key too), and opens it, reporting progress on ch.
func downloadAndOpen(target, jid string, ch chan tea.Msg) tea.Msg {
	result := func(err error) tea.Msg { return openResultMsg{target: target, isAttachment: true, err: err} }
	dest, err := attachmentCachePath(target, jid)
	if err != nil {
		return result(err)
	}
	if _, err := os.Stat(dest); err == nil {
		return result(xdgOpen(dest))
	}
	data, err := fetchAttachment(target, ch)
	if err != nil {
		return result(err)
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if os.IsExist(err) {
		// Lost a race with another concurrent open of the same attachment;
		// the other one already wrote dest.
		return result(xdgOpen(dest))
	}
	if err != nil {
		return result(err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		os.Remove(dest)
		return result(err)
	}
	return result(xdgOpen(dest))
}

// fetchAttachment downloads target - an http(s):// or aesgcm:// URL, the
// latter decrypted (XEP-0454) - reporting progress on ch as it goes.
func fetchAttachment(target string, ch chan tea.Msg) ([]byte, error) {
	downloadURL := attachmentDownloadURL(target)
	if downloadURL == "" {
		return nil, fmt.Errorf("not a downloadable URL: %s", target)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}
	// Wrapped even when ContentLength is unknown (chunked transfer-encoding)
	// - throttledProgressSender falls back to byte-based throttling then, so
	// the transfer still shows progress.
	label := "downloading " + attachmentDisplayName(target)
	data, err := io.ReadAll(&progressReader{Reader: resp.Body, total: resp.ContentLength, onProgress: throttledProgressSender(ch, target, label)})
	if err != nil {
		return nil, fmt.Errorf("reading download: %w", err)
	}
	if !strings.HasPrefix(target, "aesgcm://") {
		return data, nil
	}
	_, iv, key, err := aesgcm.ParseAESGCMURL(target)
	if err != nil {
		return nil, fmt.Errorf("parsing aesgcm URL: %w", err)
	}
	data, err = aesgcm.Decrypt(data, iv, key)
	if err != nil {
		return nil, fmt.Errorf("decrypting file: %w", err)
	}
	return data, nil
}

// xdgOpen opens path with the desktop's default application for it.
func xdgOpen(path string) error {
	cmd := exec.Command("xdg-open", path)
	cmd.SysProcAttr = detachedSysProcAttr()
	return cmd.Start()
}

type saveResultMsg struct {
	target string // source URL, so Update can clear its transfer progress entry
	path   string // destination path on success
	err    error
}

// downloadsDir returns where "save as" should write files: $XDG_DOWNLOAD_DIR
// if set, otherwise ~/Downloads, creating it if it doesn't exist yet.
func downloadsDir() (string, error) {
	dir := strings.TrimSpace(os.Getenv("XDG_DOWNLOAD_DIR"))
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("finding home directory: %w", err)
		}
		dir = filepath.Join(home, "Downloads")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	return dir, nil
}

// attachmentCachePath is attachmentLocalPath, its per-chat directory created.
func attachmentCachePath(target, jid string) (string, error) {
	path := attachmentLocalPath(target, jid)
	if path == "" {
		return "", fmt.Errorf("not a downloadable URL: %s", target)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	return path, nil
}

// uniqueDestPath returns dir/base, or dir/base-N (before the extension) for
// the smallest N that doesn't already exist, so a repeat save never
// clobbers an earlier download of the same filename.
func uniqueDestPath(dir, base string) string {
	dest := filepath.Join(dir, base)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for n := 1; ; n++ {
		if _, err := os.Stat(dest); os.IsNotExist(err) {
			return dest
		}
		dest = filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, n, ext))
	}
}

// saveURLToDownloads downloads target, decrypted, into the downloads dir.
func saveURLToDownloads(target, jid string) tea.Cmd {
	ch := make(chan tea.Msg, 8)
	go func() {
		ch <- downloadToDest(target, "", jid, ch)
	}()
	return listenForTransferChan(ch)
}

// saveURLToPath downloads target to the explicit destination path dest
// (from the "save as" prompt) instead of the default downloads directory,
// overwriting any existing file there — the user chose that exact path.
func saveURLToPath(target, dest, jid string) tea.Cmd {
	ch := make(chan tea.Msg, 8)
	go func() {
		ch <- downloadToDest(target, dest, jid, ch)
	}()
	return listenForTransferChan(ch)
}

// downloadToDest downloads target to dest, or a new name in the downloads
// dir for "", reporting progress on ch.
func downloadToDest(target, dest, jid string, ch chan tea.Msg) tea.Msg {
	result := func(err error) tea.Msg { return saveResultMsg{target: target, err: err} }
	downloadURL := attachmentDownloadURL(target)
	if downloadURL == "" {
		return result(fmt.Errorf("not a downloadable URL: %s", target))
	}
	// The user typed an explicit path in the "save as" prompt: overwrite it,
	// as a save-as dialog does, rather than erroring.
	writeFlags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if dest == "" {
		dir, err := downloadsDir()
		if err != nil {
			return result(err)
		}
		dest = uniqueDestPath(dir, attachmentBaseName(downloadURL))
		writeFlags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	} else if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return result(fmt.Errorf("creating %s: %w", filepath.Dir(dest), err))
	}

	data, err := fetchAttachment(target, ch)
	if err != nil {
		return result(err)
	}
	f, err := os.OpenFile(dest, writeFlags, 0o644)
	if err != nil {
		return result(err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		os.Remove(dest)
		return result(err)
	}
	// Cached too, so opening the file later doesn't download it again.
	if cached, err := attachmentCachePath(target, jid); err == nil {
		if _, err := os.Stat(cached); os.IsNotExist(err) {
			_ = os.WriteFile(cached, data, 0o644)
		}
	}
	return saveResultMsg{target: target, path: dest}
}
