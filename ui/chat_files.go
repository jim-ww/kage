package ui

import (
	"fmt"
	"strings"
	"time"

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
	// The list reads sizes through m, which may be an older copy of the
	// model by then: maps made now are the same ones every copy sees.
	if m.attachmentSizes == nil {
		m.attachmentSizes = make(map[string]int64)
	}
	if m.attachmentSizeFetching == nil {
		m.attachmentSizeFetching = make(map[string]bool)
	}
	if m.attachmentSizeFailed == nil {
		m.attachmentSizeFailed = make(map[string]bool)
	}
	menu := modal.NewMenu()
	menu.Styles.Cursor = onAccent(m.styles.colors.accentCyan)
	menu.MatchKey = matchesLetter
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
					return body.sizesCmd()
				},
			})
		},
	})
	return tea.Batch(open, m.daemon.ChatFiles(accountIdx, chat.Address))
}

// fillChatFiles puts msg's files into the open files list, if it's still
// the one for that chat: one row per attachment, newest first.
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
	files := make([]Message, 0, len(msg.Messages))
	for _, f := range msg.Messages {
		for _, a := range f.Attachments {
			f.Attachments = []string{a}
			files = append(files, f)
		}
	}
	body.messages, body.names = files, make([]string, len(files))
	body.matches = make([]int, len(files))
	for i := range files {
		body.matches[i] = i
	}
	body.relist()
	return body.sizesCmd()
}

// chatFileSizesMsg asks for the sizes of the files a files list shows:
// only those in view, so a long list never asks for thousands at once.
type chatFileSizesMsg struct {
	jid     string
	targets []string
}

// fetchChatFileSizes finds msg's sizes not known yet: from a local copy, or
// else asked of the host as message info does (see fetchAttachmentSizeCmd).
func (m *Model) fetchChatFileSizes(msg chatFileSizesMsg) tea.Cmd {
	var cmds []tea.Cmd
	for _, target := range msg.targets {
		cmds = append(cmds, m.fetchAttachmentSize(target, msg.jid))
	}
	m.relabelChatFiles()
	return tea.Batch(cmds...)
}

// relabelChatFiles redraws the open files list's rows in view, for a size
// that just arrived.
func (m *Model) relabelChatFiles() {
	if md := m.modals.Find(chatFilesModalID); md != nil {
		body := md.Body.(*searchResultsBody)
		body.m = m
		body.label()
	}
}

// fetchAttachmentSize asks for target's size unless it's known, already
// being asked for, or failed before. A local copy's size is taken as known.
func (m *Model) fetchAttachmentSize(target, jid string) tea.Cmd {
	if _, ok := m.attachmentSizes[target]; ok || m.attachmentSizeFetching[target] || m.attachmentSizeFailed[target] {
		return nil
	}
	if size, ok := attachmentLocalSize(target, jid); ok {
		if m.attachmentSizes == nil {
			m.attachmentSizes = make(map[string]int64)
		}
		m.attachmentSizes[target] = size
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
func (m Model) fileResultLabel(name string, msg Message) (text, right string) {
	glyph := "«"
	if msg.IsMe {
		glyph = "»"
	}
	date := searchResultDate(msg.SentAt, time.Now(), m.formatMessageTime(msg.SentAt))
	return glyph + " " + attachmentIcon(name, m.icons) + " " + name, fmt.Sprintf("%*s  %s", sizeColumn, m.fileSizeLabel(msg.Attachments[0]), date)
}

// fileSizeLabel is a files-list row's size: known, "…" while being asked
// for, blank when there's no telling. Never touches the disk: it's drawn
// on every frame.
func (m Model) fileSizeLabel(target string) string {
	if size, ok := m.attachmentSizes[target]; ok {
		return humanFileSize(size)
	}
	if m.attachmentSizeFetching[target] {
		return "…"
	}
	return ""
}
