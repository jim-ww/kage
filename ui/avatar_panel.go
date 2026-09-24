package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The sidebar's avatar panel: the contact's picture, pinned below the chat
// list. The chat pane's own rows are the scarce ones — a picture there
// costs messages — while the sidebar's are not, since the list scrolls and
// keeps the selection in view. The sidebar is also the only place wide
// enough (19-35 columns) to render an avatar at a size that reads as a
// person rather than as a colored smear.
//
// It previews the *highlighted* row while the chat list has focus, and the
// open chat otherwise (see avatarPanelChat) — which is what makes it part
// of the list rather than a detail panel that happens to sit in the list's
// territory.
const (
	// avatarPanelMaxHeightPct is the share of the sidebar the picture may
	// take. A half-block cell is two pixels tall, so the picture is half as
	// many rows as columns and every column the sidebar is dragged wider
	// costs half a row of chat list — left unbounded, a wide sidebar turns
	// the chat list into a stub. Capped on height rather than on a column
	// count so that widening the sidebar does keep buying a bigger picture,
	// up to this share of it.
	avatarPanelMaxHeightPct = 50
	// avatarPanelMinListRows is how many chat rows the list keeps no matter
	// what; the panel shrinks, then disappears, before eating into these.
	avatarPanelMinListRows = 6
	// avatarPanelMinCols is the narrowest picture worth drawing — below it
	// the panel is suppressed entirely rather than rendered as mush.
	avatarPanelMinCols = 8
	// avatarPanelTextRows is the caption under the picture: name, then
	// presence.
	avatarPanelTextRows = 2
)

// avatarPanelSize is the picture's size in cells, or (0, 0) when the panel
// shouldn't be drawn at all. Rows is half of cols because a half-block cell
// carries two pixels vertically, so a square avatar needs half as many rows
// as columns — so widening the sidebar grows the picture until it reaches
// avatarPanelMaxHeightPct of the sidebar, and then until the chat list is
// down to avatarPanelMinListRows.
//
// Deliberately independent of which chat is open: the panel's height feeds
// the chat list's own height (see updateSizes), and a height that changed
// with the selected contact would resize the list under the selection on
// every move.
func (m Model) avatarPanelSize() (cols, rows int) {
	if m.sidebarWidth() <= 0 || !anyAvatarKnown() {
		return 0, 0
	}
	maxRows := m.height * avatarPanelMaxHeightPct / 100
	cols = min(m.sidebarContentWidth()-2, maxRows*2)
	cols -= cols % 2 // an odd width can't split into whole pixel rows
	rows = cols / 2

	// Give back rows until the chat list has its floor again.
	avail := m.height - sidebarStatusHeight - avatarPanelMinListRows - avatarPanelTextRows
	for rows > 0 && rows > avail {
		rows--
		cols = rows * 2
	}
	if cols < avatarPanelMinCols {
		return 0, 0
	}
	return cols, rows
}

// avatarPanelHeight is how many sidebar rows the panel occupies, picture
// plus caption. Zero when it isn't drawn.
func (m Model) avatarPanelHeight() int {
	_, rows := m.avatarPanelSize()
	if rows == 0 {
		return 0
	}
	return rows + avatarPanelTextRows
}

// avatarPanelChat is whose avatar the panel shows: the row the chat list
// has highlighted while the list is what's focused, so arrowing through
// contacts previews their faces, and the open chat the rest of the time.
func (m Model) avatarPanelChat() (Chat, bool) {
	if m.selectedView == viewChats {
		if chat, ok := m.chats.SelectedItem().(Chat); ok {
			return chat, true
		}
	}
	return m.currentChat()
}

// renderAvatarPanel builds the panel, centered in the sidebar's content
// width. Returns "" when the panel isn't drawn, in which case
// avatarPanelHeight is zero and no rows were reserved for it.
func (m Model) renderAvatarPanel(width int) string {
	cols, rows := m.avatarPanelSize()
	if rows == 0 {
		return ""
	}
	chat, ok := m.avatarPanelChat()
	if !ok {
		// Rows are already reserved (the height can't depend on the
		// selection), so fill them rather than letting the sidebar's
		// contents shift up by exactly the panel's height.
		return strings.Repeat("\n", rows+avatarPanelTextRows-1)
	}

	center := lipgloss.NewStyle().Width(width).Align(lipgloss.Center)
	picture := center.Render(renderAvatarLarge(chat.Name, chat.Address, cols, rows))

	name := m.styles.messageNickMe.Render(ansi.Truncate(chat.Name, max(1, width), "…"))
	presence := presenceGlyph(chat.Presence) + " " +
		lipgloss.NewStyle().Foreground(m.styles.colors.textMuted).
			Render(ansi.Truncate(presenceLabel(chat.Presence), max(1, width-2), "…"))

	return lipgloss.JoinVertical(
		lipgloss.Left,
		picture,
		center.Render(name),
		center.Render(presence),
	)
}
