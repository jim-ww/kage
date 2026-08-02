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
	sidebarBody := m.chats.View()
	if m.selectedView == viewAccounts {
		sidebarBody = m.renderAccountsList(sw)
	}
	sidebarInner := lipgloss.JoinVertical(lipgloss.Left,
		statusLine,
		m.styles.sidebarInner(sw, max(0, m.height-sidebarStatusHeight), sidebarBody),
	)
	sidebar := m.styles.sidebarBox(sw, m.height, sidebarBorder, sidebarInner)

	// ── Input box ──────────────────────────────────────────────────────────
	inputBorder := colors.borderD
	if m.selectedView == viewChat {
		inputBorder = colors.accentCyan
	}

	inputWidth := m.chatAreaWidth() - 2
	var inputInner string
	if hint := m.inputHint(); hint != "" {
		inputInner = m.styles.inputInnerBox(inputWidth, hint) + "\n" + m.styles.inputInnerBox(inputWidth, m.input.View())
	} else {
		inputInner = m.styles.inputInnerBox(inputWidth, m.input.View())
	}

	inputBox := m.styles.inputContainer(inputBorder, inputInner)

	// ── Viewport / popup ───────────────────────────────────────────────────
	var viewportArea string
	switch {
	case m.confirmTarget != confirmNone:
		viewportArea = m.renderDeletePopup()
	case m.showMsgInfo:
		viewportArea = m.renderInfoPopup()
	case m.addingAccount:
		viewportArea = m.renderAddAccountPopup()
	case len(m.openItems) > 0:
		viewportArea = m.renderOpenPopup()
	default:
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

	mainRow := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, chatArea)
	footerText := ansi.Truncate(m.keys.helpHint(m.selectedView), max(1, m.width-2), "…")
	footer := m.styles.footerBar(m.width, footerText)
	root := m.styles.rootView(lipgloss.JoinVertical(lipgloss.Left, mainRow, footer))

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
		detail := ""
		if chat, ok := m.currentChat(); ok {
			detail = chat.Name
			if chat.Address != "" {
				detail = fmt.Sprintf("%s <%s>", chat.Name, chat.Address)
			}
		}
		return m.styles.deletePrompt("Leave chat?", detail)
	default:
		detail := ""
		if msgs := m.currentMessages(); m.selectedMsg >= 0 && m.selectedMsg < len(msgs) {
			msg := msgs[m.selectedMsg]
			detail = fmt.Sprintf("%s: %s", msg.Author, previewText(msg.Content, previewLen))
		}
		return m.styles.deletePrompt("Delete message?", detail)
	}
}

// renderInfoPopup shows metadata about the currently selected message.
func (m Model) renderInfoPopup() string {
	cw := m.chatAreaWidth()
	vh := m.height - m.inputAreaHeight()

	popup := m.styles.popupDialog(m.styles.colors.borderA, m.infoPrompt())

	return lipgloss.Place(cw, vh, lipgloss.Center, lipgloss.Center, popup)
}

func (m Model) infoPrompt() string {
	closeKey := m.keys.InfoMsg.Help().Key
	msgs := m.currentMessages()
	if m.selectedMsg < 0 || m.selectedMsg >= len(msgs) {
		return m.styles.infoPopup("Message info", nil, closeKey)
	}
	msg := msgs[m.selectedMsg]

	from := msg.Author
	if msg.IsMe {
		from = msg.Author + " (you)"
	}

	rows := []string{
		fmt.Sprintf("From:  %s", from),
		fmt.Sprintf("Sent:  %s", msg.SentAt.Format("2006-01-02 15:04:05")),
		fmt.Sprintf("Length: %d chars", len([]rune(msg.Content))),
	}
	if msg.ReplyTo != nil {
		rows = append(rows, fmt.Sprintf("Reply to: %s", m.replyPreview(*msg.ReplyTo, msgs)))
	}

	return m.styles.infoPopup("Message info", rows, closeKey)
}

// renderOpenPopup lists the pending link/attachment choices, numbered within
// the current page; left/right page through when there are more than
// openItemsPerPage items.
func (m Model) renderOpenPopup() string {
	cw := m.chatAreaWidth()
	vh := m.height - m.inputAreaHeight()

	start, end := openPageBounds(len(m.openItems), m.openPage)
	page := m.openItems[start:end]
	rows := make([]string, len(page))
	for i, item := range page {
		rows[i] = fmt.Sprintf("%d. %s", i+1, previewText(item, previewLen))
	}

	title := "Open — pick one"
	footer := "[1-9] open  ·  [esc] cancel"
	if pages := openPageCount(len(m.openItems)); pages > 1 {
		title = fmt.Sprintf("%s (page %d/%d)", title, m.openPage+1, pages)
		footer = "[1-9] open  ·  [←/→] page  ·  [esc] cancel"
	}

	body := m.styles.listPopup(title, rows, footer)
	popup := m.styles.popupDialog(m.styles.colors.borderA, body)

	return lipgloss.Place(cw, vh, lipgloss.Center, lipgloss.Center, popup)
}

// renderAddAccountPopup shows the add-account form: one line per field
// (JID, password, GPG key ID), the focused field highlighted by its own
// textinput cursor, plus an error line if the last attempt failed.
func (m Model) renderAddAccountPopup() string {
	cw := m.chatAreaWidth()
	vh := m.height - m.inputAreaHeight()

	rows := make([]string, len(m.addAccountInputs))
	for i, field := range m.addAccountInputs {
		rows[i] = field.View()
	}
	if m.addAccountBusy {
		rows = append(rows, "", "connecting...")
	} else if m.addAccountErr != "" {
		rows = append(rows, "", m.styles.popupDanger.Render(m.addAccountErr))
	}

	footer := "[tab] next field  ·  [enter] add  ·  [esc] cancel"
	body := m.styles.listPopup("Add account", rows, footer)
	popup := m.styles.popupDialog(m.styles.colors.borderA, body)

	return lipgloss.Place(cw, vh, lipgloss.Center, lipgloss.Center, popup)
}

func (m Model) renderAccountBar(width int) string {
	if len(m.accounts) == 0 {
		return "accounts: none"
	}
	label := "accounts"
	if m.currentAccount >= 0 && m.currentAccount < len(m.accounts) {
		label = fmt.Sprintf("accounts: %s", m.accounts[m.currentAccount].Name)
	}
	return ansi.Truncate(label, max(1, width-2), "…")
}

// renderAccountsList renders one row per account, current one highlighted,
// for display in the sidebar while viewAccounts is focused.
func (m Model) renderAccountsList(width int) string {
	if len(m.accounts) == 0 {
		return m.styles.accountNormal.Render("no accounts configured")
	}
	rows := make([]string, len(m.accounts))
	for i, account := range m.accounts {
		name := ansi.Truncate(account.Name, max(1, width-3), "…") // -3 for border + padding
		rows[i] = m.styles.renderAccountRow(name, i == m.currentAccount)
	}
	return strings.Join(rows, "\n")
}

// inputHint renders the optional line shown above the input box: a reply
// quote, or the reacting-to hint plus live emoji-shortcode suggestions.
// Empty if neither applies.
func (m Model) inputHint() string {
	if m.replyToIdx >= 0 {
		msgs := m.currentMessages()
		if m.replyToIdx < len(msgs) {
			orig := msgs[m.replyToIdx]
			return m.styles.renderReplyHint(orig.Author, previewText(orig.Content, previewLen))
		}
	}
	if m.reactingMsgIdx >= 0 {
		msgs := m.currentMessages()
		if m.reactingMsgIdx < len(msgs) {
			return m.styles.renderReactHint(previewText(msgs[m.reactingMsgIdx].Content, previewLen), m.emojiSuggestions, m.emojiSuggestIdx)
		}
	}
	return ""
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
	if chat.Address != "" {
		label = presenceGlyph(chat.Presence) + " " + label
	}
	if chat.Typing {
		label += "  ·  typing..."
	}

	return ansi.Truncate(label, max(1, width-2), "…")
}
