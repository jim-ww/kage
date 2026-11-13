package ui

import (
	"fmt"
	"image/color"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jim-ww/kage/ui/modal"
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
	accountRow := lipgloss.NewStyle().Background(colors.panelEdge).Foreground(colors.statusFg)
	accountHovered := m.isHovered(zoneAccountBarName)
	switch {
	case accountOpen:
		accountRow = onAccent(colors.borderA)
	case accountHovered:
		accountRow = accountRow.Foreground(colors.accentCyan)
	}
	// Built first, so the name is laid out in what the button leaves.
	menuBtn := m.zone.Mark(zoneAccountMenuButton, m.styles.renderAccountMenuButton(m.icons, accountRow, m.isHovered(zoneAccountMenuButton)))
	nameWidth := max(1, scw-lipgloss.Width(menuBtn))
	accountName, accountStatus := m.renderAccountBar(nameWidth, scw, accountHovered, accountOpen)
	nameRow := m.zone.Mark(zoneAccountBarName, m.styles.accountBarNameRow(nameWidth, accountRow, accountName)) + menuBtn
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
	sidebarBody := m.chatListBody()
	switch {
	case m.selectedView == viewAccounts:
		sidebarBody = m.renderAccountsList(scw)
	case len(m.chats.Items()) == 0 && m.currentAccountConnecting():
		sidebarBody = m.styles.accountNormal.Render("connecting...")
	}
	sidebar := ""
	if sw > 0 {
		innerHeight := max(0, m.height-sidebarStatusHeight)
		sidebar = m.zone.Mark(zonePaneSidebar, m.renderSidebar(sidebarCacheEntry{
			statusLine: statusLine,
			body:       sidebarBody,
			width:      scw,
			height:     innerHeight,
			boxWidth:   sw,
			boxHeight:  m.height,
			border:     sidebarBorder,
		}))
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
	if panel, px, py, ok := m.avatarPanelOverlay(sidebarBody); ok {
		rendered = overlayAtWidth(rendered, panel, px, py, m.sidebarContentWidth())
	}
	var toastLines []string
	if transferLines := m.renderTransferLines(); len(transferLines) > 0 {
		toastLines = transferLines
	} else if m.noticeText != "" {
		toastLines = append(toastLines, m.noticeText)
	}
	if len(toastLines) > 0 {
		rendered = overlayBottomRight(rendered, m.styles.noticeToast(m.width, strings.Join(toastLines, "\n")))
	}
	if popup, x, y, ok := m.renderDeviceHoverPopup(); ok {
		rendered = overlayAt(rendered, popup, x, y)
	}

	v := tea.NewView(rendered)
	v.AltScreen = true
	v.ReportFocus = true
	// Kitty-protocol terminals report Key.BaseCode, the physical key, so
	// bindings match on non-Latin layouts too. Every key as an escape code:
	// a legacy-encoded ctrl+letter can't carry BaseCode.
	v.KeyboardEnhancements.ReportAlternateKeys = true
	v.KeyboardEnhancements.ReportAllKeysAsEscapeCodes = true
	// The typed text itself: on non-Latin layouts ShiftedCode is often the
	// Latin key.
	v.KeyboardEnhancements.ReportAssociatedText = true
	// TODO: use double-click event instead of our own double click logic? v.KeyboardEnhancements.ReportEventTypes
	if m.mouseEnabled {
		// AllMotion (not just CellMotion) so hover highlighting works without
		// a button held — see handleMouseMotion.
		v.MouseMode = tea.MouseModeAllMotion
	}
	return v
}

// sidebarCacheEntry is every input of the rendered sidebar (see
// renderSidebar). The colors used are comparable values, so == compares
// them.
type sidebarCacheEntry struct {
	statusLine string
	body       string
	width      int
	height     int
	boxWidth   int
	boxHeight  int
	border     color.Color
	rendered   string
}

// chatListCacheEntry memoizes m.chats.View()'s output against a key built
// from its inputs — see chatListBody.
type chatListCacheEntry struct {
	key  string
	body string
}

// chatListBody returns the rendered chat list, reusing the previous frame's
// when nothing it draws from changed, so renderSidebar's cache doesn't need
// it rendered. The key is the Chat fields, cheaper than the rendered rows,
// compared in full (see TestChatListKeyCoversEverythingRendered).
func (m Model) chatListBody() string {
	c := m.chatListViewCache
	if c == nil {
		return m.chats.View()
	}
	key := m.chatListViewKey()
	if c.key == key {
		return c.body
	}
	body := m.chats.View()
	c.key, c.body = key, body
	return body
}

// chatListViewKey encodes everything the chat list's rendering varies on:
// the list's own geometry/cursor/paging/filter state, which row the
// pointer is over (zoneChatListDelegate.Render reads it), and the fields
// of each visible Chat that Title/Description are derived from.
func (m Model) chatListViewKey() string {
	var sb strings.Builder
	items := m.chats.VisibleItems()
	sb.Grow(64 * (len(items) + 1))
	// The hovered chat row, not the hover ID, which changes over messages.
	hoveredChat := -1
	if i, ok := chatIndexFromZone(m.hover.id); ok {
		hoveredChat = i
	}
	fmt.Fprintf(&sb, "%d|%d|%d|%d|%d|%d|%s|%d\x00",
		m.chats.Width(), m.chats.Height(), m.chats.Index(),
		m.chats.Paginator.Page, m.chats.Paginator.TotalPages,
		m.chats.FilterState(), m.chats.FilterValue(), hoveredChat)
	for _, it := range items {
		chat, ok := it.(Chat)
		if !ok {
			// Not a Chat, so its rendering isn't described by the fields
			// below — fall back to a key that never matches.
			fmt.Fprintf(&sb, "?%s\x00", it.FilterValue())
			continue
		}
		fmt.Fprintf(&sb, "%s\x01%s\x01%s\x01%d\x01%d\x01%t\x01%t\x00",
			chat.Name, chat.Address, chat.LastMessage, chat.Presence, chat.Unread, chat.Pinned, chat.Muted)
	}
	return sb.String()
}

// renderSidebar renders the sidebar, reusing the previous frame's when every
// input is the same (see Model.sidebarRenderCache).
func (m Model) renderSidebar(want sidebarCacheEntry) string {
	c := m.sidebarRenderCache
	if c.statusLine == want.statusLine && c.body == want.body && c.width == want.width &&
		c.height == want.height && c.boxWidth == want.boxWidth && c.boxHeight == want.boxHeight &&
		c.border == want.border {
		return c.rendered
	}
	inner := lipgloss.JoinVertical(
		lipgloss.Left,
		want.statusLine,
		m.styles.sidebarInner(want.width, want.height, want.body),
	)
	want.rendered = m.styles.sidebarBox(want.boxWidth, want.boxHeight, want.border, inner)
	*c = want
	return want.rendered
}

// viewportFrameCacheEntry is every input of the framed message viewport
// (see renderViewportFrame), compared like sidebarCacheEntry.
type viewportFrameCacheEntry struct {
	width, height int
	content       string
	rendered      string
}

// renderViewportFrame frames rendered viewport content, reusing the previous
// frame's when its inputs are the same.
func (m Model) renderViewportFrame(want viewportFrameCacheEntry) string {
	c := m.viewportFrameCache
	if c.width == want.width && c.height == want.height && c.content == want.content {
		return c.rendered
	}
	// The viewport already sizes its view; when that's the frame's size,
	// lipgloss's sizing passes would only re-scan it unchanged (see
	// TestViewportFramePresizedMatchesSizedRender).
	if m.viewport.Width() == want.width && m.viewport.Height() == want.height {
		want.rendered = m.styles.viewportFramePresized(want.content)
	} else {
		want.rendered = m.styles.viewportFrame(want.width, want.height, m.styles.viewportContent(want.width, want.height, want.content))
	}
	*c = want
	return want.rendered
}

// renderChatArea builds the chat column: status bar, messages, input.
// Only called when chatAreaWidth() > 0.
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
	case m.modals.Open():
		viewportArea = m.modals.View()
	default:
		viewportHeight := m.popupAreaHeight()
		viewportArea = m.zone.Mark(zonePaneViewport, m.renderViewportFrame(viewportFrameCacheEntry{
			width:   m.chatAreaWidth(),
			height:  viewportHeight,
			content: m.viewport.View(),
		}))
		if m.currentChatIndex() >= 0 && m.scrolledPastFirstPage() {
			btn := m.zone.Mark(zoneJumpToBottom, m.styles.renderJumpToBottomButton(m.isHovered(zoneJumpToBottom), caretKey(shortestKey(m.keys.JumpLatest))))
			viewportArea = overlayBottomCenter(viewportArea, btn, 1)
		}
	}

	// In narrow mode the list is hidden whenever the chat shows.
	listHidden := m.sidebarHidden || m.narrow()
	toggleBtn := m.zone.Mark(zoneToggleSidebar, m.styles.renderSidebarToggleButton(listHidden, m.isHovered(zoneToggleSidebar)))
	statusWidth := max(0, m.chatAreaWidth()-lipgloss.Width(toggleBtn))
	chatStatus := lipgloss.JoinHorizontal(
		lipgloss.Top,
		toggleBtn,
		m.zone.Mark(zoneChatStatusBar, m.styles.chatStatusLine(statusWidth, m.renderChatStatusBar(statusWidth))),
	)
	return lipgloss.JoinVertical(lipgloss.Left, chatStatus, viewportArea, inputBox)
}

// renderCallBar builds the call bar's one row: status, then buttons, the
// least important dropped when narrow (fitCallBarButtons) - the menu last.
// "" without a call.
func (m Model) renderCallBar(width int) string {
	if m.call == nil {
		return ""
	}
	inner := max(0, width-2) // callBar's own horizontal padding
	buttons := m.callBarButtons()
	rendered := func(bs []callBarButton) (string, int) {
		parts := make([]string, len(bs))
		for i, b := range bs {
			parts[i] = m.zone.Mark(b.zone, m.styles.renderCallBarButton("["+b.label+"]", m.isHovered(b.zone)))
		}
		out := strings.Join(parts, m.styles.callBarText(" "))
		return out, lipgloss.Width(out)
	}
	buttons = fitCallBarButtons(buttons, inner, func(bs []callBarButton) int {
		_, w := rendered(bs)
		return w
	})
	btns, btnsWidth := rendered(buttons)

	room := inner
	if btnsWidth > 0 {
		room -= btnsWidth + 1
	}
	status := ansi.Truncate(m.callStatus(), max(0, room), "…")
	gap := max(0, inner-lipgloss.Width(status)-btnsWidth)
	return m.styles.callBarLine(width, m.styles.callBarText(status+strings.Repeat(" ", gap))+btns)
}

// overlayBottomRight splices toast on top of base, anchored a small margin
// off the bottom-right corner of the screen.
func overlayBottomRight(base, toast string) string {
	const marginBottom, marginRight = 2, 2
	baseLines, toastLines := strings.Split(base, "\n"), strings.Split(toast, "\n")
	x := max(0, lipgloss.Width(base)-lipgloss.Width(toast)-marginRight)
	y := max(0, len(baseLines)-len(toastLines)-marginBottom)
	return overlayAt(base, toast, x, y)
}

// overlayBottomCenter splices content on top of base, horizontally centered
// and anchored marginBottom rows off the bottom edge of base.
func overlayBottomCenter(base, content string, marginBottom int) string {
	baseLines, contentLines := strings.Split(base, "\n"), strings.Split(content, "\n")
	x := max(0, (lipgloss.Width(base)-lipgloss.Width(content))/2)
	y := max(0, len(baseLines)-len(contentLines)-marginBottom)
	return overlayAt(base, content, x, y)
}

// renderDeviceHoverPopup builds the hovered chat's online devices box, laid
// over the frame. ok is false with nothing to show.
func (m Model) renderDeviceHoverPopup() (popup string, x, y int, ok bool) {
	if m.hover == nil || m.hover.devicesID == "" || m.hover.id != m.hover.devicesID {
		return "", 0, 0, false
	}
	idx, isChatZone := chatIndexFromZone(m.hover.devicesID)
	if !isChatZone {
		return "", 0, 0, false
	}
	items := m.chats.Items()
	if idx < 0 || idx >= len(items) {
		return "", 0, 0, false
	}
	chat, isChat := items[idx].(Chat)
	if !isChat {
		return "", 0, 0, false
	}

	var lines []string
	switch {
	case len(chat.Resources) > 0:
		lines = make([]string, len(chat.Resources))
		for i, r := range chat.Resources {
			name := r.Name
			if name == "" {
				name = resourceDisplayName(r.Resource)
			}
			lines[i] = deviceGlyph(r.Presence) + " " + name
		}
	case chat.Presence == PresenceOffline:
		lines = []string{deviceGlyph(PresenceOffline) + " offline"}
	default:
		// Roster says this contact is online/away/etc but no per-resource
		// presence has arrived (e.g. a resource-less presence stanza) —
		// fall back to the aggregate presence rather than showing nothing.
		lines = []string{deviceGlyph(chat.Presence) + " " + presenceLabel(chat.Presence)}
	}

	z := m.zone.Get(m.hover.devicesID)
	if z.IsZero() {
		return "", 0, 0, false
	}

	// No background: the styled text inside has none, and would show
	// patches of the terminal's.
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.styles.colors.accentCyan).
		Padding(0, 1)
	popup = box.Render(strings.Join(lines, "\n"))

	x = z.EndX + 1
	if popupWidth := lipgloss.Width(popup); x+popupWidth > m.width {
		x = max(0, m.width-popupWidth)
	}
	y = z.StartY
	if popupHeight := len(lines) + 2; y+popupHeight > m.height {
		y = max(0, m.height-popupHeight)
	}
	return popup, x, y, true
}

// overlayAt splices content on top of base at cell position (x, y). base is
// the already fully rendered (ANSI-styled) block; content is composited
// line-by-line with ansi.Cut so it doesn't break escape codes or
// wide-character boundaries in whatever base content it covers.
func overlayAt(base, content string, x, y int) string {
	return overlayAtWidth(base, content, x, y, lipgloss.Width(content))
}

// overlayAtWidth is overlayAt for content of known width, saving measuring
// it - most of the cost for the avatar panel.
func overlayAtWidth(base, content string, x, y, contentWidth int) string {
	baseLines := strings.Split(base, "\n")
	contentLines := strings.Split(content, "\n")

	for i, contentLine := range contentLines {
		row := y + i
		if row >= len(baseLines) {
			break
		}
		bg := baseLines[row]
		bgWidth := ansi.StringWidth(bg)
		left := ansi.Cut(bg, 0, min(x, bgWidth))
		right := ansi.Cut(bg, min(x+contentWidth, bgWidth), bgWidth)
		baseLines[row] = left + contentLine + right
	}
	return strings.Join(baseLines, "\n")
}

// wrapFooterHint wraps a helpHint to width in at most maxLines rows, ending
// with "…" if cut.
func wrapFooterHint(hint string, width, maxLines int) string {
	lines := footerWrapLines(hint, width, maxLines)
	return strings.Join(lines, "\n")
}

// footerLineCount is how many rows wrapFooterHint takes.
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

// infoLines is the message-info modal's content for the selected message:
// each attachment's line copies its URL when acted on.
func (m Model) infoLines() []modal.Line {
	msgs := m.currentMessages()
	if m.selectedMsg < 0 || m.selectedMsg >= len(msgs) {
		return nil
	}
	msg := msgs[m.selectedMsg]

	from := msg.Author
	if msg.IsMe {
		from = msg.Author + " (you)"
	}
	lines := []modal.Line{
		{Text: fmt.Sprintf("From:  %s", from)},
		{Text: fmt.Sprintf("Sent:  %s", msg.SentAt.Format("2006-01-02 15:04:05"))},
		{Text: fmt.Sprintf("Length: %d chars", len([]rune(msg.Content)))},
		{Text: fmt.Sprintf("Encryption: %s", encryptionLabel(msg))},
	}
	if replyIdx := messageIndexByID(msgs, msg.ReplyToID); replyIdx >= 0 {
		lines = append(lines, modal.Line{Text: fmt.Sprintf("Reply to: %s", m.replyPreview(replyIdx, msgs))})
	}
	chat, _ := m.currentChat()
	for i, a := range msg.Attachments {
		label := "Attachment:"
		if len(msg.Attachments) > 1 {
			label = fmt.Sprintf("Attachment %d:", i+1)
		}
		lines = append(lines,
			modal.Line{Text: label + " " + a, Value: a},
			modal.Line{Text: "  Size: " + m.attachmentSizeLabel(a, chat.Address)})
	}
	if msg.Retracted {
		// The chat view hides deleted content, but we never actually erase
		// it locally — the info modal is where the original stays visible.
		lines = append(lines, modal.Line{Text: "Deleted: yes (original content below)"}, modal.Line{Text: fmt.Sprintf("Content: %s", msg.Content)})
	}
	return lines
}

// infoModalID finds the open message-info modal, to refresh it as the
// attachment sizes it shows arrive.
const infoModalID = "msg-info"

// openHelp lists every keybinding by view, in as many columns as fit.
func (m *Model) openHelp() tea.Cmd {
	var sections []modal.Section
	for _, section := range m.keys.helpSections() {
		var lines []string
		for _, e := range section.Entries {
			if key := shortestKey(e.binding); key != "" {
				lines = append(lines, fmt.Sprintf("%-11s %s", caretKey(key), e.desc))
			}
		}
		sections = append(sections, modal.Section{Title: section.Title, Lines: lines})
	}
	body := modal.NewColumns(sections...)
	body.TitleStyle = lipgloss.NewStyle().Bold(true).Foreground(m.styles.colors.accentCyan)
	// As wide as its columns, however many the height makes - not capped
	// at a dialog's width.
	return m.openModal(&modal.Modal[*Model]{Title: "Help", Body: body, MaxWidth: 1 << 16})
}

// renderAccountBar builds the account bar's name and status rows. The name
// row is narrower, sharing its row with the account menu button.
func (m Model) renderAccountBar(nameWidth, statusWidth int, hovered, open bool) (name, status string) {
	if len(m.accounts) == 0 {
		return ansi.Truncate("no accounts", max(1, nameWidth-2), "…"), ""
	}
	name, status = "accounts", ""
	if m.currentAccount >= 0 && m.currentAccount < len(m.accounts) {
		account := m.accounts[m.currentAccount]
		name = account.DisplayName()
		status = account.StatusText() + " " + presenceGlyph(account.Status)
	}
	w := max(1, nameWidth-2)
	if hovered || open {
		arrow := "[▼]"
		if open {
			arrow = "[▲]"
		}
		arrowW := lipgloss.Width(arrow)
		name = ansi.Truncate(name, max(1, w-arrowW-1), "…")
		pad := max(1, w-lipgloss.Width(name)-arrowW)
		name += strings.Repeat(" ", pad) + arrow
	} else {
		name = ansi.Truncate(name, w, "…")
	}
	return name, ansi.Truncate(status, max(1, statusWidth-2), "…")
}

// renderAccountsList renders one row per account, current one highlighted,
// for display in the sidebar while viewAccounts is focused.
func (m Model) renderAccountsList(width int) string {
	if len(m.accounts) == 0 {
		return m.styles.accountNormal.Render("no accounts configured")
	}
	rows := make([]string, len(m.accounts))
	for i, account := range m.accounts {
		label := fmt.Sprintf("%s (%s)", account.NameWithJID(), account.StatusText())
		// The glyph is prepended after styling: a styled string nested in
		// another style breaks on some terminals.
		name := ansi.Truncate(label, max(1, width-5), "…") // -5 for border + padding + glyph + space
		row := m.styles.renderAccountRow(name, i == m.currentAccount, m.isHovered(zoneAccountRow(i)))
		rows[i] = m.zone.Mark(zoneAccountRow(i), presenceGlyph(account.Status)+" "+row)
	}
	return strings.Join(rows, "\n")
}

// inputHint renders the optional reply-quote line shown above the input box.
// Empty if not currently replying. Reacting no longer has a compose-area
// hint of its own — it's a popup now (see renderEmojiPickerPopup).
func (m Model) inputHint() string {
	if !m.replyTo.empty() {
		msgs := m.currentMessages()
		if idx := m.replyTo.index(msgs); idx >= 0 {
			orig := msgs[idx]
			hint := m.styles.renderReplyHint(orig.Author, previewText(MessagePreviewContent(orig), previewLen))
			// Clickable to cancel the pending reply (see zoneReplyHintCancel in
			// mouse.go).
			return m.zone.Mark(zoneReplyHintCancel, hint)
		}
	}
	return ""
}

// renderPendingAttachments renders the staged files, each removed by a
// click; the selected one bracketed.
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

// chatStatusLabel is the status bar's headline text: who the chat is with,
// plus whatever transient state (typing, history sync) is worth saying
// alongside it.
func (m Model) chatStatusLabel(chat Chat) string {
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
		label += " · typing..."
	}
	if m.currentAccount >= 0 && m.currentAccount < len(m.accounts) && m.accounts[m.currentAccount].SyncingHistory {
		label += " · syncing history..."
	}
	return label
}

func (m Model) renderChatStatusBar(width int) string {
	chat, ok := m.currentChat()
	if !ok {
		return ""
	}

	label := m.chatStatusLabel(chat)
	// Colored here: the glyph's reset would cut off an outer color.
	label = m.styles.messageNickMe.Render(label)

	return ansi.Truncate(label, max(1, width-2), "…")
}
