package ui

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// pendingAttachment is one file staged (via the file picker) to go out with
// the next sent message — shown as a chip above the compose box. Nothing is
// uploaded until the message is actually sent (see startAttachedSend).
type pendingAttachment struct {
	path string
	name string
}

// stageAttachment adds path as a pending attachment and selects it — the
// file picker stays open (see update_keys.go) so more can be staged before
// sending. The set of staged paths has no duplicates: re-selecting a file
// that's already staged just (re)selects its existing chip instead of
// adding a second copy.
func (m *Model) stageAttachment(path string) {
	for i, a := range m.pendingAttachments {
		if a.path == path {
			m.selectedAttachment = i
			return
		}
	}
	m.pendingAttachments = append(m.pendingAttachments, pendingAttachment{
		path: path,
		name: filepath.Base(path),
	})
	m.selectedAttachment = len(m.pendingAttachments) - 1
}

// removeAttachment drops the pending attachment at idx (e.g. Backspace on
// the selected one, or clicking its chip) and keeps selectedAttachment
// pointing at a valid index (or -1 once the list is empty).
func (m *Model) removeAttachment(idx int) {
	if idx < 0 || idx >= len(m.pendingAttachments) {
		return
	}
	m.pendingAttachments = append(m.pendingAttachments[:idx], m.pendingAttachments[idx+1:]...)
	switch {
	case len(m.pendingAttachments) == 0:
		m.selectedAttachment = -1
	case m.selectedAttachment >= len(m.pendingAttachments):
		m.selectedAttachment = len(m.pendingAttachments) - 1
	}
}

// cycleSelectedAttachment moves selectedAttachment forward by one,
// wrapping back to 0 past the end — bound to Tab while attachments are
// staged (see update_keys.go).
func (m *Model) cycleSelectedAttachment() {
	if len(m.pendingAttachments) == 0 {
		return
	}
	m.selectedAttachment = (m.selectedAttachment + 1) % len(m.pendingAttachments)
}

// composeBodyWithAttachments builds the wire body for a message that
// combines typed text with attachment URLs: text first (if any), then one
// URL per line, same "body is the URL" convention SendFile already uses for
// a single attachment — any client renders each linkified URL as its own
// attachment.
func composeBodyWithAttachments(text string, urls []string) string {
	parts := make([]string, 0, len(urls)+1)
	if text != "" {
		parts = append(parts, text)
	}
	parts = append(parts, urls...)
	return strings.Join(parts, "\n")
}

// startAttachedSend uploads every staged attachment and sends one message
// combining text with all their URLs, as a single Bubble Tea command —
// uploading (like SendFile) can take a while, so it must not block Update().
// Nothing is uploaded until this actually runs, i.e. not until the user
// hits send; staging a file earlier (stageAttachment) never touches the
// network. reply carries the same reply metadata the text-only send path
// resolves inline, since by the time the async upload finishes the message
// being replied to might have scrolled out of m.currentMessages().
func (m *Model) startAttachedSend(text string, to string, reply SendOptions) tea.Cmd {
	if m.fileSender == nil || m.sender == nil {
		return m.showNotification("file sending unavailable")
	}
	accountIdx := m.currentAccount
	paths := make([]string, len(m.pendingAttachments))
	for i, a := range m.pendingAttachments {
		paths[i] = a.path
	}
	fileSender := m.fileSender
	sender := m.sender

	return func() tea.Msg {
		urls := make([]string, len(paths))
		for i, path := range paths {
			result, ok := fileSender.UploadFile(accountIdx, to, path).(FileUploadResultMsg)
			if !ok {
				return ComposedSendResultMsg{AccountIdx: accountIdx, To: to, Err: fmt.Errorf("uploading %s: unexpected result", filepath.Base(path))}
			}
			if result.Err != nil {
				return ComposedSendResultMsg{AccountIdx: accountIdx, To: to, Err: fmt.Errorf("uploading %s: %w", filepath.Base(path), result.Err)}
			}
			urls[i] = result.URL
		}

		body := composeBodyWithAttachments(text, urls)
		id, err := sender.Send(accountIdx, to, body, reply)
		if err != nil {
			return ComposedSendResultMsg{AccountIdx: accountIdx, To: to, Err: err}
		}
		return ComposedSendResultMsg{
			AccountIdx:  accountIdx,
			To:          to,
			ID:          id,
			Content:     body,
			Attachments: urls,
			ReplyToID:   reply.ReplyToID,
		}
	}
}
