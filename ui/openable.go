package ui

import (
	"os/exec"
	"regexp"

	tea "charm.land/bubbletea/v2"
)

var urlPattern = regexp.MustCompile(`https?://\S+`)

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
