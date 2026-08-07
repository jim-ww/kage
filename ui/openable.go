package ui

import (
	"bytes"
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

// openItemsPerPage caps how many open-picker entries are shown at once,
// since only digits 1-9 are used to pick one; extra items page with left/right.
const openItemsPerPage = 9

// openPageBounds returns the [start, end) slice bounds of items on page.
func openPageBounds(total, page int) (start, end int) {
	start = page * openItemsPerPage
	if start > total {
		start = total
	}
	end = start + openItemsPerPage
	if end > total {
		end = total
	}
	return start, end
}

// openPageCount returns the number of pages needed for n items.
func openPageCount(n int) int {
	if n == 0 {
		return 1
	}
	return (n + openItemsPerPage - 1) / openItemsPerPage
}

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

// pickerMode distinguishes what digit-selecting an item in the open/save
// popup actually does — both share the same numbered-list UI (m.openItems),
// just with a different action and result phrasing.
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

// isAttachmentDownloaded reports whether target already has a local copy —
// either in the aesgcm view cache (attachmentCacheDir) or in the downloads
// directory, mirroring the exact destination paths openWithXDGOpen and
// saveURLToDownloads would use. Doesn't create either directory: this runs
// on every render, so it must stay a pure read.
func isAttachmentDownloaded(target string) bool {
	downloadURL := attachmentDownloadURL(target)
	if downloadURL == "" {
		return false
	}
	base := attachmentBaseName(downloadURL)

	if strings.HasPrefix(target, "aesgcm://") {
		cacheBase, err := os.UserCacheDir()
		if err != nil {
			return false
		}
		sum := sha256.Sum256([]byte(target))
		dest := filepath.Join(cacheBase, "kage", "attachments", hex.EncodeToString(sum[:8])+"-"+base)
		_, err = os.Stat(dest)
		return err == nil
	}

	dir := strings.TrimSpace(os.Getenv("XDG_DOWNLOAD_DIR"))
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		dir = filepath.Join(home, "Downloads")
	}
	_, err := os.Stat(filepath.Join(dir, base))
	return err == nil
}

// renderAttachmentLine formats an attachment as "<icon> <name>", with a
// trailing marker when a local copy already exists.
func renderAttachmentLine(target string, icons bool) string {
	name := attachmentDisplayName(target)
	line := attachmentIcon(name, icons) + " " + name
	if isAttachmentDownloaded(target) {
		line += " (downloaded)"
	}
	return line
}

type pickerMode int

const (
	pickerModeOpen pickerMode = iota
	pickerModeSave
	pickerModeSaveAs
)

type openResultMsg struct {
	target       string
	isAttachment bool // true when target is a real attachment (downloaded via downloadAndOpen), not a plain link handed straight to xdg-open - lets the UI show the decoded filename instead of the raw URL
	err          error
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
func openWithXDGOpen(target string, isAttachment bool) tea.Cmd {
	isRemoteURL := strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "aesgcm://")
	downloadFirst := strings.HasPrefix(target, "aesgcm://") || (isAttachment && isRemoteURL)
	if !downloadFirst {
		return func() tea.Msg {
			err := exec.Command("xdg-open", target).Start()
			return openResultMsg{target: target, err: err}
		}
	}

	ch := make(chan tea.Msg, 8)
	go func() {
		ch <- downloadAndOpen(target, ch)
	}()
	return listenForTransferChan(ch)
}

// downloadAndOpen does the actual download(+decrypt)+open work for
// openWithXDGOpen's attachment path, reporting progress on ch as it goes.
// Split out so openWithXDGOpen's Cmd construction (which must return
// immediately) stays simple.
func downloadAndOpen(target string, ch chan tea.Msg) tea.Msg {
	// Cache the (decrypted, for aesgcm://) file by a hash of the full
	// target (for aesgcm://, URL+iv+key all factor into the plaintext), so
	// re-opening the same attachment for viewing reuses the
	// already-downloaded copy instead of writing a new one each time
	// (unlike an explicit "Save As", which always wants a fresh,
	// uniquely-named file in Downloads).
	downloadURL := target
	var iv, key []byte
	if strings.HasPrefix(target, "aesgcm://") {
		var parseErr error
		downloadURL, iv, key, parseErr = aesgcm.ParseAESGCMURL(target)
		if parseErr != nil {
			return openResultMsg{target: target, isAttachment: true, err: fmt.Errorf("parsing aesgcm URL: %w", parseErr)}
		}
	}

	dir, dirErr := attachmentCacheDir()
	if dirErr != nil {
		return openResultMsg{target: target, isAttachment: true, err: dirErr}
	}
	base := filepath.Base(downloadURL)
	if idx := strings.IndexAny(base, "?#"); idx >= 0 {
		base = base[:idx]
	}
	if base == "" || base == "." || base == "/" {
		base = "download"
	}
	sum := sha256.Sum256([]byte(target))
	dest := filepath.Join(dir, hex.EncodeToString(sum[:8])+"-"+base)

	if _, statErr := os.Stat(dest); statErr == nil {
		err := exec.Command("xdg-open", dest).Start()
		return openResultMsg{target: target, isAttachment: true, err: err}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if reqErr != nil {
		return openResultMsg{target: target, isAttachment: true, err: reqErr}
	}
	resp, httpErr := http.DefaultClient.Do(req)
	if httpErr != nil {
		return openResultMsg{target: target, isAttachment: true, err: httpErr}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openResultMsg{target: target, isAttachment: true, err: fmt.Errorf("download failed: HTTP %d", resp.StatusCode)}
	}

	label := "downloading " + attachmentDisplayName(target)
	body := io.Reader(resp.Body)
	if resp.ContentLength > 0 {
		body = &progressReader{Reader: resp.Body, total: resp.ContentLength, onProgress: throttledProgressSender(ch, target, label)}
	}
	data, readErr := io.ReadAll(body)
	if readErr != nil {
		return openResultMsg{target: target, isAttachment: true, err: fmt.Errorf("reading download: %w", readErr)}
	}

	if iv != nil && key != nil {
		var decryptErr error
		data, decryptErr = aesgcm.Decrypt(data, iv, key)
		if decryptErr != nil {
			return openResultMsg{target: target, isAttachment: true, err: fmt.Errorf("decrypting file: %w", decryptErr)}
		}
	}

	f, openErr := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if os.IsExist(openErr) {
		// Lost a race with another concurrent open of the same attachment;
		// the other one already wrote dest.
		err := exec.Command("xdg-open", dest).Start()
		return openResultMsg{target: target, isAttachment: true, err: err}
	}
	if openErr != nil {
		return openResultMsg{target: target, isAttachment: true, err: openErr}
	}
	defer f.Close()
	if _, copyErr := io.Copy(f, bytes.NewReader(data)); copyErr != nil {
		os.Remove(dest)
		return openResultMsg{target: target, isAttachment: true, err: copyErr}
	}

	err := exec.Command("xdg-open", dest).Start()
	return openResultMsg{target: target, isAttachment: true, err: err}
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

// attachmentCacheDir returns where decrypted aesgcm:// attachments are
// cached for viewing (as opposed to downloadsDir, which is for explicit
// "Save As"), creating it if it doesn't exist yet.
func attachmentCacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("finding cache directory: %w", err)
	}
	dir := filepath.Join(base, "kage", "attachments")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	return dir, nil
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
func saveURLToDownloads(target string) tea.Cmd {
	ch := make(chan tea.Msg, 8)
	go func() {
		ch <- downloadToDest(target, "", ch)
	}()
	return listenForTransferChan(ch)
}

// saveURLToPath downloads target to the explicit destination path dest
// (from the "save as" prompt) instead of the default downloads directory,
// overwriting any existing file there — the user chose that exact path.
func saveURLToPath(target, dest string) tea.Cmd {
	ch := make(chan tea.Msg, 8)
	go func() {
		ch <- downloadToDest(target, dest, ch)
	}()
	return listenForTransferChan(ch)
}

// downloadToDest does the actual download+decrypt+write work for
// saveURLToDownloads/saveURLToPath, reporting progress on ch as it goes.
// dest is the explicit destination path to write to; if empty, one is
// computed from downloadsDir() + the URL's basename (deduped against
// existing files via uniqueDestPath). Split out so the Cmd constructors
// (which must return immediately) stay simple.
func downloadToDest(target, dest string, ch chan tea.Msg) tea.Msg {
	var downloadURL string
	var iv, key []byte
	var err error

	if strings.HasPrefix(target, "aesgcm://") {
		// Parse aesgcm:// URL to extract HTTPS URL, IV, and key
		downloadURL, iv, key, err = aesgcm.ParseAESGCMURL(target)
		if err != nil {
			return saveResultMsg{target: target, err: fmt.Errorf("parsing aesgcm URL: %w", err)}
		}
	} else if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		downloadURL = target
	} else {
		return saveResultMsg{target: target, err: fmt.Errorf("not a downloadable URL: %s", target)}
	}

	explicitDest := dest != ""
	if !explicitDest {
		dir, err := downloadsDir()
		if err != nil {
			return saveResultMsg{target: target, err: err}
		}
		base := filepath.Base(downloadURL)
		if idx := strings.IndexAny(base, "?#"); idx >= 0 {
			base = base[:idx]
		}
		if base == "" || base == "." || base == "/" {
			base = "download"
		}
		dest = uniqueDestPath(dir, base)
	} else if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return saveResultMsg{target: target, err: fmt.Errorf("creating %s: %w", filepath.Dir(dest), err)}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return saveResultMsg{target: target, err: err}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return saveResultMsg{target: target, err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return saveResultMsg{target: target, err: fmt.Errorf("download failed: HTTP %d", resp.StatusCode)}
	}

	label := "downloading " + attachmentDisplayName(target)
	body := io.Reader(resp.Body)
	if resp.ContentLength > 0 {
		body = &progressReader{Reader: resp.Body, total: resp.ContentLength, onProgress: throttledProgressSender(ch, target, label)}
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return saveResultMsg{target: target, err: fmt.Errorf("reading download: %w", err)}
	}

	// Decrypt if this is an aesgcm:// URL
	if iv != nil && key != nil {
		data, err = aesgcm.Decrypt(data, iv, key)
		if err != nil {
			return saveResultMsg{target: target, err: fmt.Errorf("decrypting file: %w", err)}
		}
	}

	writeFlags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if explicitDest {
		// The user typed this exact path in the "save as" prompt — overwrite
		// it rather than erroring, matching what a save-as dialog does.
		writeFlags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(dest, writeFlags, 0o644)
	if err != nil {
		return saveResultMsg{target: target, err: err}
	}
	defer f.Close()
	if _, err := io.Copy(f, bytes.NewReader(data)); err != nil {
		os.Remove(dest)
		return saveResultMsg{target: target, err: err}
	}
	return saveResultMsg{target: target, path: dest}
}
