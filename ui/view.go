package ui

import (
	"fmt"
	"image/color"
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

// sidebarCacheEntry is the exact set of inputs that determine the sidebar's
// styled/bordered output (see renderSidebar) — everything sidebarBox/
// sidebarInner/JoinVertical actually read. border is image/color.Color,
// which for every concrete color type this app uses (lipgloss's named/hex
// colors) is a comparable value type, so == is a real equality check here,
// not just an interface-identity check.
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

// renderSidebar renders the sidebar (accounts/status bar + chat list,
// bordered) for the given inputs, reusing the previous frame's styled
// output when every input is byte-identical to it — the box-drawing and
// join steps involve several lipgloss Style.Render calls (each of which
// does its own Unicode grapheme-cluster width scan), and those don't need
// to happen again for a frame that changed nothing about the sidebar, e.g.
// almost any mouse motion or key press while a message (not the chat list)
// is what's actually being interacted with. See Model.sidebarRenderCache's
// doc comment for why this compares by value instead of tracking mutations.
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

// viewportFrameCacheEntry is the exact set of inputs that determine the
// message viewport's styled/framed output (see renderViewportFrame) — the
// same content-addressed caching approach as sidebarCacheEntry, and for the
// same reason: m.viewport.View()'s content changes on every message-cursor
// move, mouse hover, and scroll, and tracking every one of those call sites
// to know when to invalidate would be as fragile as doing it for the chat
// list. Comparing the rendered content itself instead means the cache can
// never be stale.
type viewportFrameCacheEntry struct {
	width, height int
	content       string
	rendered      string
}

// renderViewportFrame renders the message viewport's border/padding frame
// around already-rendered content, reusing the previous frame's styled
// output when width/height/content are all byte-identical to it — e.g.
// almost any event that doesn't touch the loaded chat's content or the pane
// size at all (a mouse motion over the sidebar, an unrelated popup opening,
// scrolledPastFirstPage's jump-to-bottom button toggling — none of those
// change what's inside this frame).
func (m Model) renderViewportFrame(want viewportFrameCacheEntry) string {
	c := m.viewportFrameCache
	if c.width == want.width && c.height == want.height && c.content == want.content {
		return c.rendered
	}
	want.rendered = m.styles.viewportFrame(want.width, want.height, m.styles.viewportContent(want.width, want.height, want.content))
	*c = want
	return want.rendered
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
	case m.emojiPicker != nil:
		viewportArea = m.renderEmojiPickerPopup()
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
	case m.searchingChat:
		viewportArea = m.renderSearchChatPopup()
	case m.searchResults != nil:
		viewportArea = m.renderSearchResultsPopup()
	case m.savingAs:
		viewportArea = m.renderSaveAsPopup()
	case len(m.openItems) > 0:
		viewportArea = m.renderOpenPopup()
	case m.pickingFile:
		viewportArea = m.renderFilePickerPopup()
	default:
		viewportHeight := m.height - m.inputAreaHeight() - chatStatusHeight
		viewportArea = m.zone.Mark(zonePaneViewport, m.renderViewportFrame(viewportFrameCacheEntry{
			width:   m.chatAreaWidth(),
			height:  viewportHeight,
			content: m.viewport.View(),
		}))
		if m.currentChatIndex() >= 0 && m.scrolledPastFirstPage() {
			btn := m.zone.Mark(zoneJumpToBottom, m.styles.renderJumpToBottomButton(m.isHovered(zoneJumpToBottom)))
			viewportArea = overlayBottomCenter(viewportArea, btn, 1)
		}
	}

	// renderChatArea is only called when the chat pane is the visible one, so
	// in narrow mode the list is by definition hidden right now — the icon
	// should always offer to bring it back, regardless of sidebarHidden
	// (which narrow mode ignores; see sidebarWidth/chatAreaWidth).
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

// renderCallBar builds the persistent call status bar row: plain status text
// plus real clickable [key] label buttons (zoneCallAnswer/Reject/Mute/Hangup)
// for whichever actions apply to the current call state — mirrors the
// zoneSendButton pattern (m.zone.Mark around a styles.render* button,
// hover-highlighted via m.isHovered). "" when there's no call to show,
// matching callBarActive so View() only reserves space for it when needed.
func (m Model) renderCallBar(width int) string {
	if m.call == nil {
		if m.videoDialPrompt {
			text := "📹 start video call from:·"
			cameraBtn := m.zone.Mark(zoneCallVideoCamera, m.styles.renderCallBarButton("[ctrl+c] camera", m.isHovered(zoneCallVideoCamera)))
			screenBtn := m.zone.Mark(zoneCallVideoScreen, m.styles.renderCallBarButton("[ctrl+s] screen", m.isHovered(zoneCallVideoScreen)))
			return m.styles.callBarLine(width, text+cameraBtn+m.styles.callBarText("·")+screenBtn+m.styles.callBarText("·[esc] cancel"))
		}
		return ""
	}
	switch m.call.state {
	case "ringing-local":
		text := "📞 incoming call: " + m.call.peer + "·"
		answer := m.zone.Mark(zoneCallAnswer, m.styles.renderCallBarButton("[ctrl+y] answer", m.isHovered(zoneCallAnswer)))
		reject := m.zone.Mark(zoneCallReject, m.styles.renderCallBarButton("[ctrl+n] reject", m.isHovered(zoneCallReject)))
		return m.styles.callBarLine(width, text+answer+m.styles.callBarText("·")+reject)

	case "proposing", "ringing-remote", "negotiating":
		verb := "calling"
		if m.call.state == "negotiating" {
			verb = "connecting to"
		}
		text := "📞 " + verb + " " + m.call.peer + "...·"
		if m.call.fingerprintChanged {
			text += "⚠ peer's call key changed since last time!·"
		}
		if m.call.sas != "" {
			text += "🔑 " + m.call.sas + "·"
		}
		hangup := m.zone.Mark(zoneCallHangup, m.styles.renderCallBarButton("[ctrl+h] hang up", m.isHovered(zoneCallHangup)))
		return m.styles.callBarLine(width, text+hangup)

	case "connected":
		dur := "00:00"
		if !m.call.startedAt.IsZero() {
			dur = formatCallDuration(time.Since(m.call.startedAt))
		}
		mic := "🎤 unmuted"
		muteLabel := "[ctrl+m] mute"
		if m.call.muted {
			mic = "🎤 muted"
			muteLabel = "[ctrl+m] unmute"
		}
		quality := "📶 …"
		if m.call.quality != "" {
			quality = "📶 " + m.call.quality
		}
		text := "📞 " + m.call.peer + "·" + dur + "·" + mic + "·" + quality + "·"
		if m.call.fingerprintChanged {
			text += "⚠ peer's call key changed since last time!·"
		}
		if m.call.sas != "" {
			text += "🔑 " + m.call.sas + "·"
		}
		if m.videoSourcePrompt {
			text += "start video from:·"
			cameraBtn := m.zone.Mark(zoneCallVideoCamera, m.styles.renderCallBarButton("[ctrl+c] camera", m.isHovered(zoneCallVideoCamera)))
			screenBtn := m.zone.Mark(zoneCallVideoScreen, m.styles.renderCallBarButton("[ctrl+s] screen", m.isHovered(zoneCallVideoScreen)))
			return m.styles.callBarLine(width, text+cameraBtn+m.styles.callBarText("·")+screenBtn+m.styles.callBarText("·[esc] cancel"))
		}
		if m.call.sharing {
			text += "🖥 sharing·"
		}
		dot := m.styles.callBarText("·")
		muteBtn := m.zone.Mark(zoneCallMute, m.styles.renderCallBarButton(muteLabel, m.isHovered(zoneCallMute)))
		hangupBtn := m.zone.Mark(zoneCallHangup, m.styles.renderCallBarButton("[ctrl+h] hang up", m.isHovered(zoneCallHangup)))
		reopenBtn := m.zone.Mark(zoneCallReopenVideo, m.styles.renderCallBarButton("[ctrl+r] reopen video", m.isHovered(zoneCallReopenVideo)))
		buttons := muteBtn + dot + reopenBtn + dot + hangupBtn
		if !m.call.sharing {
			videoBtn := m.zone.Mark(zoneCallVideo, m.styles.renderCallBarButton("[ctrl+v] video", m.isHovered(zoneCallVideo)))
			buttons += dot + videoBtn
		}
		return m.styles.callBarLine(width, text+buttons)

	case "ended", "failed":
		// Terminal state: nothing left to click, just the plain self-clearing
		// text (callClearMsg drops m.call shortly after — see call.go).
		return m.styles.callBarLine(width, m.callBarLine())

	default:
		return ""
	}
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

// renderDeviceHoverPopup builds the floating "online devices" box for the
// currently hovered chat row, once hoverDevicesDelay has revealed it (see
// hoverDevicesTimer/hoverState.devicesID) — a real overlay composited on
// top of the fully rendered frame (via overlayAt in View()), not squeezed
// into the row's own two-line height, so it isn't clipped by the sidebar
// column width or cut off by neighboring rows. ok is false whenever there's
// nothing to show: no row is in its post-delay hover state, or that
// contact has no online resources.
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

	// Deliberately no Background/Foreground on this box style: every
	// fragment above (deviceGlyph, plain resource/label text) is already
	// fully rendered with its own color and no background set, so
	// wrapping it in a style that added a background here would just
	// leave the border/padding cells filled while the already-rendered
	// text cells underneath keep showing through the terminal's own
	// background — the two-tone/transparent-patchwork bug. Leaving the
	// box's own style bare keeps the whole popup uniformly on the
	// terminal's background.
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
	baseLines := strings.Split(base, "\n")
	contentLines := strings.Split(content, "\n")
	contentWidth := lipgloss.Width(content)

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

// wrapFooterHint word-wraps a helpHint string (entries joined by "·", each
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
	case confirmDisableStorageEncryption:
		return m.styles.deletePrompt(width, "Disable local storage encryption?",
			"Every stored message and draft will be rewritten to disk in plain text.")
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
	chat, _ := m.currentChat()
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
		rows = append(rows, "  Size: "+m.attachmentSizeLabel(a, chat.Address))
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
	footer := "[1-9] " + verb + " · [esc] cancel"
	if pages := openPageCount(len(m.openItems)); pages > 1 {
		title = fmt.Sprintf("%s (page %d/%d)", title, m.openPage+1, pages)
		footer = "[1-9] " + verb + " · [←/→] page · [esc] cancel"
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
	back := m.renderFilePickerNavButton("‹", zoneFilePickerBack, m.filePicker.CanGoBack())
	forward := m.renderFilePickerNavButton("›", zoneFilePickerForward, m.filePicker.CanGoForward())
	title := back + " " + forward + "  Attach file — " + m.filePicker.CurrentDirectory
	footer := "[enter] open/select · [" + sortKey + "] sort: " + m.filePicker.SortLabel() + " · [esc] cancel"

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
	popup = m.zone.Mark(zoneFilePickerPopup, popup)
	return lipgloss.Place(cw, vh, lipgloss.Center, lipgloss.Center, popup)
}

// renderFilePickerNavButton renders a back/forward glyph for the file
// picker's title bar, zone-marked for a click only while enabled — matching
// the picker's own CanGoBack/CanGoForward availability, so a click can never
// fire a no-op navigation.
func (m Model) renderFilePickerNavButton(glyph, zoneID string, enabled bool) string {
	style := lipgloss.NewStyle().Foreground(m.styles.colors.time)
	if enabled {
		style = lipgloss.NewStyle().Foreground(m.styles.colors.themFg).Bold(true)
	}
	rendered := style.Render(glyph)
	if !enabled {
		return rendered
	}
	return m.zone.Mark(zoneID, rendered)
}

// renderAddAccountPopup shows the add-account form: one line per active
// field (JID, password, confirm password when registering, GPG key ID), the
// focused field highlighted by its own textinput cursor, plus an error line
// if the last attempt failed.
func (m Model) renderAddAccountPopup() string {
	cw := m.chatAreaWidth()
	vh := m.height - m.inputAreaHeight()

	title := "Log in"
	verb := "log in"
	if m.addAccountRegister {
		title = "Register"
		verb = "register"
	}

	count := m.addAccountFieldCount()
	rows := make([]string, count)
	for pos := 0; pos < count; pos++ {
		rows[pos] = m.addAccountInputs[m.addAccountFieldIndex(pos)].View()
	}
	if m.addAccountBusy {
		rows = append(rows, "", verb+"...")
	} else if m.addAccountErr != "" {
		rows = append(rows, "", m.styles.popupDanger.Render(m.addAccountErr))
	}

	footer := fmt.Sprintf("[tab] next field · [ctrl+r] switch to %s · [enter] %s · [esc] cancel", altMode(m.addAccountRegister), verb)
	body := m.styles.listPopup(title, rows, footer)
	popup := m.styles.popupDialog(m.styles.colors.borderA, body)

	return lipgloss.Place(cw, vh, lipgloss.Center, lipgloss.Center, popup)
}

// altMode returns the label for the mode ctrl+r switches to, opposite the
// current one.
func altMode(register bool) string {
	if register {
		return "log in"
	}
	return "register"
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
		"Leave both fields blank to turn local storage encryption off.",
		"",
		s.inputs[0].View(),
		s.inputs[1].View(),
	}
	if s.busy {
		if s.inputs[0].Value() == "" {
			rows = append(rows, "", "disabling local storage encryption...")
		} else {
			rows = append(rows, "", "re-encrypting local storage...")
		}
	} else if s.err != "" {
		rows = append(rows, "", m.styles.popupDanger.Render(s.err))
	}

	footer := "[tab] next field · [enter] change · [esc] cancel"
	body := m.styles.listPopup("Change storage password", rows, footer)
	popup := m.styles.popupDialog(m.styles.colors.borderA, body)

	return lipgloss.Place(cw, vh, lipgloss.Center, lipgloss.Center, popup)
}

// renderRenameChatPopup shows the single-field rename-contact prompt.
func (m Model) renderRenameChatPopup() string {
	cw := m.chatAreaWidth()
	vh := m.height - m.inputAreaHeight()

	footer := "[enter] save · [esc] cancel"
	body := m.styles.listPopup("Rename chat", []string{m.renameInput.View()}, footer)
	popup := m.styles.popupDialog(m.styles.colors.borderA, body)

	return lipgloss.Place(cw, vh, lipgloss.Center, lipgloss.Center, popup)
}

// renderSaveAsPopup shows the single-field save-as destination-path prompt.
func (m Model) renderSaveAsPopup() string {
	cw := m.chatAreaWidth()
	vh := m.height - m.inputAreaHeight()

	footer := "[enter] save · [esc] cancel"
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

// inputHint renders the optional reply-quote line shown above the input box.
// Empty if not currently replying. Reacting no longer has a compose-area
// hint of its own — it's a popup now (see renderEmojiPickerPopup).
func (m Model) inputHint() string {
	if m.replyToIdx >= 0 {
		msgs := m.currentMessages()
		if m.replyToIdx < len(msgs) {
			orig := msgs[m.replyToIdx]
			hint := m.styles.renderReplyHint(orig.Author, previewText(MessagePreviewContent(orig), previewLen))
			// Clickable to cancel the pending reply (see zoneReplyHintCancel in
			// mouse.go).
			return m.zone.Mark(zoneReplyHintCancel, hint)
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
		label += " · typing..."
	}
	if m.currentAccount >= 0 && m.currentAccount < len(m.accounts) && m.accounts[m.currentAccount].SyncingHistory {
		label += " · syncing history..."
	}
	// nickMe-color the text ourselves rather than let chatStatusLine wrap
	// the whole string in one Foreground — presenceGlyph's own Render call
	// ends in a full ANSI reset, which would otherwise cut the outer color
	// off right after the dot, leaving the rest of the label uncolored.
	label = m.styles.messageNickMe.Render(label)

	return ansi.Truncate(label, max(1, width-2), "…")
}
