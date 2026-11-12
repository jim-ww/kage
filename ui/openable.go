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

// openableItems returns every attachment path/URL plus every link found in
// the message body, attachments first, deduplicated — a file we sent (or
// received) has its URL in both Attachments and, since that URL is also the
// entire message body, matched again by urlPattern against Content, so
// without deduping it'd show up as two identical picker entries.
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

// sanitizeJIDForPath turns a JID into a single filesystem-safe path
// component for the attachments cache's per-chat subdirectory: strips any
// /resource, and replaces path separators that would otherwise split it
// into extra directory levels or escape the attachments dir.
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

// AttachmentLocalPath exports attachmentLocalPath for the daemon-side upload
// path (adapter.go), which copies a just-uploaded local file into the same
// cache/downloads location a download of its own URL would use, so it's
// available offline without a round trip through the server.
func AttachmentLocalPath(target, jid string) string {
	return attachmentLocalPath(target, jid)
}

// attachmentLocalPath returns the destination path target would already be
// at if opened via Ctrl+O - always the attachments cache (attachmentRoot),
// mirroring the exact destination downloadAndOpen uses regardless of whether
// target is aesgcm:// or plain http(s)://. Empty if target's download URL
// can't be determined. Doesn't create the directory: this runs on every
// render, so it must stay a pure read.
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

// attachmentLocalSize returns the size in bytes of target's local copy (see
// attachmentLocalPath), and whether one was found - only meaningful once
// downloaded, since nothing else in this client tracks an attachment's
// size (XEP-0363 upload responses don't report it back).
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

// attachmentSizeLabel returns the info popup's size line for target: a
// local copy's real size if one exists, else whatever fetchAttachmentSizeCmd
// found (triggered by actionInfoMessage when the popup opened), else a
// status word for the in-between/failure states.
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

// renderAttachmentLine formats an attachment as "<icon> <name>", with a
// trailing marker when a local copy already exists. Size isn't shown here -
// for a not-yet-downloaded attachment that would mean an HTTP HEAD request
// per render, silently telling whatever server hosts the file that this
// account is looking at it even if it's never actually opened; see the
// message-info popup (ctrl+i/actionInfoMessage) instead, which fetches it
// on demand only for the one message you explicitly asked about.
func renderAttachmentLine(target string, icons bool, jid string) string {
	name := attachmentDisplayName(target)
	line := attachmentIcon(name, icons) + " " + name
	if isAttachmentDownloaded(target, jid) {
		line += " (downloaded)"
	}
	return line
}

// openItemMenu asks which of a message's links/attachments to act on - open,
// save, save as - the first nine numbered. items is openableItems' list:
// its first attachCount entries are real attachments, shown by their file
// name rather than the raw URL (which for aesgcm:// also embeds the file's
// decryption key); a plain link is shown as-is.
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

// attachmentSizeMsg reports the result of fetchAttachmentSizeCmd: ok is
// false when the HEAD request failed or the server didn't report a
// Content-Length, in which case size is meaningless and shouldn't be
// retried automatically (see Model.attachmentSizeFailed).
type attachmentSizeMsg struct {
	target string
	size   int64
	ok     bool
}

// fetchAttachmentSizeCmd HEAD-requests target's size - deliberately not
// called eagerly for every rendered attachment (that would tell whatever
// server hosts each file that this account is looking at it, even for
// files never actually opened); only actionInfoMessage triggers this, for
// the one message's attachments the user explicitly asked to inspect.
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

// openWithXDGOpen shells out to xdg-open in the background; the result is
// reported back as an openResultMsg so the UI can show a toast.
//
// A local file path (e.g. a staged-but-unsent pendingAttachment) or a plain
// http(s):// target that isn't a known attachment (isAttachment false - the
// user opening an ordinary link they pasted/received in a message, not a
// file) is handed to xdg-open as-is: for a local path that's simply opening
// it, for a link it resolves via the desktop's URL-scheme handler -
// normally the web browser, which is exactly what's wanted.
//
// Otherwise (aesgcm:// always, or a remote http(s):// target that *is* a
// known attachment) the file is downloaded first - decrypted too, for
// aesgcm:// (XEP-0454) - and only the resulting local file is passed to
// xdg-open, so the desktop's file-type association picks the right viewer
// (image viewer, PDF reader, etc.) instead of the URL handler opening it as
// a raw browser download. Progress is reported as the download runs — see
// throttledProgressSender.
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

// downloadAndOpen does the actual download(+decrypt)+open work for
// openWithXDGOpen's attachment path, reporting progress on ch as it goes.
// The file is cached by a hash of the full target (for aesgcm://,
// URL+iv+key all factor into the plaintext), so re-opening the same
// attachment reuses the already-downloaded copy (unlike an explicit "Save
// As", which always wants a fresh, uniquely-named file in Downloads).
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

// attachmentCachePath is attachmentLocalPath, its directory created if need
// be. Attachments are cached for viewing (as opposed to downloadsDir, which
// is for explicit "Save As") under a subdirectory per chat (bare JID), so
// files from different chats never share a directory.
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

// saveURLToDownloads downloads target (an http/https URL or aesgcm:// URL — everything
// openableItems surfaces is one, whether a peer's attachment or our own
// just-uploaded file) into the user's downloads directory, reporting
// progress as it goes (see throttledProgressSender).
// For aesgcm:// URLs (XEP-0454), the file is decrypted after download.
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

// downloadToDest does the actual download+decrypt+write work for
// saveURLToDownloads/saveURLToPath, reporting progress on ch as it goes.
// dest is the explicit destination path to write to; if empty, one is
// computed from downloadsDir() + the URL's basename (deduped against
// existing files via uniqueDestPath). Split out so the Cmd constructors
// (which must return immediately) stay simple.
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
