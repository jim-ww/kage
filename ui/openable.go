package ui

import (
	"os/exec"
	"regexp"

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
// the message body, attachments first.
func openableItems(msg Message) []string {
	items := make([]string, 0, len(msg.Attachments))
	items = append(items, msg.Attachments...)
	items = append(items, urlPattern.FindAllString(msg.Content, -1)...)
	return items
}

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
