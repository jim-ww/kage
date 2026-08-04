package ui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jim-ww/kage/crypto/aesgcm"
	tea "charm.land/bubbletea/v2"
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
type pickerMode int

const (
	pickerModeOpen pickerMode = iota
	pickerModeSave
)

type openResultMsg struct {
	target string
	err    error
}

// openWithXDGOpen shells out to xdg-open in the background; the result is
// reported back as an openResultMsg so the UI can show a toast.
// For aesgcm:// URLs, the file is downloaded, decrypted, and then opened.
func openWithXDGOpen(target string) tea.Cmd {
	return func() tea.Msg {
		var openTarget string
		var err error

		if strings.HasPrefix(target, "aesgcm://") {
			// For aesgcm:// URLs, we need to download and decrypt before
			// xdg-open can view it. Cache the decrypted file by a hash of
			// the full target (URL+iv+key all factor into the plaintext),
			// so re-opening the same attachment for viewing reuses the
			// already-downloaded copy instead of writing a new one each
			// time (unlike an explicit "Save As", which always wants a
			// fresh, uniquely-named file in Downloads).
			downloadURL, iv, key, parseErr := aesgcm.ParseAESGCMURL(target)
			if parseErr != nil {
				return openResultMsg{target: target, err: fmt.Errorf("parsing aesgcm URL: %w", parseErr)}
			}

			dir, dirErr := attachmentCacheDir()
			if dirErr != nil {
				return openResultMsg{target: target, err: dirErr}
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
				err = exec.Command("xdg-open", dest).Start()
				return openResultMsg{target: target, err: err}
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
			if reqErr != nil {
				return openResultMsg{target: target, err: reqErr}
			}
			resp, httpErr := http.DefaultClient.Do(req)
			if httpErr != nil {
				return openResultMsg{target: target, err: httpErr}
			}
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return openResultMsg{target: target, err: fmt.Errorf("download failed: HTTP %d", resp.StatusCode)}
			}

			data, readErr := io.ReadAll(resp.Body)
			if readErr != nil {
				return openResultMsg{target: target, err: fmt.Errorf("reading download: %w", readErr)}
			}

			data, decryptErr := aesgcm.Decrypt(data, iv, key)
			if decryptErr != nil {
				return openResultMsg{target: target, err: fmt.Errorf("decrypting file: %w", decryptErr)}
			}

			f, openErr := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
			if os.IsExist(openErr) {
				// Lost a race with another concurrent open of the same
				// attachment; the other one already wrote dest.
				err = exec.Command("xdg-open", dest).Start()
				return openResultMsg{target: target, err: err}
			}
			if openErr != nil {
				return openResultMsg{target: target, err: openErr}
			}
			defer f.Close()
			if _, copyErr := io.Copy(f, bytes.NewReader(data)); copyErr != nil {
				os.Remove(dest)
				return openResultMsg{target: target, err: copyErr}
			}

			openTarget = dest
		} else {
			openTarget = target
		}

		err = exec.Command("xdg-open", openTarget).Start()
		return openResultMsg{target: target, err: err}
	}
}

type saveResultMsg struct {
	path string // destination path on success
	err  error
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
// just-uploaded file) into the user's downloads directory.
// For aesgcm:// URLs (XEP-0454), the file is decrypted after download.
func saveURLToDownloads(target string) tea.Cmd {
	return func() tea.Msg {
		var downloadURL string
		var iv, key []byte
		var err error

		if strings.HasPrefix(target, "aesgcm://") {
			// Parse aesgcm:// URL to extract HTTPS URL, IV, and key
			downloadURL, iv, key, err = aesgcm.ParseAESGCMURL(target)
			if err != nil {
				return saveResultMsg{err: fmt.Errorf("parsing aesgcm URL: %w", err)}
			}
		} else if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
			downloadURL = target
		} else {
			return saveResultMsg{err: fmt.Errorf("not a downloadable URL: %s", target)}
		}

		dir, err := downloadsDir()
		if err != nil {
			return saveResultMsg{err: err}
		}
		base := filepath.Base(downloadURL)
		if idx := strings.IndexAny(base, "?#"); idx >= 0 {
			base = base[:idx]
		}
		if base == "" || base == "." || base == "/" {
			base = "download"
		}
		dest := uniqueDestPath(dir, base)

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
		if err != nil {
			return saveResultMsg{err: err}
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return saveResultMsg{err: err}
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return saveResultMsg{err: fmt.Errorf("download failed: HTTP %d", resp.StatusCode)}
		}

		// Read the downloaded data
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return saveResultMsg{err: fmt.Errorf("reading download: %w", err)}
		}

		// Decrypt if this is an aesgcm:// URL
		if iv != nil && key != nil {
			data, err = aesgcm.Decrypt(data, iv, key)
			if err != nil {
				return saveResultMsg{err: fmt.Errorf("decrypting file: %w", err)}
			}
		}

		f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return saveResultMsg{err: err}
		}
		defer f.Close()
		if _, err := io.Copy(f, bytes.NewReader(data)); err != nil {
			os.Remove(dest)
			return saveResultMsg{err: err}
		}
		return saveResultMsg{path: dest}
	}
}
