package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func (m Model) View() tea.View {
	if m.width == 0 {
		v := tea.NewView("loading...")
		v.AltScreen = true
		return v
	}

	colors := m.styles.colors
	sw := m.sidebarWidth()

	// ── Sidebar ────────────────────────────────────────────────────────────
	sidebarBorder := colors.borderD
	if m.selectedView == viewChats || m.selectedView == viewAccounts {
		sidebarBorder = colors.borderA
	}
	accountBg := colors.panelEdge
	accountFg := colors.statusFg
	if m.selectedView == viewAccounts {
		accountBg = colors.borderA
		// accountFg = colors.appBg
	}
	statusLine := m.styles.sidebarStatusLine(sw, accountBg, accountFg, m.renderAccountBar(sw))
	sidebarInner := lipgloss.JoinVertical(lipgloss.Left,
		statusLine,
		m.styles.sidebarInner(sw, max(0, m.height-sidebarStatusHeight), m.chats.View()),
	)
	sidebar := m.styles.sidebarBox(sw, m.height, sidebarBorder, sidebarInner)

	// ── Input box ──────────────────────────────────────────────────────────
	inputBorder := colors.borderD
	if m.selectedView == viewChat {
		inputBorder = colors.accentCyan
	}

	var inputInner string
	inputWidth := m.chatAreaWidth() - 2
	if m.replyToIdx >= 0 {
		chatIdx := m.currentChatIndex()
		if chatIdx >= 0 {
			msgs := m.currentMessages()
			if m.replyToIdx < len(msgs) {
				orig := msgs[m.replyToIdx]
				preview := strings.ReplaceAll(orig.Content, "\n", " ")
				runes := []rune(preview)
				if len(runes) > 40 {
					preview = string(runes[:37]) + "…"
				}
				hint := m.styles.renderReplyHint(orig.Author, preview)
				inputInner = m.styles.inputInnerBox(inputWidth, hint) + "\n" + m.styles.inputInnerBox(inputWidth, m.input.View())
			} else {
				inputInner = m.styles.inputInnerBox(inputWidth, m.input.View())
			}
		} else {
			inputInner = m.styles.inputInnerBox(inputWidth, m.input.View())
		}
	} else {
		inputInner = m.styles.inputInnerBox(inputWidth, m.input.View())
	}

	inputBox := m.styles.inputContainer(inputBorder, inputInner)

	// ── Viewport / popup ───────────────────────────────────────────────────
	var viewportArea string
	if m.confirmTarget != confirmNone {
		viewportArea = m.renderDeletePopup()
	} else {
		viewportHeight := m.height - m.inputAreaHeight() - chatStatusHeight
		contentHeight := viewportHeight
		if m.noticeText != "" && contentHeight > 1 {
			contentHeight--
		}
		viewportBody := m.styles.viewportContent(m.chatAreaWidth(), contentHeight, m.viewport.View())
		if m.noticeText != "" {
			notice := m.styles.noticeBar(m.chatAreaWidth(), m.noticeText)
			viewportBody = lipgloss.JoinVertical(lipgloss.Left, viewportBody, notice)
		}
		viewportArea = m.styles.viewportFrame(m.chatAreaWidth(), viewportHeight, viewportBody)
	}

	chatStatus := m.styles.sidebarStatusLine(
		m.chatAreaWidth(),
		colors.panelEdge,
		colors.statusFg,
		m.renderChatStatusBar(m.chatAreaWidth()),
	)
	chatArea := lipgloss.JoinVertical(lipgloss.Left, chatStatus, viewportArea, inputBox)

	root := m.styles.rootView(lipgloss.JoinHorizontal(lipgloss.Top, sidebar, chatArea))

	v := tea.NewView(root)
	v.AltScreen = true
	return v
}

// renderDeletePopup renders a centered confirmation dialog inside the viewport
// area instead of overlaying raw ANSI (simpler and more portable).
func (m Model) renderDeletePopup() string {
	cw := m.chatAreaWidth()
	vh := m.height - m.inputAreaHeight()

	popup := m.styles.popupDialog(m.styles.colors.borderA, m.deletePrompt())

	return lipgloss.Place(cw, vh, lipgloss.Center, lipgloss.Center, popup)
}

func (m Model) deletePrompt() string {
	switch m.confirmTarget {
	case confirmDeleteChat:
		return m.styles.deletePrompt("Leave chat?")
	default:
		return m.styles.deletePrompt("Delete message?")
	}
}

func (m Model) renderAccountBar(width int) string {
	if len(m.accounts) == 0 {
		return "accounts: none"
	}
	parts := make([]string, 0, len(m.accounts)+1)
	parts = append(parts, "accounts:")
	for i, account := range m.accounts {
		name := account.Name
		switch {
		case i == m.currentAccount && m.selectedView == viewAccounts:
			name = "[" + name + "]"
		case i == m.currentAccount:
			name = "<" + name + ">"
		}
		parts = append(parts, name)
	}
	return ansi.Truncate(strings.Join(parts, " "), max(1, width-2), "…")
}

func (m Model) renderChatStatusBar(width int) string {
	chat, ok := m.currentChat()
	if !ok {
		return ""
	}

	label := chat.Name
	switch {
	case chat.Address != "":
		label = fmt.Sprintf("%s <%s>", chat.Name, chat.Address)
	case strings.HasPrefix(chat.Name, "#"):
		label = chat.Name
	}

	return ansi.Truncate(label, max(1, width-2), "…")
}
