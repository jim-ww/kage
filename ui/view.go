package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// View implements tea.Model.
func (m Model) View() tea.View {
	if m.width == 0 {
		v := tea.NewView("loading...")
		v.AltScreen = true
		v.ReportFocus = true
		return v
	}

	colors := m.styles.colors
	sw := m.sidebarWidth()
	scw := m.sidebarContentWidth()

	// ── Sidebar ────────────────────────────────────────────────────────────
	sidebarBorder := colors.borderD
	if m.selectedView == viewChats || m.selectedView == viewAccounts {
		sidebarBorder = colors.borderA
	}
	accountOpen := m.selectedView == viewAccounts
	accountBg := colors.panelEdge
	accountFg := colors.statusFg
	if accountOpen {
		accountBg = colors.borderA
		accountFg = colors.appBg
	}
	accountHovered := m.isHovered(zoneAccountBarName)
	if accountHovered && !accountOpen {
		accountFg = colors.accentCyan
	}
	accountName, accountStatus := m.renderAccountBar(scw, accountHovered, accountOpen)
	nameRow := m.zone.Mark(zoneAccountBarName, m.styles.accountBarNameRow(scw, accountBg, accountFg, accountName))
	statusRow := ""
	if accountOpen {
		// Only shown while the accounts panel itself is open — it's an
		// accounts-scoped action, and stays out of the way (full-width status
		// text) the rest of the time.
		keyIconHovered := m.isHovered(zoneStoragePasswordButton)
		keyIcon := m.zone.Mark(zoneStoragePasswordButton, m.styles.renderStoragePasswordButton(m.icons, keyIconHovered))
		keyIconWidth := lipgloss.Width(keyIcon)
		statusHovered := m.isHovered(zoneAccountBarStatus)
		statusText := m.zone.Mark(zoneAccountBarStatus, m.styles.accountBarStatusRow(max(1, scw-keyIconWidth), accountStatus, statusHovered))
		statusRow = keyIcon + statusText
	} else {
		statusHovered := m.isHovered(zoneAccountBarStatus)
		statusRow = m.zone.Mark(zoneAccountBarStatus, m.styles.accountBarStatusRow(scw, accountStatus, statusHovered))
	}
	statusLine := nameRow + "\n" + statusRow
	sidebarBody := m.chats.View()
	switch {
	case m.selectedView == viewAccounts:
		sidebarBody = m.renderAccountsList(scw)
	case len(m.chats.Items()) == 0 && m.currentAccountConnecting():
		sidebarBody = m.styles.accountNormal.Render("connecting...")
	}
	sidebarInner := lipgloss.JoinVertical(
		lipgloss.Left,
		statusLine,
		m.styles.sidebarInner(scw, max(0, m.height-sidebarStatusHeight), sidebarBody),
	)
	sidebar := ""
	if sw > 0 {
		sidebar = m.zone.Mark(zonePaneSidebar, m.styles.sidebarBox(sw, m.height, sidebarBorder, sidebarInner))
	}

	chatArea := ""
	if m.chatAreaWidth() > 0 {
		chatArea = m.renderChatArea(colors)
	}

	mainRow := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, chatArea)
	footerText := wrapFooterHint(m.keys.helpHint(m.selectedView, len(m.pendingAttachments) > 0), max(1, m.width-2), footerMaxLines)
	footer := m.styles.footerBar(m.width, footerText)
	rootRows := []string{mainRow}
	if m.callBarActive() {
		// A real, fixed layout row (not the toast overlay below) — only
		// present at all while a call is active, so idle layout is exactly
		// as before this existed. Reserved in updateSizes (callBarHeight).
		rootRows = append(rootRows, m.renderCallBar(m.width))
	}
	rootRows = append(rootRows, "", footer)
	root := m.styles.rootView(lipgloss.JoinVertical(lipgloss.Left, rootRows...))

	rendered := m.zone.Scan(root)
	var toastLines []string
	if transferLines := m.renderTransferLines(); len(transferLines) > 0 {
		toastLines = transferLines
	} else if m.noticeText != "" {
		toastLines = append(toastLines, m.noticeText)
	}
	if len(toastLines) > 0 {
		rendered = overlayBottomRight(rendered, m.styles.noticeToast(m.width, strings.Join(toastLines, "\n")))
	}

	v := tea.NewView(rendered)
	v.AltScreen = true
	v.ReportFocus = true
	// Ask Kitty-protocol terminals to report Key.BaseCode (the PC-101 key
	// regardless of active keyboard layout) alongside the layout-shifted
	// code. Key.String()/Keystroke() already prefer BaseCode when present,
	// so this makes "ctrl+e"-style bindings match the physical E key even
	// on non-Latin layouts (e.g. Cyrillic) without touching match logic.
	//
	// ReportAlternateKeys alone isn't enough: a plain "ctrl+<letter>" combo
	// already has an unambiguous legacy encoding (a single C0 control
	// byte), so terminals keep sending it that way — with no room to carry
	// BaseCode — unless ReportAllKeysAsEscapeCodes forces every key through
	// the full CSI u encoding instead.
	v.KeyboardEnhancements.ReportAlternateKeys = true
	v.KeyboardEnhancements.ReportAllKeysAsEscapeCodes = true
	// ReportAssociatedText asks the terminal to send the literal typed text
	// explicitly. Without it, the decoder falls back to ShiftedCode for
	// Key.Text on shift-held keys — but on non-Latin layouts terminals often
	// report ShiftedCode as the unshifted PC-101/Latin key, not the actual
	// shifted character.
	v.KeyboardEnhancements.ReportAssociatedText = true
	// TODO: use double-click event instead of our own double click logic? v.KeyboardEnhancements.ReportEventTypes
	if m.mouseEnabled {
		// AllMotion (not just CellMotion) so hover highlighting works without
		// a button held — see handleMouseMotion.
		v.MouseMode = tea.MouseModeAllMotion
	}
	return v
}

// renderChatArea builds the chat status bar + viewport/popup + input box
// column. Only called when chatAreaWidth() > 0 — in narrow mode that's
// exactly when the chat pane (rather than the chat list) is the single
// visible pane.
func (m Model) renderChatArea(colors uiColors) string {
	// ── Input box ──────────────────────────────────────────────────────────
	inputBorder := colors.borderD
	if m.selectedView == viewChat {
		inputBorder = colors.accentCyan
	}

	inputWidth := m.chatAreaWidth() - 2
	inputLine := m.styles.inputInnerBox(m.inputFieldWidth(), m.zone.Mark(zoneInputTextarea, m.input.View()))
	if m.mouseEnabled {
		attachBtn := m.zone.Mark(zoneAttachButton, m.styles.renderAttachButton(m.icons, m.isHovered(zoneAttachButton)))
		sendBtn := m.zone.Mark(zoneSendButton, m.styles.renderSendButton(m.isHovered(zoneSendButton)))
		inputLine = lipgloss.JoinHorizontal(lipgloss.Top, inputLine, attachBtn, strings.Repeat(" ", buttonGap), sendBtn)
	}
	var inputRows []string
	if hint := m.inputHint(); hint != "" {
		inputRows = append(inputRows, m.styles.inputInnerBox(inputWidth, hint))
	}
	if row := m.renderPendingAttachments(inputWidth); row != "" {
		inputRows = append(inputRows, m.styles.inputInnerBox(inputWidth, row))
	}
	inputRows = append(inputRows, inputLine)
	inputInner := strings.Join(inputRows, "\n")

	inputBox := m.zone.Mark(zonePaneInput, m.styles.inputContainer(inputBorder, inputInner))

	// ── Viewport / popup ───────────────────────────────────────────────────
	var viewportArea string
	switch {
	case m.contextMenu != nil:
		viewportArea = m.renderContextMenuPopup()
	case m.confirmTarget != confirmNone:
		viewportArea = m.renderDeletePopup()
	case m.showMsgInfo:
		viewportArea = m.renderInfoPopup()
	case m.showHelp:
		viewportArea = m.renderHelpPopup()
	case m.deviceList != nil:
		viewportArea = m.renderDeviceListPopup()
	case m.contactManagerState != nil:
		viewportArea = m.renderContactManagerPopup()
	case m.changePasswordState != nil:
		viewportArea = m.renderChangePasswordPopup()
	case m.addingAccount:
		viewportArea = m.renderAddAccountPopup()
	case m.renamingChat:
		viewportArea = m.renderRenameChatPopup()
	case m.savingAs:
		viewportArea = m.renderSaveAsPopup()
	case len(m.openItems) > 0:
		viewportArea = m.renderOpenPopup()
	case m.pickingFile:
		viewportArea = m.renderFilePickerPopup()
	default:
		viewportHeight := m.height - m.inputAreaHeight() - chatStatusHeight
		viewportBody := m.styles.viewportContent(m.chatAreaWidth(), viewportHeight, m.viewport.View())
		viewportArea = m.zone.Mark(zonePaneViewport, m.styles.viewportFrame(m.chatAreaWidth(), viewportHeight, viewportBody))
	}

	// renderChatArea is only called when the chat pane is the visible one, so
	// in narrow mode the list is by definition hidden right now — the icon
	// should always offer to bring it back, regardless of sidebarHidden
	// (which narrow mode ignores; see sidebarWidth/chatAreaWidth).
	listHidden := m.sidebarHidden || m.narrow()
	toggleBtn := m.zone.Mark(zoneToggleSidebar, m.styles.renderSidebarToggleButton(listHidden, m.isHovered(zoneToggleSidebar)))
	statusWidth := max(0, m.chatAreaWidth()-lipgloss.Width(toggleBtn))
	statusContent := m.renderChatStatusBar(statusWidth)
	if m.searchingChat {
		statusContent = m.renderSearchChatBar(statusWidth)
	}
	chatStatus := lipgloss.JoinHorizontal(
		lipgloss.Top,
		toggleBtn,
		m.zone.Mark(zoneChatStatusBar, m.styles.chatStatusLine(statusWidth, statusContent)),
	)
	return lipgloss.JoinVertical(lipgloss.Left, chatStatus, viewportArea, inputBox)
}

// renderCallBar builds the persistent call status bar row: plain status text
// plus real clickable [key] label buttons (zoneCallAnswer/Reject/Mute/Hangup)
// for whichever actions apply to the current call state — mirrors the
// zoneSendButton pattern (m.zone.Mark around a styles.render* button,
// hover-highlighted via m.isHovered). "" when there's no call to show,
// matching callBarActive so View() only reserves space for it when needed.
func (m Model) renderCallBar(width int) string {
	if m.call == nil {
		return ""
	}
	switch m.call.state {
	case "ringing-local":
		text := "📞 incoming call: " + m.call.peer + "   "
		answer := m.zone.Mark(zoneCallAnswer, m.styles.renderCallBarButton("[y] answer", m.isHovered(zoneCallAnswer)))
		reject := m.zone.Mark(zoneCallReject, m.styles.renderCallBarButton("[n] reject", m.isHovered(zoneCallReject)))
		return m.styles.callBarLine(width, text+answer+reject)

	case "proposing", "ringing-remote", "negotiating":
		verb := "calling"
		if m.call.state == "negotiating" {
			verb = "connecting to"
		}
		text := "📞 " + verb + " " + m.call.peer + "...   "
		hangup := m.zone.Mark(zoneCallHangup, m.styles.renderCallBarButton("[h] hang up", m.isHovered(zoneCallHangup)))
		return m.styles.callBarLine(width, text+hangup)

	case "connected":
		dur := "00:00"
		if !m.call.startedAt.IsZero() {
			dur = formatCallDuration(time.Since(m.call.startedAt))
		}
		mic := "🎤 unmuted"
		muteLabel := "[m] mute"
		if m.call.muted {
			mic = "🎤 muted"
			muteLabel = "[m] unmute"
		}
		quality := "📶 …"
		if m.call.quality != "" {
			quality = "📶 " + m.call.quality
		}
		text := "📞 " + m.call.peer + "  " + dur + "  " + mic + "  " + quality + "   "
		muteBtn := m.zone.Mark(zoneCallMute, m.styles.renderCallBarButton(muteLabel, m.isHovered(zoneCallMute)))
		hangupBtn := m.zone.Mark(zoneCallHangup, m.styles.renderCallBarButton("[h] hang up", m.isHovered(zoneCallHangup)))
		return m.styles.callBarLine(width, text+muteBtn+" "+hangupBtn)

	case "ended", "failed":
		// Terminal state: nothing left to click, just the plain self-clearing
		// text (callClearMsg drops m.call shortly after — see call.go).
		return m.styles.callBarLine(width, m.callBarLine())

	default:
		return ""
	}
}

// overlayBottomRight splices toast on top of base, anchored a small margin
// off the bottom-right corner of the screen. base is the already fully
// rendered (ANSI-styled) screen; toast is composited line-by-line with
// ansi.Cut so it doesn't break escape codes or wide-character boundaries in
// whatever base content it covers.
func overlayBottomRight(base, toast string) string {
	const marginBottom, marginRight = 2, 2

	baseLines := strings.Split(base, "\n")
	toastLines := strings.Split(toast, "\n")
	toastWidth := lipgloss.Width(toast)

	x := max(0, lipgloss.Width(base)-toastWidth-marginRight)
	y := max(0, len(baseLines)-len(toastLines)-marginBottom)

	for i, toastLine := range toastLines {
		row := y + i
		if row >= len(baseLines) {
			break
		}
		bg := baseLines[row]
		bgWidth := ansi.StringWidth(bg)
		left := ansi.Cut(bg, 0, min(x, bgWidth))
		right := ansi.Cut(bg, min(x+toastWidth, bgWidth), bgWidth)
		baseLines[row] = left + toastLine + right
	}
	return strings.Join(baseLines, "\n")
}

// wrapFooterHint word-wraps a helpHint string (entries joined by " · ", each
// entry internally held together with non-breaking spaces — see helpHint) to
// width, keeping at most maxLines rows. A single line's width, even on a
// wide terminal, isn't enough to list every binding for a busy view like
// viewChat, so this lets the footer grow down instead of truncating early;
// past maxLines the remaining entries are dropped with a trailing "…".
func wrapFooterHint(hint string, width, maxLines int) string {
	lines := footerWrapLines(hint, width, maxLines)
	return strings.Join(lines, "\n")
}

// footerLineCount is how many rows wrapFooterHint's output will actually
// take — used to size the rest of the layout so nothing is reserved for
// footer rows a view doesn't need. Kept in lockstep with wrapFooterHint by
// sharing footerWrapLines rather than reimplementing the wrap.
func footerLineCount(hint string, width, maxLines int) int {
	return len(footerWrapLines(hint, width, maxLines))
}

func footerWrapLines(hint string, width, maxLines int) []string {
	wrapped := lipgloss.Wrap(hint, width, "")
	lines := strings.Split(wrapped, "\n")
	if len(lines) <= maxLines {
		return lines
	}
	lines = lines[:maxLines]
	lines[maxLines-1] = ansi.Truncate(lines[maxLines-1], width, "…")
	return lines
}

// renderContextMenuPopup lists the actions available on whatever was
// right-clicked; each row is a marked zone so handleContextMenuClick can
// map a click back to the item that produced it. Rows are widened to a
// uniform size and, when there's room, separated by a blank line — packed
// edge-to-edge single-char-tall rows are easy to misclick. On short
// terminals the spacing is dropped rather than letting the popup get
// clipped; row width is always capped to the available chat-area width.
func (m Model) renderContextMenuPopup() string {
	cw := m.chatAreaWidth()
	vh := m.height - m.inputAreaHeight()

	// Leave room for the popup's border + Padding(1, 4) (see uiStyles.popup).
	maxItemWidth := max(6, cw-10)
	longest := 0
	for _, item := range m.contextMenu.items {
		if w := lipgloss.Width(item.label); w > longest {
			longest = w
		}
	}
	// contextMenuMinWidth keeps rows from shrink-wrapping to the longest
	// label alone — that reads as squeezed/cramped on anything but a
	// narrow terminal, where maxItemWidth still clamps it down.
	const contextMenuMinWidth = 20
	itemWidth := min(max(longest, contextMenuMinWidth), maxItemWidth)

	rows := make([]string, len(m.contextMenu.items))
	for i, item := range m.contextMenu.items {
		row := m.styles.contextMenuRow(item.label, m.isHovered(zoneContextMenuItem(i)), itemWidth)
		rows[i] = m.zone.Mark(zoneContextMenuItem(i), row)
	}

	sep := "\n"
	if vh >= len(rows)*2+4 {
		sep = "\n\n"
	}
	title := lipgloss.NewStyle().Bold(true).Foreground(m.styles.colors.borderA).Render("Actions")
	body := title + "\n" + strings.Join(rows, sep)
	popup := m.styles.popupDialog(m.styles.colors.borderA, body)

	return lipgloss.Place(cw, vh, lipgloss.Center, lipgloss.Center, popup)
}

// deletePromptWidth is the wrap width for any styles.deletePrompt call
// (this file, contacts.go, omemo_devices.go): -10 for the popup's own
// border+padding (2 for Border(1,1) + 8 for Padding(1,4)), capped at 50 so
// the dialog doesn't stretch edge-to-edge on wide terminals — content
// narrower than this just renders at its own width. Recomputed on every
// render from the current m.chatAreaWidth(), so it tracks live resizes.
func (m Model) deletePromptWidth() int {
	return min(max(1, m.chatAreaWidth()-10), 50)
}

// renderDeletePopup renders a centered confirmation dialog inside the viewport
// area instead of overlaying raw ANSI (simpler and more portable).
func (m Model) renderDeletePopup() string {
	cw := m.chatAreaWidth()
	vh := m.height - m.inputAreaHeight()

	popup := m.styles.popupDialog(m.styles.colors.borderA, m.deletePrompt(m.deletePromptWidth()))

	return lipgloss.Place(cw, vh, lipgloss.Center, lipgloss.Center, popup)
}

func (m Model) deletePrompt(width int) string {
	switch m.confirmTarget {
	case confirmQuit:
		return m.styles.deletePrompt(width, "Quit kage?", "")
	case confirmDeleteChat:
		detail := ""
		if chat, ok := m.currentChat(); ok {
			detail = chat.Name
			if chat.Address != "" {
				detail = fmt.Sprintf("%s <%s>", chat.Name, chat.Address)
			}
		}
		return m.styles.deletePrompt(width, "Leave chat?", detail)
	case confirmRemoveAccount:
		detail := ""
		if m.currentAccount >= 0 && m.currentAccount < len(m.accounts) {
			detail = m.accounts[m.currentAccount].DisplayName()
		}
		return m.styles.deletePrompt(width, "Remove account?", detail+" — disconnects and drops it from config; local history is kept")
	default:
		detail := ""
		if msgs := m.currentMessages(); m.selectedMsg >= 0 && m.selectedMsg < len(msgs) {
			msg := msgs[m.selectedMsg]
			detail = fmt.Sprintf("%s: %s", msg.Author, previewText(MessagePreviewContent(msg), previewLen))
		}
		return m.styles.deletePrompt(width, "Delete message?", detail)
	}
}

// renderInfoPopup shows metadata about the currently selected message.
func (m Model) renderInfoPopup() string {
	cw := m.chatAreaWidth()
	vh := m.height - m.inputAreaHeight()

	// popup padding (4 each side) + border (1 each side).
	popup := m.styles.popupDialog(m.styles.colors.borderA, m.infoPrompt(cw-10))
	popup = m.zone.Mark(zoneMsgInfoPopup, popup)

	return lipgloss.Place(cw, vh, lipgloss.Center, lipgloss.Center, popup)
}

// encryptionLabel describes a message's wire-level encryption state for the
// info popup: which mechanism (OMEMO/GPG) encrypted it, or that it went
// unencrypted.
func encryptionLabel(msg Message) string {
	if !msg.Encrypted {
		return "None (plaintext)"
	}
	switch msg.EncMethod {
	case "omemo-v1":
		return "OMEMO (v1)"
	case "omemo-v2":
		return "OMEMO (v2)"
	case "omemo":
		return "OMEMO"
	case "gpg":
		return "GPG"
	default:
		return "Yes"
	}
}

func (m Model) infoPrompt(width int) string {
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
		fmt.Sprintf("Encryption: %s", encryptionLabel(msg)),
	}
	if msg.ReplyTo != nil {
		rows = append(rows, fmt.Sprintf("Reply to: %s", m.replyPreview(*msg.ReplyTo, msgs)))
	}
	for i, a := range msg.Attachments {
		label := "Attachment:"
		if len(msg.Attachments) > 1 {
			label = fmt.Sprintf("Attachment %d:", i+1)
		}
		fg := m.styles.colors.themFg
		if m.isHovered(zoneMsgInfoAttachment(i)) {
			fg = m.styles.colors.accentCyan
		}
		url := lipgloss.NewStyle().Foreground(fg).Render(a)
		row := fmt.Sprintf("%s %s", label, url)
		row = ansi.Truncate(row, width, "…")
		rows = append(rows, m.zone.Mark(zoneMsgInfoAttachment(i), row))
	}
	if msg.Retracted {
		// The chat view hides deleted content, but we never actually erase
		// it locally — the info popup is where the original stays visible.
		rows = append(rows, "Deleted: yes (original content below)", fmt.Sprintf("Content: %s", msg.Content))
	}

	return m.styles.infoPopup("Message info", rows, closeKey)
}

// renderHelpPopup lists every keybinding grouped by which tab it applies to
// (plus a Global section for bindings that work everywhere) — the full
// reference behind the footer's necessarily-truncated one-line hint.
func (m Model) renderHelpPopup() string {
	closeKey := m.keys.Help.Help().Key
	var rows []string
	for i, section := range m.keys.helpSections() {
		if i > 0 {
			rows = append(rows, "")
		}
		title := lipgloss.NewStyle().Bold(true).Foreground(m.styles.colors.accentCyan).Render(section.Title)
		rows = append(rows, title)
		for _, e := range section.Entries {
			key := shortestKey(e.binding)
			if key == "" {
				continue
			}
			rows = append(rows, fmt.Sprintf("  %-14s %s", caretKey(key), e.desc))
		}
	}
	return m.styles.infoPopup("Help", rows, closeKey)
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
		// openableItems puts every real attachment before any plain link
		// found in Content (see m.openItemsAttachCount) - show attachments
		// by their decoded filename, not the raw URL (which for aesgcm://
		// also embeds the file's decryption key in its fragment). A plain
		// link is shown as-is: the URL itself is the meaningful thing there.
		label := item
		if start+i < m.openItemsAttachCount {
			label = attachmentDisplayName(item)
		}
		rows[i] = fmt.Sprintf("%d. %s", i+1, previewText(label, previewLen))
	}

	verb := "open"
	title := "Open — pick one"
	switch m.openMode {
	case pickerModeSave:
		verb = "save"
		title = "Save — pick one"
	case pickerModeSaveAs:
		verb = "save as"
		title = "Save as — pick one"
	}
	footer := "[1-9] " + verb + "  ·  [esc] cancel"
	if pages := openPageCount(len(m.openItems)); pages > 1 {
		title = fmt.Sprintf("%s (page %d/%d)", title, m.openPage+1, pages)
		footer = "[1-9] " + verb + "  ·  [←/→] page  ·  [esc] cancel"
	}

	body := m.styles.listPopup(title, rows, footer)
	popup := m.styles.popupDialog(m.styles.colors.borderA, body)

	return lipgloss.Place(cw, vh, lipgloss.Center, lipgloss.Center, popup)
}

// renderFilePickerPopup marks each rendered row of the file picker with its
// own zone (zoneFilePickerRow) so handleLeftClick can turn a click into the
// right number of synthetic up/down key presses — the picker's selection
// index isn't exported, so there's no other way to tell it "select row 3".
// Rows are padded to the widest row's width first so every row's click/hover
// zone spans the full list width, not just its own text (see
// padLinesToWidth).
func (m Model) renderFilePickerPopup() string {
	cw := m.chatAreaWidth()
	vh := m.height - m.inputAreaHeight()

	sortKey := caretKey(m.keys.SortFilePicker.Help().Key)
	title := "Attach file — " + m.filePicker.CurrentDirectory
	footer := "[enter] open/select  ·  [" + sortKey + "] sort: " + m.filePicker.SortLabel() + "  ·  [esc] cancel"

	// Set the picker's minimum row width to at least the title/footer's
	// width so short filenames don't leave size/date stranded mid-line —
	// the popup box always auto-sizes to whichever line (title, footer, or
	// a row) is widest, so without this, rows narrower than the title got
	// right-padded by padLinesToWidth *after* the date column instead of
	// widening the name column in front of it.
	picker := m.filePicker
	picker.SetWidth(max(lipgloss.Width(title), lipgloss.Width(footer)))
	lines := strings.Split(picker.View(), "\n")
	width := 0
	for _, line := range lines {
		if w := lipgloss.Width(line); w > width {
			width = w
		}
	}
	lines = strings.Split(padLinesToWidth(strings.Join(lines, "\n"), width), "\n")
	for i, line := range lines {
		lines[i] = m.zone.Mark(zoneFilePickerRow(i), line)
	}
	body := m.styles.listPopup(title, []string{strings.Join(lines, "\n")}, footer)
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

// renderChangePasswordPopup shows the "change local storage password"
// popup's two masked fields, plus a loud warning: unlike an account
// password, this key isn't recoverable from anywhere else (not the server,
// not a peer) — losing it means losing every locally-stored message/draft
// it protects.
func (m Model) renderChangePasswordPopup() string {
	cw := m.chatAreaWidth()
	vh := m.height - m.inputAreaHeight()

	s := m.changePasswordState
	rows := []string{
		m.styles.popupDanger.Render("Write this down. If you lose it, your local message history is unrecoverable."),
		"",
		s.inputs[0].View(),
		s.inputs[1].View(),
	}
	if s.busy {
		rows = append(rows, "", "re-encrypting local storage...")
	} else if s.err != "" {
		rows = append(rows, "", m.styles.popupDanger.Render(s.err))
	}

	footer := "[tab] next field  ·  [enter] change  ·  [esc] cancel"
	body := m.styles.listPopup("Change storage password", rows, footer)
	popup := m.styles.popupDialog(m.styles.colors.borderA, body)

	return lipgloss.Place(cw, vh, lipgloss.Center, lipgloss.Center, popup)
}

// renderRenameChatPopup shows the single-field rename-contact prompt.
func (m Model) renderRenameChatPopup() string {
	cw := m.chatAreaWidth()
	vh := m.height - m.inputAreaHeight()

	footer := "[enter] save  ·  [esc] cancel"
	body := m.styles.listPopup("Rename chat", []string{m.renameInput.View()}, footer)
	popup := m.styles.popupDialog(m.styles.colors.borderA, body)

	return lipgloss.Place(cw, vh, lipgloss.Center, lipgloss.Center, popup)
}

// renderSaveAsPopup shows the single-field save-as destination-path prompt.
func (m Model) renderSaveAsPopup() string {
	cw := m.chatAreaWidth()
	vh := m.height - m.inputAreaHeight()

	footer := "[enter] save  ·  [esc] cancel"
	body := m.styles.listPopup("Save as", []string{m.saveAsInput.View()}, footer)
	popup := m.styles.popupDialog(m.styles.colors.borderA, body)

	return lipgloss.Place(cw, vh, lipgloss.Center, lipgloss.Center, popup)
}

// renderAccountBar returns the account bar's name and status text
// separately, each already truncated to fit width (see
// uiStyles.accountBarNameRow/accountBarStatusRow, which style them).
func (m Model) renderAccountBar(width int, hovered, open bool) (name, status string) {
	if len(m.accounts) == 0 {
		return ansi.Truncate("no accounts", max(1, width-2), "…"), ""
	}
	name, status = "accounts", ""
	if m.currentAccount >= 0 && m.currentAccount < len(m.accounts) {
		account := m.accounts[m.currentAccount]
		name = account.DisplayName()
		status = account.StatusText() + " " + presenceGlyph(account.Status)
	}
	w := max(1, width-2)
	if hovered || open {
		arrow := "[▼]"
		if open {
			arrow = "[▲]"
		}
		nameW := max(1, w-len(arrow)-1)
		name = ansi.Truncate(name, nameW, "…")
		pad := max(1, w-len(name)-len(arrow))
		name += strings.Repeat(" ", pad) + arrow
	} else {
		name = ansi.Truncate(name, w, "…")
	}
	return name, ansi.Truncate(status, w, "…")
}

// renderAccountsList renders one row per account, current one highlighted,
// for display in the sidebar while viewAccounts is focused.
func (m Model) renderAccountsList(width int) string {
	if len(m.accounts) == 0 {
		return m.styles.accountNormal.Render("no accounts configured")
	}
	rows := make([]string, len(m.accounts))
	for i, account := range m.accounts {
		label := fmt.Sprintf("%s (%s)", account.DisplayName(), account.StatusText())
		// The glyph is rendered separately and prepended after styling the
		// rest of the row, never passed through renderAccountRow's own
		// style.Render - that call ends in a full ANSI reset, and wrapping
		// it in another style (hover's Underline) produces broken/nested
		// escape sequences that some terminals show as raw text instead of
		// rendering (see the same reasoning in renderChatStatusBar).
		name := ansi.Truncate(label, max(1, width-5), "…") // -5 for border + padding + glyph + space
		row := m.styles.renderAccountRow(name, i == m.currentAccount, m.isHovered(zoneAccountRow(i)))
		rows[i] = m.zone.Mark(zoneAccountRow(i), presenceGlyph(account.Status)+" "+row)
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
			return m.styles.renderReplyHint(orig.Author, previewText(MessagePreviewContent(orig), previewLen))
		}
	}
	if m.reactingMsgIdx >= 0 {
		msgs := m.currentMessages()
		if m.reactingMsgIdx < len(msgs) {
			return m.renderReactHint(previewText(msgs[m.reactingMsgIdx].Content, previewLen))
		}
	}
	return ""
}

// renderPendingAttachments renders the row of chips for files staged (via
// the attach button/ctrl+f) to go out with the next sent message — each
// chip is its own clickable zone (see zoneAttachmentRemove) that removes it;
// empty if nothing is staged. The one Tab last landed on
// (m.selectedAttachment — also what ctrl+o opens and Backspace removes) is
// wrapped in brackets so it's always clear which one those keys act on.
func (m Model) renderPendingAttachments(width int) string {
	if len(m.pendingAttachments) == 0 {
		return ""
	}
	chips := make([]string, len(m.pendingAttachments))
	for i, a := range m.pendingAttachments {
		icon := attachmentIcon(a.name, m.icons)
		chip := m.styles.renderAttachmentChip(icon, a.name, i == m.selectedAttachment, m.isHovered(zoneAttachmentRemove(i)))
		chips[i] = m.zone.Mark(zoneAttachmentRemove(i), chip)
	}
	return ansi.Truncate(strings.Join(chips, "  "), max(1, width), "…")
}

// renderReactHint renders the "react to ..." line plus the live
// emoji-shortcode suggestions, each one a marked, hoverable zone so a click
// accepts it exactly like pressing tab after arrowing onto it.
func (m Model) renderReactHint(target string) string {
	hint := fmt.Sprintf("react to %q", target)
	if len(m.emojiSuggestions) == 0 {
		return m.styles.messageReply.Render(hint)
	}

	codes := make([]string, len(m.emojiSuggestions))
	for i, sug := range m.emojiSuggestions {
		label := sug.Emoji + sug.Shortcode
		styled := m.styles.emojiSuggestionLabel(label, i == m.emojiSuggestIdx, m.isHovered(zoneEmojiSuggestion(i)))
		codes[i] = m.zone.Mark(zoneEmojiSuggestion(i), styled)
	}
	hint += "  →  " + strings.Join(codes, " ") + "  [←/→] pick · [tab/enter/click] accept"
	return m.styles.messageReply.Render(hint)
}

func (m Model) renderChatStatusBar(width int) string {
	chat, ok := m.currentChat()
	if !ok {
		return ""
	}

	label := chat.Name
	switch {
	case chat.Address != "" && chat.Address != chat.Name:
		label = fmt.Sprintf("%s <%s>", chat.Name, chat.Address)
	case chat.Address != "":
		label = chat.Address
	case strings.HasPrefix(chat.Name, "#"):
		label = chat.Name
	}
	if chat.Typing {
		label += "  ·  typing..."
	}
	if m.currentAccount >= 0 && m.currentAccount < len(m.accounts) && m.accounts[m.currentAccount].SyncingHistory {
		label += "  ·  syncing history..."
	}
	// nickMe-color the text ourselves rather than let chatStatusLine wrap
	// the whole string in one Foreground — presenceGlyph's own Render call
	// ends in a full ANSI reset, which would otherwise cut the outer color
	// off right after the dot, leaving the rest of the label uncolored.
	label = m.styles.messageNickMe.Render(label)

	return ansi.Truncate(label, max(1, width-2), "…")
}

// renderSearchChatBar shows the live search-in-chat prompt in place of the
// chat status bar row while searchingChat is true: the text input plus a
// running "match N/M" (or "no matches") count.
func (m Model) renderSearchChatBar(width int) string {
	status := "no matches"
	if len(m.searchMatches) > 0 {
		status = fmt.Sprintf("%d/%d", m.searchMatchPos+1, len(m.searchMatches))
	}
	line := m.searchInput.View() + "  " + status
	return ansi.Truncate(line, max(1, width-2), "…")
}
