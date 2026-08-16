package ui

import (
	"fmt"
	"path/filepath"

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
// that's already staged toggles it off (removes its chip) instead of
// adding a second copy.
func (m *Model) stageAttachment(path string) {
	for i, a := range m.pendingAttachments {
		if a.path == path {
			m.removeAttachment(i)
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

// composeBodyWithAttachments builds the wire body for a single-attachment
// message: text first (if any), then the URL on its own line, same "body is
// the URL" convention SendFile already uses.
func composeBodyWithAttachments(text string, url string) string {
	if text == "" {
		return url
	}
	return text + "\n" + url
}

// startAttachedSend uploads every staged attachment and sends each as its
// own single-attachment message, as a single Bubble Tea command — uploading
// (like SendFile) can take a while, so it must not block Update(). Multiple
// files aren't combined into one message: other clients (verified against
// Dino) only read the first XEP-0066 <x xmlns='jabber:x:oob'>/attachment
// URL in a message and silently drop the rest, so a "combined" message
// would only ever show its first file to anyone else. text (if any) goes
// only on the first message; every message shares reply's ReplyToID, so a
// multi-file reply threads all of them to the same original message.
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
		result := ComposedSendResultMsg{AccountIdx: accountIdx, To: to, ReplyToID: reply.ReplyToID, Paths: paths}
		for i, path := range paths {
			msgText := ""
			if i == 0 {
				msgText = text
			}
			sendOpts := reply
			// A LocalID up front, same as sendCurrentInput's live-echo path -
			// needed whether this ends up queued (identifies the outbox row,
			// and the placeholder built for it below) or sent live (identifies
			// nothing here, but costs nothing to always set).
			sendOpts.LocalID = newLocalID()
			// upload+send is one atomic unit if the account is offline
			// (fileSender.UploadFile queues both together, see its
			// docstring) - pass msgText/sendOpts through so it has
			// everything needed to replay this file's message later.
			upload, ok := fileSender.UploadFile(accountIdx, to, path, msgText, sendOpts).(FileUploadResultMsg)
			if !ok {
				result.Err = fmt.Errorf("uploading %s: unexpected result", filepath.Base(path))
				return result
			}
			if upload.Queued {
				// Nothing to send yet - no URL. The rest of the batch would
				// hit the same offline account, so stop here rather than
				// queuing files one at a time across separate flush passes.
				result.Queued = true
				result.QueuedLocalID = sendOpts.LocalID
				result.QueuedPath = path
				result.QueuedText = msgText
				return result
			}
			if upload.Err != nil {
				result.Err = fmt.Errorf("uploading %s: %w", filepath.Base(path), upload.Err)
				return result
			}

			body := composeBodyWithAttachments(msgText, upload.URL)
			sendOpts.OOBURLs = []string{upload.URL}
			id, err := sender.Send(accountIdx, to, body, sendOpts)
			if err != nil {
				result.Err = fmt.Errorf("sending %s: %w", filepath.Base(path), err)
				return result
			}
			result.Messages = append(result.Messages, SentMessage{ID: id, Content: body, Attachments: []string{upload.URL}})
		}
		return result
	}
}
