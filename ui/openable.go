package ui

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

var urlPattern = regexp.MustCompile(`https?://\S+`)

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
func openWithXDGOpen(target string) tea.Cmd {
	return func() tea.Msg {
		err := exec.Command("xdg-open", target).Start()
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

// saveURLToDownloads downloads target (an http/https URL — everything
// openableItems surfaces is one, whether a peer's attachment or our own
// just-uploaded file) into the user's downloads directory.
func saveURLToDownloads(target string) tea.Cmd {
	return func() tea.Msg {
		if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
			return saveResultMsg{err: fmt.Errorf("not a downloadable URL: %s", target)}
		}
		dir, err := downloadsDir()
		if err != nil {
			return saveResultMsg{err: err}
		}
		base := filepath.Base(target)
		if idx := strings.IndexAny(base, "?#"); idx >= 0 {
			base = base[:idx]
		}
		if base == "" || base == "." || base == "/" {
			base = "download"
		}
		dest := uniqueDestPath(dir, base)

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
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

		f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return saveResultMsg{err: err}
		}
		defer f.Close()
		if _, err := io.Copy(f, resp.Body); err != nil {
			os.Remove(dest)
			return saveResultMsg{err: err}
		}
		return saveResultMsg{path: dest}
	}
}
