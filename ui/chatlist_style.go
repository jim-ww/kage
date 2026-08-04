package ui

import (
	"fmt"
	"io"
	"strings"

	"charm.land/bubbles/v2/filepicker"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	zone "github.com/lrstanley/bubblezone/v2"
)

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

func applyTextAreaStyles(ta *textarea.Model, colors uiColors) {
	taStyles := ta.Styles()
	taStyles.Focused.Prompt = taStyles.Focused.Prompt.Foreground(colors.accentCyan)
	taStyles.Focused.Text = taStyles.Focused.Text.Foreground(colors.themFg)
	taStyles.Focused.Placeholder = taStyles.Focused.Placeholder.Foreground(colors.time)
	taStyles.Focused.CursorLine = taStyles.Focused.CursorLine.UnsetBackground()
	taStyles.Blurred.Prompt = taStyles.Blurred.Prompt.Foreground(colors.borderD)
	taStyles.Blurred.Text = taStyles.Blurred.Text.Foreground(colors.textMuted)
	taStyles.Blurred.Placeholder = taStyles.Blurred.Placeholder.Foreground(colors.time)
	taStyles.Blurred.CursorLine = taStyles.Blurred.CursorLine.UnsetBackground()
	taStyles.Cursor.Color = colors.accentCyan
	ta.SetStyles(taStyles)
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
