package ui

import (
	"fmt"
	"image/color"

	"charm.land/lipgloss/v2"
)

const (
	sidebarStatusHeight = 1
	chatStatusHeight    = 1
	// footerMaxLines caps how tall the key-hint footer can grow when word-
	// wrapping a view's full hint list — even a wide terminal can't fit
	// viewChat's dozen-odd bindings on one line, so it wraps instead of
	// truncating, but only up to this many rows before the overflow is
	// dropped with an ellipsis.
	footerMaxLines = 2
	// footerMarginTop is a blank row separating the main view from the
	// footer, so the key hints don't read as glued to the input box.
	footerMarginTop = 1
)

type uiStyles struct {
	colors                uiColors
	messageTime           lipgloss.Style
	messageReply          lipgloss.Style
	messageSelectedPrefix lipgloss.Style
	messageHoverPrefix    lipgloss.Style
	messageNickMe         lipgloss.Style
	messageNickThem       lipgloss.Style
	sidebarStatus         lipgloss.Style
	sidebarPanel          lipgloss.Style
	sidebarList           lipgloss.Style
	sidebar               lipgloss.Style
	inputInner            lipgloss.Style
	inputBox              lipgloss.Style
	viewportBody          lipgloss.Style
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
	contextMenuItem       lipgloss.Style
	contextMenuItemHover  lipgloss.Style
}

func newUIStyles(theme Theme) uiStyles {
	colors := newUIColors(theme)
	return uiStyles{
		colors: colors,
		messageTime: lipgloss.NewStyle().
			Foreground(colors.time),
		messageReply: lipgloss.NewStyle().
			Foreground(colors.replyFg).
			Italic(true),
		messageSelectedPrefix: lipgloss.NewStyle().
			Foreground(colors.borderA),
		messageHoverPrefix: lipgloss.NewStyle().
			Foreground(colors.textMuted),
		messageNickMe: lipgloss.NewStyle().
			Foreground(colors.nickMe),
		messageNickThem: lipgloss.NewStyle().
			Foreground(colors.nickThem),
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
		viewportBody: lipgloss.NewStyle().
			Foreground(colors.themFg),
		notice: lipgloss.NewStyle().
			Foreground(colors.noticeFg).
			Padding(0, 1),
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
		sendButton: lipgloss.NewStyle().
			Foreground(colors.panelEdge).
			Background(colors.accentCyan).
			Bold(true).
			Padding(0, 1),
		contextMenuItem: lipgloss.NewStyle().
			Foreground(colors.themFg).
			PaddingLeft(1),
		contextMenuItemHover: lipgloss.NewStyle().
			Foreground(colors.panelEdge).
			Background(colors.accentCyan).
			Bold(true).
			PaddingLeft(1),
	}
}

func (s uiStyles) sidebarStatusLine(width int, bg, fg color.Color, content string) string {
	return s.sidebarStatus.
		Width(width).
		// Background(bg).
		Foreground(fg).
		Render(content)
}

// chatStatusLine renders the chat pane's status bar (peer name/presence,
// typing indicator) centered; the content is expected to already be colored
// (see renderChatStatusBar) to match our own messages' "» [time]" glyph.
func (s uiStyles) chatStatusLine(width int, content string) string {
	return s.sidebarStatus.
		Width(width).
		Align(lipgloss.Center).
		Render(content)
}

func (s uiStyles) sidebarInner(width, height int, content string) string {
	return s.sidebarPanel.
		Width(width).
		Height(height).
		Render(s.sidebarList.Width(width).Render(content))
}

func (s uiStyles) sidebarBox(width, height int, border color.Color, content string) string {
	return s.sidebar.
		Width(width).
		Height(height).
		BorderForeground(border).
		Render(content)
}

func (s uiStyles) inputInnerBox(width int, content string) string {
	return s.inputInner.Width(width).Render(content)
}

func (s uiStyles) inputContainer(border color.Color, content string) string {
	return s.inputBox.BorderForeground(border).Render(content)
}

// sendButtonLabel is the send button's rendered text (including its
// padding), used both to draw the button and to size the input field
// around it — see sendButtonWidth.
const sendButtonLabel = " Send "

// sendButtonWidth is lipgloss.Width(rendered send button): the label plus
// its Padding(0, 1).
const sendButtonWidth = len(sendButtonLabel) + 2

func (s uiStyles) renderSendButton(hovered bool) string {
	st := s.sendButton
	if hovered {
		st = st.Reverse(true)
	}
	return st.Render(sendButtonLabel)
}

// renderSidebarToggleButton renders the chat-status-bar icon button that
// shows/hides the chat list sidebar (mirrors DefaultKeyMap.ToggleSidebar,
// bound to Ctrl+\). Shows a closed-side icon when the list is hidden (click
// to open it) and an open-side icon when visible (click to close it).
func (s uiStyles) renderSidebarToggleButton(hidden, hovered bool) string {
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

// contextMenuRow renders one action label, padded/highlighted to width so
// every row is a consistent, easy-to-hit target — narrow rows packed
// tightly together (the original complaint) invite misclicks.
func (s uiStyles) contextMenuRow(label string, hovered bool, width int) string {
	st := s.contextMenuItem
	if hovered {
		st = s.contextMenuItemHover
	}
	return st.Width(width).Render(label)
}

func (s uiStyles) viewportContent(width, height int, content string) string {
	return s.viewportBody.
		Width(width).
		Height(height).
		Render(content)
}

func (s uiStyles) noticeBar(width int, content string) string {
	return s.notice.Width(width).Render(content)
}

func (s uiStyles) viewportFrame(width, height int, content string) string {
	return s.viewportArea.
		Width(width).
		Height(height).
		Render(content)
}

func (s uiStyles) rootView(content string) string {
	return s.root.Render(content)
}

func (s uiStyles) popupDialog(border color.Color, content string) string {
	return s.popup.BorderForeground(border).Render(content)
}

func (s uiStyles) footerBar(width int, content string) string {
	return s.footer.Width(width).Render(content)
}

func (s uiStyles) renderAccountRow(name string, selected, hovered bool) string {
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

func (s uiStyles) deletePrompt(title, detail string) string {
	body := s.popupDanger.Render(title)
	if detail != "" {
		body += "\n" + s.messageReply.Render(detail)
	}
	return body + "\n\n  [y] yes    [n] no"
}

func (s uiStyles) infoPopup(title string, rows []string, closeKey string) string {
	return s.listPopup(title, rows, "[esc/"+closeKey+"] close")
}

func (s uiStyles) listPopup(title string, rows []string, footer string) string {
	body := lipgloss.NewStyle().Bold(true).Foreground(s.colors.borderA).Render(title)
	for _, row := range rows {
		body += "\n" + row
	}
	body += "\n\n  " + footer
	return body
}

func (s uiStyles) renderMessagePrefix(selected, hovered bool) string {
	switch {
	case selected:
		return s.messageSelectedPrefix.Render("> ")
	case hovered:
		return s.messageHoverPrefix.Render("> ")
	default:
		return "  "
	}
}

// renderMessageHeader renders a directional glyph (« them, » me) plus the
// timestamp, both color-coded by sender — chats are always 1:1 here, so the
// nick would be redundant on every line.
func (s uiStyles) renderMessageHeader(timeLabel string, isMe bool) string {
	timeStyle := s.messageNickThem
	glyph := "«"
	if isMe {
		timeStyle = s.messageNickMe
		glyph = "»"
	}
	return timeStyle.Render(glyph+" ["+timeLabel+"]") + " "
}

func (s uiStyles) renderReplyHint(author, preview string) string {
	return s.messageReply.Render(fmt.Sprintf("↩ %s: %s", author, preview))
}

// emojiSuggestionLabel styles one react-hint suggestion label: bracketed
// (like the selected-message prefix) when it's the arrow-key selection,
// dimmer-bracketed on hover, plain otherwise. Applied once to the plain
// label text — not layered on top of another already-rendered style — so
// it can't hit the corruption double-render risk described on
// zoneChatListDelegate.Render.
func (s uiStyles) emojiSuggestionLabel(label string, selected, hovered bool) string {
	switch {
	case selected:
		return s.messageSelectedPrefix.Render("[" + label + "]")
	case hovered:
		return s.messageHoverPrefix.Render("[" + label + "]")
	default:
		return label
	}
}
