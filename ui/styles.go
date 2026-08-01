package ui

import (
	"fmt"
	"image/color"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
)

const (
	sidebarStatusHeight = 1
	chatStatusHeight    = 1
)

type uiColors struct {
	// appBg       color.Color
	// panelBg     color.Color
	// panelAltBg  color.Color
	panelEdge color.Color
	// logBg       color.Color
	themFg     color.Color
	textMuted  color.Color
	time       color.Color
	borderD    color.Color
	borderA    color.Color
	accentCyan color.Color
	replyFg    color.Color
	// popupBg     color.Color
	popupDanger color.Color
	filterMatch color.Color
	nickMe      color.Color
	nickThem    color.Color
	statusFg    color.Color
	// noticeBg    color.Color
	noticeFg color.Color
}

func newUIColors(theme Theme) uiColors {
	return uiColors{
		// appBg:      lipgloss.Color(theme.AppBg),
		// panelBg:    lipgloss.Color(theme.PanelBg),
		// panelAltBg: lipgloss.Color(theme.PanelAltBg),
		panelEdge: lipgloss.Color(theme.PanelEdge),
		// logBg:      lipgloss.Color(theme.LogBg),
		themFg:     lipgloss.Color(theme.ThemFg),
		textMuted:  lipgloss.Color(theme.TextMuted),
		time:       lipgloss.Color(theme.Time),
		borderD:    lipgloss.Color(theme.BorderD),
		borderA:    lipgloss.Color(theme.BorderA),
		accentCyan: lipgloss.Color(theme.AccentCyan),
		replyFg:    lipgloss.Color(theme.ReplyFg),
		// popupBg:     lipgloss.Color(theme.PopupBg),
		popupDanger: lipgloss.Color(theme.PopupDanger),
		filterMatch: lipgloss.Color(theme.FilterMatch),
		nickMe:      lipgloss.Color(theme.NickMe),
		nickThem:    lipgloss.Color(theme.NickThem),
		statusFg:    lipgloss.Color(theme.StatusFg),
		// noticeBg:    lipgloss.Color(theme.NoticeBg),
		noticeFg: lipgloss.Color(theme.NoticeFg),
	}
}

type uiStyles struct {
	colors                uiColors
	messageTime           lipgloss.Style
	messageReply          lipgloss.Style
	messageSelectedPrefix lipgloss.Style
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
	}
}

func newChatListDelegate(colors uiColors) list.DefaultDelegate {
	delegate := list.NewDefaultDelegate()
	delegate.SetSpacing(0)
	delegate.SetHeight(2)
	delegate.Styles.NormalTitle = delegate.Styles.NormalTitle.
		Foreground(colors.themFg).
		PaddingLeft(1)
	delegate.Styles.NormalDesc = delegate.Styles.NormalDesc.
		Foreground(colors.textMuted).
		PaddingLeft(1)
	delegate.Styles.SelectedTitle = delegate.Styles.SelectedTitle.
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(colors.borderA).
		Foreground(colors.themFg).
		Bold(true).
		PaddingLeft(1)
	delegate.Styles.SelectedDesc = delegate.Styles.SelectedDesc.
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(colors.borderA).
		Foreground(colors.textMuted).
		PaddingLeft(1)
	delegate.Styles.DimmedTitle = delegate.Styles.DimmedTitle.Foreground(colors.time)
	delegate.Styles.DimmedDesc = delegate.Styles.DimmedDesc.Foreground(colors.time)
	delegate.Styles.FilterMatch = delegate.Styles.FilterMatch.Foreground(colors.filterMatch).Bold(true)
	return delegate
}

func applyChatListStyles(l *list.Model, colors uiColors) {
	l.Styles.HelpStyle = l.Styles.HelpStyle.Foreground(colors.time)
	l.Styles.NoItems = l.Styles.NoItems.Foreground(colors.time)
	l.Styles.PaginationStyle = l.Styles.PaginationStyle.Foreground(colors.time)
	l.Styles.DefaultFilterCharacterMatch = l.Styles.DefaultFilterCharacterMatch.Foreground(colors.filterMatch).Bold(true)

	filterStyles := l.Styles.Filter
	filterStyles.Focused.Prompt = filterStyles.Focused.Prompt.Foreground(colors.accentCyan)
	filterStyles.Focused.Text = filterStyles.Focused.Text.Foreground(colors.themFg)
	filterStyles.Focused.Placeholder = filterStyles.Focused.Placeholder.Foreground(colors.time)
	filterStyles.Blurred.Prompt = filterStyles.Blurred.Prompt.Foreground(colors.accentCyan)
	filterStyles.Blurred.Text = filterStyles.Blurred.Text.Foreground(colors.themFg)
	filterStyles.Blurred.Placeholder = filterStyles.Blurred.Placeholder.Foreground(colors.time)
	filterStyles.Cursor.Color = colors.accentCyan
	l.Styles.Filter = filterStyles
}

func applyTextInputStyles(ti *textinput.Model, colors uiColors) {
	tiStyles := ti.Styles()
	tiStyles.Focused.Prompt = tiStyles.Focused.Prompt.Foreground(colors.accentCyan)
	tiStyles.Focused.Text = tiStyles.Focused.Text.Foreground(colors.themFg)
	tiStyles.Focused.Placeholder = tiStyles.Focused.Placeholder.Foreground(colors.time)
	tiStyles.Blurred.Prompt = tiStyles.Blurred.Prompt.Foreground(colors.borderD)
	tiStyles.Blurred.Text = tiStyles.Blurred.Text.Foreground(colors.textMuted)
	tiStyles.Blurred.Placeholder = tiStyles.Blurred.Placeholder.Foreground(colors.time)
	tiStyles.Cursor.Color = colors.accentCyan
	ti.SetStyles(tiStyles)
}

func (s uiStyles) sidebarStatusLine(width int, bg, fg color.Color, content string) string {
	return s.sidebarStatus.
		Width(width).
		// Background(bg).
		Foreground(fg).
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

func (s uiStyles) deletePrompt(title string) string {
	return s.popupDanger.Render(title) + "\n\n  [y] yes    [n] no"
}

func (s uiStyles) renderMessagePrefix(selected bool) string {
	if !selected {
		return "  "
	}
	return s.messageSelectedPrefix.Render("> ")
}

func (s uiStyles) renderMessageHeader(timeLabel, nick string, isMe bool) string {
	nickStyle := s.messageNickThem
	if isMe {
		nickStyle = s.messageNickMe
	}
	return s.messageTime.Render("["+timeLabel+"]") + " " + nickStyle.Render("<"+nick+">") + " "
}

func (s uiStyles) renderReplyHint(author, preview string) string {
	return s.messageReply.Render(fmt.Sprintf("↩ %s: %s", author, preview))
}
