package ui

import (
	"fmt"
	"image/color"
	"io"
	"strings"

	"charm.land/bubbles/v2/filepicker"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	zone "github.com/lrstanley/bubblezone/v2"
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

// Presence colors are fixed traffic-light semantics (green/amber/gray),
// independent of the theme — "online" should read as green in every theme.
var (
	presenceOnlineStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))  // green
	presenceAwayStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("136")) // amber/brown
	presenceOfflineStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))   // gray
)

// presenceGlyph renders a contact's presence as a single colored symbol:
// ● online, ◐ away, ○ offline/unknown.
func presenceGlyph(p Presence) string {
	switch p {
	case PresenceOnline:
		return presenceOnlineStyle.Render("●")
	case PresenceAway:
		return presenceAwayStyle.Render("◐")
	default:
		return presenceOfflineStyle.Render("○")
	}
}

// presenceGlyphOn is presenceGlyph rendered against bg — used when the
// glyph is going to sit inside a row that itself has a background (e.g. a
// hover highlight), so the dot's own style needs to carry that same
// background rather than leaving a gap where the row highlight would
// otherwise show through unstyled.
func presenceGlyphOn(p Presence, bg color.Color) string {
	switch p {
	case PresenceOnline:
		return presenceOnlineStyle.Background(bg).Render("●")
	case PresenceAway:
		return presenceAwayStyle.Background(bg).Render("◐")
	default:
		return presenceOfflineStyle.Background(bg).Render("○")
	}
}

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

func newChatListDelegate(colors uiColors, zm *zone.Manager, mouseEnabled bool, hv *hoverState) list.ItemDelegate {
	delegate := list.NewDefaultDelegate()
	delegate.SetSpacing(0)
	delegate.SetHeight(2)
	delegate.Styles.NormalTitle = delegate.Styles.NormalTitle.
		Foreground(colors.themFg).
		PaddingLeft(1)
	delegate.Styles.NormalDesc = delegate.Styles.NormalDesc.
		Foreground(colors.textMuted).
		PaddingLeft(1)
	// PaddingLeft(0), not 1: the list.DefaultDelegate truncates title/desc
	// text to m.width minus NormalTitle's padding alone, regardless of which
	// style ends up rendering the row — so SelectedTitle/Desc must consume
	// the same total 1 column of left decoration as NormalTitle's padding
	// does, or the selected row ends up 1 column wider than the list's
	// width and wraps instead of fitting (border(1) + padding(1) = 2).
	delegate.Styles.SelectedTitle = delegate.Styles.SelectedTitle.
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(colors.borderA).
		Foreground(colors.themFg).
		Bold(true).
		PaddingLeft(0)
	delegate.Styles.SelectedDesc = delegate.Styles.SelectedDesc.
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(colors.borderA).
		Foreground(colors.textMuted).
		PaddingLeft(0)
	delegate.Styles.DimmedTitle = delegate.Styles.DimmedTitle.Foreground(colors.time)
	delegate.Styles.DimmedDesc = delegate.Styles.DimmedDesc.Foreground(colors.time)
	delegate.Styles.FilterMatch = delegate.Styles.FilterMatch.Foreground(colors.filterMatch).Bold(true)
	if !mouseEnabled {
		return delegate
	}

	// Selected+hover recolors the row's left border — a decoration drawn at
	// the fixed edge of the block, not layered on top of the row's own
	// rendered text, so it isn't at the mercy of resets embedded inside
	// that text (e.g. from the presence dot's own pre-rendered color).
	hoveredSelected := delegate
	hoveredSelected.Styles.SelectedTitle = hoveredSelected.Styles.SelectedTitle.BorderForeground(colors.accentCyan)
	hoveredSelected.Styles.SelectedDesc = hoveredSelected.Styles.SelectedDesc.BorderForeground(colors.accentCyan)

	return zoneChatListDelegate{
		DefaultDelegate: delegate,
		hoverSelected:   hoveredSelected,
		colors:          colors,
		zone:            zm,
		hover:           hv,
	}
}

// zoneChatListDelegate wraps DefaultDelegate's rendering with a bubblezone
// mark per row so clicks/wheel events can be mapped back to the chat at
// that index (see zoneChatItem, handleMouseClick), and highlights the row
// currently under the pointer (see hoverState). Only used when mouse
// support is enabled — otherwise the plain DefaultDelegate is returned, so
// no zone markers are ever emitted into the rendered output.
type zoneChatListDelegate struct {
	list.DefaultDelegate
	hoverSelected list.DefaultDelegate // used when the hovered row is also the selected one
	colors        uiColors
	zone          *zone.Manager
	hover         *hoverState
}

func (d zoneChatListDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	hovered := d.hover.id == zoneChatItem(index)
	selected := index == m.Index()

	var sb strings.Builder
	switch {
	case hovered && selected:
		d.hoverSelected.Render(&sb, m, index, item)
	case hovered:
		if chat, ok := item.(Chat); ok {
			sb.WriteString(renderHoverChatRow(chat, d.colors, m.Width()))
		} else {
			d.DefaultDelegate.Render(&sb, m, index, item)
		}
	default:
		d.DefaultDelegate.Render(&sb, m, index, item)
	}
	fmt.Fprint(w, d.zone.Mark(zoneChatItem(index), padLinesToWidth(sb.String(), m.Width())))
}

// padLinesToWidth pads every line to width with trailing spaces (ANSI-aware,
// via lipgloss.Width). zone.Mark's start/end markers land at the first and
// last character of the marked content; if the row's lines are different
// widths (title long, desc short/empty), the end marker lands at the short
// line's column and InBounds' single start→end rectangle shrinks to that —
// producing a tiny click/hover target instead of the full row. Padding every
// line to the same width keeps the end marker pinned to the right edge, so
// the whole row is inside the box.
func padLinesToWidth(content string, width int) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if pad := width - lipgloss.Width(line); pad > 0 {
			lines[i] = line + strings.Repeat(" ", pad)
		}
	}
	return strings.Join(lines, "\n")
}

// renderHoverChatRow builds the hovered-non-selected chat row's two lines.
// It doesn't reuse Chat.Title()/list.DefaultDelegate: Title() embeds an
// already-rendered, already-reset presence glyph, and rendering that whole
// string through another Style() (to add the hover background) is a nested
// re-render — content that's already been through Style.Render() getting
// wrapped in more Style.Render() — which is the documented corruption
// pattern for this codebase (see the border-glyph bug fixed earlier this
// session). The fix isn't a raw-escape workaround; it's to never nest the
// render in the first place: each fragment below is rendered exactly once,
// carries the row's background itself, and fragments are joined by plain
// string concatenation — ordinary lipgloss usage, not manual SGR.
func renderHoverChatRow(c Chat, colors uiColors, width int) string {
	bg := colors.accentCyan
	fg := colors.panelEdge

	pad := lipgloss.NewStyle().Background(bg).Render(" ")
	dot := presenceGlyphOn(c.Presence, bg)
	// Truncate name/desc to width ourselves — unlike list.DefaultDelegate
	// (which truncates to its own textwidth), nothing downstream of this
	// row does, and an untruncated long name/address wraps the row instead
	// of clipping, growing the list taller than its allotted height (same
	// failure mode fixed for the selected row and the sidebar border).
	nameWidth := max(1, width-3) // pad + dot + pad
	name := lipgloss.NewStyle().Background(bg).Foreground(fg).Bold(true).
		Render(ansi.Truncate(c.Name, nameWidth, "…"))
	title := pad + dot + pad + name

	desc := ""
	if text := c.Description(); text != "" {
		descWidth := max(1, width-1) // pad
		desc = pad + lipgloss.NewStyle().Background(bg).Foreground(fg).
			Render(ansi.Truncate(text, descWidth, "…"))
	}

	return title + "\n" + desc
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

func applyFilePickerStyles(p *filepicker.Model, colors uiColors) {
	p.Styles.Cursor = p.Styles.Cursor.Foreground(colors.accentCyan)
	p.Styles.Selected = p.Styles.Selected.Foreground(colors.accentCyan).Bold(true)
	p.Styles.DisabledSelected = p.Styles.DisabledSelected.Foreground(colors.textMuted)
	p.Styles.DisabledCursor = p.Styles.DisabledCursor.Foreground(colors.textMuted)
	p.Styles.Directory = p.Styles.Directory.Foreground(colors.replyFg)
	p.Styles.Symlink = p.Styles.Symlink.Foreground(colors.filterMatch)
	p.Styles.File = p.Styles.File.Foreground(colors.themFg)
	p.Styles.DisabledFile = p.Styles.DisabledFile.Foreground(colors.textMuted)
	p.Styles.Permission = p.Styles.Permission.Foreground(colors.time)
	p.Styles.FileSize = p.Styles.FileSize.Foreground(colors.time)
	p.Styles.EmptyDirectory = p.Styles.EmptyDirectory.Foreground(colors.time)
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
