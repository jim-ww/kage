package ui

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

const (
	sidebarStatusHeight = 2
	chatStatusHeight    = 1
	// callBarHeight is the persistent call bar's row count — only reserved
	// in updateSizes while callBarActive() (see layout.go), so idle layout
	// is unaffected.
	callBarHeight = 1
	// footerMaxLines keeps the key hints to one row, so the layout doesn't
	// shift; help lists them all.
	footerMaxLines = 1
	// footerMarginTop is a blank row separating the main view from the
	// footer, so the key hints don't read as glued to the input box.
	footerMarginTop = 1
)

type uiStyles struct {
	colors                uiColors
	messageTime           lipgloss.Style
	messageReply          lipgloss.Style
	messageDeleted        lipgloss.Style
	messageFlash          lipgloss.Style
	messageSelectedPrefix lipgloss.Style
	messageHoverPrefix    lipgloss.Style
	messageNickMe         lipgloss.Style
	messageNickThem       lipgloss.Style
	messageBar            lipgloss.Style
	messageMuted          lipgloss.Style
	sidebarStatus         lipgloss.Style
	sidebarPanel          lipgloss.Style
	sidebarList           lipgloss.Style
	sidebar               lipgloss.Style
	inputInner            lipgloss.Style
	inputBox              lipgloss.Style
	viewportBody          lipgloss.Style
	plainText             lipgloss.Style
	notice                lipgloss.Style
	viewportArea          lipgloss.Style
	root                  lipgloss.Style
	popup                 lipgloss.Style
	popupDanger           lipgloss.Style
	footer                lipgloss.Style
	accountNormal         lipgloss.Style
	accountSelected       lipgloss.Style
	accountHover          lipgloss.Style
	sendButton            lipgloss.Style
	attachButton          lipgloss.Style
	dimButton             lipgloss.Style
	contextMenuItem       lipgloss.Style
	contextMenuItemHover  lipgloss.Style
	callBar               lipgloss.Style
}

func newUIStyles(theme Theme) *uiStyles {
	colors := newUIColors(theme)
	return &uiStyles{
		colors: colors,
		messageTime: lipgloss.NewStyle().
			Foreground(colors.time),
		messageReply: lipgloss.NewStyle().
			Foreground(colors.replyFg).
			Italic(true),
		messageDeleted: lipgloss.NewStyle().
			Foreground(colors.textMuted).
			Faint(true).
			Italic(true),
		messageFlash: lipgloss.NewStyle().
			Background(colors.noticeBg).
			Foreground(colors.noticeFg).
			Bold(true),
		messageSelectedPrefix: lipgloss.NewStyle().
			Foreground(colors.borderA),
		messageHoverPrefix: lipgloss.NewStyle().
			Foreground(colors.textMuted),
		messageNickMe: lipgloss.NewStyle().
			Foreground(colors.nickMe),
		messageNickThem: lipgloss.NewStyle().
			Foreground(colors.nickThem),
		// messageBar draws the selected-message left-edge indicator - red,
		// reusing the same semantic color as popupDanger/DND rather than
		// adding a new theme field just for this one glyph.
		messageBar: lipgloss.NewStyle().
			Foreground(colors.popupDanger),
		messageMuted: lipgloss.NewStyle().
			Foreground(colors.textMuted),
		sidebarStatus: lipgloss.NewStyle().
			Bold(true).
			Padding(0, 1),
		sidebarPanel: lipgloss.NewStyle().
			Foreground(colors.themFg),
		sidebarList: lipgloss.NewStyle().
			Foreground(colors.themFg),
		sidebar: lipgloss.NewStyle().
			Foreground(colors.themFg).
			Border(lipgloss.NormalBorder(), false, true, false, false),
		inputInner: lipgloss.NewStyle().
			Foreground(colors.themFg),
		inputBox: lipgloss.NewStyle().
			Foreground(colors.themFg).
			Border(lipgloss.NormalBorder(), true, false, false, false).
			Padding(0, 1),
		// No Foreground: around styled text, it'd color what follows each
		// embedded reset.
		viewportBody: lipgloss.NewStyle(),
		plainText: lipgloss.NewStyle().
			Foreground(colors.themFg),
		notice: lipgloss.NewStyle().
			Foreground(colors.noticeFg).
			Background(colors.noticeBg).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colors.accentCyan).
			Padding(0, 1).
			Bold(true),
		viewportArea: lipgloss.NewStyle().
			Foreground(colors.themFg),
		root: lipgloss.NewStyle().
			Foreground(colors.themFg),
		popup: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Foreground(colors.themFg).
			Padding(1, 4),
		popupDanger: lipgloss.NewStyle().
			Foreground(colors.popupDanger).
			Bold(true),
		footer: lipgloss.NewStyle().
			Foreground(colors.time).
			Padding(0, 1),
		// callBar mirrors footer's plain look — it sits in the same kind of
		// fixed layout row, just with an accent background so an in-progress
		// call reads as a persistent status, not another hint line.
		callBar: onAccent(colors.accentCyan).
			Padding(0, 1).
			Bold(true),
		accountNormal: lipgloss.NewStyle().
			Foreground(colors.themFg).
			PaddingLeft(1),
		accountSelected: lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderForeground(colors.borderA).
			Foreground(colors.themFg).
			Bold(true).
			PaddingLeft(1),
		accountHover: lipgloss.NewStyle().
			Foreground(colors.themFg).
			Underline(true).
			PaddingLeft(1),
		sendButton: onAccent(colors.accentCyan).
			Bold(true).
			Padding(0, 1),
		// attachButton is deliberately calmer than sendButton (send is the
		// primary action) and un-bold/less padded so it doesn't visually
		// compete with it.
		attachButton: lipgloss.NewStyle().
			Foreground(colors.themFg).
			Background(colors.buttonBg).
			Padding(0, 1),
		// dimButton is small in-row controls at rest.
		dimButton: lipgloss.NewStyle().
			Foreground(colors.textMuted).
			Background(colors.dimButtonBg).
			Padding(0, 1),
		contextMenuItem: lipgloss.NewStyle().
			Foreground(colors.themFg).
			PaddingLeft(1),
		contextMenuItemHover: onAccent(colors.accentCyan).
			Bold(true).
			PaddingLeft(1),
	}
}

// accountBarNameRow and accountBarStatusRow render the account bar's rows,
// separately so each is its own zone and color.
func (s *uiStyles) accountBarNameRow(width int, st lipgloss.Style, name string) string {
	return s.sidebarStatus.
		Inherit(st).
		Width(width).
		Render(name)
}

// onAccent is text on a block of accent: the accent reversed, so the text is
// in the terminal's background color, readable with any theme.
func onAccent(accent color.Color) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(accent).Reverse(true)
}

// accountBarStatusRow renders just the account bar's status row, tinting it
// on hover.
func (s *uiStyles) accountBarStatusRow(width int, status string, hovered bool) string {
	fg := s.colors.textMuted
	if hovered {
		fg = s.colors.accentCyan
	}
	return s.sidebarStatus.
		Width(width).
		Align(lipgloss.Right).
		Foreground(fg).
		Render(status)
}

// chatStatusLine renders the chat pane's status bar (peer name/presence,
// typing indicator) centered; the content is expected to already be colored
// (see renderChatStatusBar) to match our own messages' "» [time]" glyph.
func (s *uiStyles) chatStatusLine(width int, content string) string {
	return s.sidebarStatus.
		Width(width).
		Align(lipgloss.Center).
		Render(content)
}

func (s *uiStyles) sidebarInner(width, height int, content string) string {
	return s.sidebarPanel.
		Width(width).
		Height(height).
		Render(s.sidebarList.Width(width).Render(content))
}

func (s *uiStyles) sidebarBox(width, height int, border color.Color, content string) string {
	return s.sidebar.
		Width(width).
		Height(height).
		BorderForeground(border).
		Render(content)
}

func (s *uiStyles) inputInnerBox(width int, content string) string {
	return s.inputInner.Width(width).Render(content)
}

func (s *uiStyles) inputContainer(border color.Color, content string) string {
	return s.inputBox.BorderForeground(border).Render(content)
}

// sendButtonLabel is the send button's rendered text (including its
// padding), used both to draw the button and to size the input field
// around it — see sendButtonWidth.
const sendButtonLabel = " Send "

// sendButtonWidth is lipgloss.Width(rendered send button): the label plus
// its Padding(0, 1).
const sendButtonWidth = len(sendButtonLabel) + 2

func (s *uiStyles) renderSendButton(hovered bool) string {
	st := s.sendButton
	if hovered {
		st = st.Reverse(true)
	}
	return st.Render(sendButtonLabel)
}

// attachButtonLabel is the attach button's text: a paperclip, or plain text
// without icons.
func attachButtonLabel(icons bool) string {
	if icons {
		return emojiFileIconDefault
	}
	return "Attach"
}

// attachButtonWidth is lipgloss.Width(rendered attach button): the label
// plus its Padding(0, 1).
func attachButtonWidth(icons bool) int {
	return lipgloss.Width(attachButtonLabel(icons)) + 2
}

// renderStoragePasswordButton renders the storage password button of the
// open accounts panel.
func (s *uiStyles) renderStoragePasswordButton(icons, hovered bool) string {
	label := "[k]"
	if icons {
		label = "🔑"
	}
	st := lipgloss.NewStyle().
		Foreground(s.colors.themFg).
		Background(s.colors.buttonBg).
		Padding(0, 1)
	if hovered {
		st = st.Reverse(true)
	}
	return st.Render(label)
}

// renderMsgActionKey renders a message's reply or react glyph, dim, cyan
// when hovered.
func (s *uiStyles) renderMsgActionKey(key string, hovered bool) string {
	if !hovered {
		return s.messageMuted.Render(key)
	}
	return s.messageMuted.Foreground(s.colors.accentCyan).Bold(true).Render(key)
}

// renderMsgExpandButton is a long message's show more/less button, with
// keyLabel - the key doing the same for the selected message - after it.
func (s *uiStyles) renderMsgExpandButton(expanded, hovered bool, keyLabel string) string {
	label := "▾ show more"
	if expanded {
		label = "▴ show less"
	}
	if keyLabel != "" {
		label += " " + keyLabel
	}
	st := s.dimButton
	if hovered {
		st = st.Reverse(true)
	}
	return st.Render(label)
}

func (s *uiStyles) renderAttachButton(icons, hovered bool) string {
	st := s.attachButton
	if hovered {
		st = st.Reverse(true)
	}
	return st.Render(attachButtonLabel(icons))
}

// renderAttachmentChip renders a staged attachment: icon, name and "x";
// bracketed if selected.
func (s *uiStyles) renderAttachmentChip(icon, name string, selected, hovered bool) string {
	label := icon + " " + name + " ×"
	if selected {
		label = "[" + label + "]"
	}
	st := s.messageReply
	if hovered {
		st = st.Reverse(true)
	}
	return st.Render(label)
}

// renderSidebarToggleButton renders the chat list toggle.
func (s *uiStyles) renderSidebarToggleButton(hidden, hovered bool) string {
	icon := " ◧ "
	if hidden {
		icon = " ◨ "
	}
	st := s.sendButton
	if hovered {
		st = st.Reverse(true)
	}
	return st.Render(icon)
}

// renderJumpToBottomButton renders the "jump to latest" button over the
// compose box, faint until hovered, with keyLabel, its key.
func (s *uiStyles) renderJumpToBottomButton(hovered bool, keyLabel string) string {
	st := lipgloss.NewStyle().
		Foreground(s.colors.themFg).
		Background(s.colors.buttonBg).
		Padding(0, 1).
		Faint(true)
	if hovered {
		st = st.Faint(false).Reverse(true)
	}
	label := "↓ jump to latest"
	if keyLabel != "" {
		label += " " + keyLabel
	}
	return st.Render(label)
}

// renderAccountMenuButton draws the account bar's menu button, in the bar's
// colors.
func (s *uiStyles) renderAccountMenuButton(icons bool, row lipgloss.Style, hovered bool) string {
	label := " ⋯ "
	if !icons {
		label = " ... "
	}
	st := row
	if hovered {
		st = st.Foreground(s.colors.accentCyan).Bold(true)
	}
	return st.Render(label)
}

// plainTextLine colors line with the body foreground, unless it's styled
// already: an outer color would stop at its resets.
func (s *uiStyles) plainTextLine(line string) string {
	if strings.Contains(line, "\x1b[") {
		return line
	}
	return s.plainText.Render(line)
}

// dateDividerDashes is the fixed dash run on each side of a date-divider
// label - short and left-aligned rather than centered/stretched to the full
// row width, so it reads as a compact marker rather than a wide rule.
const dateDividerDashes = 3

// renderDateDivider renders a short "─── Tue 26 Aug ───" separator line
// marking the start of a new calendar day in the message list, left-aligned
// at the start of the row rather than centered or stretched to width.
func (s *uiStyles) renderDateDivider(label string) string {
	dashes := strings.Repeat("─", dateDividerDashes)
	line := dashes + " " + label + " " + dashes
	return s.messageTime.Render(line)
}

// rowBackgroundTint tints a rendered row's background, reapplied after
// every reset so it covers the whole row.
func (s *uiStyles) rowBackgroundTint(line string) string {
	bgCode, _, ok := strings.Cut(lipgloss.NewStyle().Background(s.colors.rowHoverBg).Render("\x00"), "\x00")
	if !ok {
		return line
	}
	return bgCode + strings.ReplaceAll(line, "\x1b[m", "\x1b[m"+bgCode) + "\x1b[m"
}

func (s *uiStyles) viewportContent(width, height int, content string) string {
	return s.viewportBody.
		Width(width).
		Height(height).
		Render(content)
}

// noticeToast renders the notification text as a self-sized bordered toast,
// wrapped to maxWidth so a long message doesn't stretch off-screen.
func (s *uiStyles) noticeToast(maxWidth int, content string) string {
	style := s.notice
	// -4 for border and padding; a width only when the text must wrap.
	if textWidth := max(1, maxWidth-4); lipgloss.Width(content) > textWidth {
		style = style.Width(textWidth)
	}
	return style.Render(content)
}

func (s *uiStyles) viewportFrame(width, height int, content string) string {
	return s.viewportArea.
		Width(width).
		Height(height).
		Render(content)
}

// viewportFramePresized is viewportFrame for content of the frame's size,
// sparing lipgloss re-scanning it (see renderViewportFrame).
func (s *uiStyles) viewportFramePresized(content string) string {
	return s.viewportArea.Render(content)
}

func (s *uiStyles) rootView(content string) string {
	return s.root.Render(content)
}

func (s *uiStyles) footerBar(width int, content string) string {
	return s.footer.Width(width).Render(content)
}

func (s *uiStyles) callBarLine(width int, content string) string {
	return s.callBar.Width(width).Render(content)
}

// callBarText renders plain call bar text on its background, for text
// between buttons: after a button's reset it would have none.
func (s *uiStyles) callBarText(text string) string {
	return onAccent(s.colors.accentCyan).
		Bold(true).
		Render(text)
}

// renderCallBarButton renders one of the call bar's [key] label buttons —
// same small hover-reversed pattern as renderStoragePasswordButton, just
// against callBar's background instead of the account bar's.
func (s *uiStyles) renderCallBarButton(label string, hovered bool) string {
	st := onAccent(s.colors.accentCyan).
		Bold(true)
	if hovered {
		st = st.Reverse(false)
	}
	return st.Render(label)
}

func (s *uiStyles) renderAccountRow(name string, selected, hovered bool) string {
	style := s.accountNormal
	if selected {
		style = s.accountSelected
	}
	if hovered {
		// Chained onto whichever base style already applies (including the
		// current account's), so hovering it still shows some feedback
		// instead of looking unresponsive just because it's already active.
		style = style.Underline(true)
	}
	return style.Render(name)
}

func (s *uiStyles) renderMessagePrefix(selected, hovered bool) string {
	switch {
	case selected:
		return s.messageSelectedPrefix.Render("> ")
	case hovered:
		return s.messageHoverPrefix.Render("> ")
	default:
		return "  "
	}
}

// renderMessageHeader renders the direction glyph, sender and time, colored
// by sender; bold when selected, underlined when hovered.
func (s *uiStyles) renderMessageHeader(name, timeLabel string, isMe, selected, hovered bool) string {
	timeStyle := s.messageNickThem
	glyph := "«"
	if isMe {
		timeStyle = s.messageNickMe
		glyph = "»"
	}
	switch {
	case selected:
		timeStyle = timeStyle.Bold(true)
	case hovered:
		timeStyle = timeStyle.Underline(true)
	}
	return timeStyle.Render(glyph+" "+name+"["+timeLabel+"]") + " "
}

func (s *uiStyles) renderReplyHint(author, preview string) string {
	return s.messageReply.Render(fmt.Sprintf("↩ %s: %s", author, preview))
}

// renderQuoteReplyFragment renders "↑ preview" for a "> " text quote (see
// parseQuoteReply), in budget cells.
func (s *uiStyles) renderQuoteReplyFragment(preview string, budget int) string {
	return s.messageTime.Render("↑ " + previewText(preview, min(previewLen, budget-2)))
}
