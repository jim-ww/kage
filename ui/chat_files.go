package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jim-ww/kage/ui/modal"
)

// chatFilesModalID finds the open files list, to fill it when
// Daemon.ChatFiles is done and as attachment sizes arrive.
const chatFilesModalID = "chat-files"

// sizeColumn is how wide a file row's size is padded to ("999.9 KB"), so
// dates line up whatever the sizes.
const sizeColumn = 8

// openChatFiles lists every attachment in the current chat, newest first,
// narrowed by filter (a name, empty for all): the search results' list,
// with names, sizes and dates for rows. Busy until ChatFilesMsg brings
// them; picking one jumps the chat to its message.
func (m *Model) openChatFiles(filter string) tea.Cmd {
	chat, ok := m.currentChat()
	if !ok || m.currentChatIndex() < 0 {
		return nil
	}
	accountIdx := m.currentAccount
	menu := modal.NewMenu()
	menu.Styles.Cursor = onAccent(m.styles.colors.accentCyan)
	menu.Styles.Hover = lipgloss.NewStyle().Foreground(m.styles.colors.accentCyan)
	body := &searchResultsBody{Menu: menu, accountIdx: accountIdx, chatAddress: chat.Address, peerName: chat.Name, files: true, filter: strings.TrimSpace(filter), status: "loading…", m: m}
	open := m.openModal(&modal.Modal[*Model]{
		ID:       chatFilesModalID,
		Title:    "Files",
		Body:     body,
		MaxWidth: body.PreferredWidth(),
		OnSubmit: func(m *Model, v any) tea.Cmd {
			return m.loadSearchResult(body.accountIdx, body.chatAddress, body.messages[body.shown[v.(int)]])
		},
		OnAct: func(m *Model, _ any) tea.Cmd {
			return m.openModal(&modal.Modal[*Model]{
				Title: "Filter files",
				Body:  m.newInput(body.filter, "filter by name", "/ ", "filter"),
				OnSubmit: func(m *Model, v any) tea.Cmd {
					body.filter = v.(string)
					body.relist()
					return nil
				},
			})
		},
	})
	return tea.Batch(open, m.daemon.ChatFiles(accountIdx, chat.Address))
}

// fillChatFiles puts msg's files into the open files list, if it's still
// the one for that chat: one row per attachment, newest first. Sizes not
// known yet are asked for here, on opening the list, as message info does
// (see fetchAttachmentSizeCmd).
func (m *Model) fillChatFiles(msg ChatFilesMsg) tea.Cmd {
	md := m.modals.Find(chatFilesModalID)
	if md == nil {
		return nil
	}
	body := md.Body.(*searchResultsBody)
	body.m = m
	if body.accountIdx != msg.AccountIdx || body.chatAddress != msg.From {
		return nil
	}
	body.status = ""
	if msg.Err != nil {
		body.err = msg.Err.Error()
		return nil
	}
	var files []Message
	for i := len(msg.Messages) - 1; i >= 0; i-- {
		for _, a := range msg.Messages[i].Attachments {
			f := msg.Messages[i]
			f.Attachments = []string{a}
			files = append(files, f)
		}
	}
	body.messages = files
	body.matches = make([]int, len(files))
	for i := range files {
		body.matches[i] = i
	}
	body.relist()

	var cmds []tea.Cmd
	for _, f := range files {
		cmds = append(cmds, m.fetchAttachmentSize(f.Attachments[0], msg.From))
	}
	return tea.Batch(cmds...)
}

// relistChatFiles redraws the open files list's rows, for a size that just
// arrived.
func (m *Model) relistChatFiles() {
	if md := m.modals.Find(chatFilesModalID); md != nil {
		body := md.Body.(*searchResultsBody)
		body.m = m
		body.relist()
	}
}

// fetchAttachmentSize asks for target's size unless it's known, cached
// locally, already being asked for, or failed before.
func (m *Model) fetchAttachmentSize(target, jid string) tea.Cmd {
	if _, ok := attachmentLocalSize(target, jid); ok {
		return nil
	}
	if _, ok := m.attachmentSizes[target]; ok || m.attachmentSizeFetching[target] || m.attachmentSizeFailed[target] {
		return nil
	}
	if m.attachmentSizeFetching == nil {
		m.attachmentSizeFetching = make(map[string]bool)
	}
	m.attachmentSizeFetching[target] = true
	return fetchAttachmentSizeCmd(target)
}

// fileResultLabel formats a files-list row's two parts, like
// searchResultLabel: the direction glyph, icon and file name, and its size
// and date.
func (m Model) fileResultLabel(jid string, msg Message) (text, right string) {
	glyph := "«"
	if msg.IsMe {
		glyph = "»"
	}
	target := msg.Attachments[0]
	name := attachmentDisplayName(target)
	_, date := m.searchResultLabel(msg)
	return glyph + " " + attachmentIcon(name, m.icons) + " " + name, fmt.Sprintf("%*s  %s", sizeColumn, m.fileSizeLabel(target, jid), date)
}

// fileSizeLabel is a files-list row's size: known, "…" while being asked
// for, blank when there's no telling.
func (m Model) fileSizeLabel(target, jid string) string {
	if size, ok := attachmentLocalSize(target, jid); ok {
		return humanFileSize(size)
	}
	if size, ok := m.attachmentSizes[target]; ok {
		return humanFileSize(size)
	}
	if m.attachmentSizeFetching[target] {
		return "…"
	}
	return ""
}
