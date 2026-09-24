package ui

import (
	"strings"
	"sync"
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
//
// Deliberately picture-only, with no caption: the name and presence it
// would carry are already on the chat-list row it is previewing, directly
// above it and highlighted, and again in the chat status bar for the open
// chat. A third copy an inch from the second is not information.
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
	// avatarPanelMinCols is the narrowest picture worth drawing: a cell
	// carries two pixels vertically, so this is a 12x12-pixel image. Below
	// roughly that a photo stops resolving into a face and becomes a
	// colored smear, which is worse than nothing — the same reason a
	// contact without an avatar gets no panel rather than a monogram. A
	// short or narrow terminal therefore simply has no avatar panel.
	avatarPanelMinCols = 12
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
	// In narrow mode the sidebar is the entire terminal, so the picture
	// would be as wide as the screen and — being square — half as tall as
	// it again, leaving the chat list a strip at the top. The list is the
	// only thing on screen there; it should be the whole of it.
	if m.narrow() {
		return 0, 0
	}
	maxRows := m.height * avatarPanelMaxHeightPct / 100
	cols = min(m.sidebarContentWidth()-2, maxRows*2)
	cols -= cols % 2 // an odd width can't split into whole pixel rows
	rows = cols / 2

	// Give back rows until the chat list has its floor again.
	avail := m.height - sidebarStatusHeight - avatarPanelMinListRows
	for rows > 0 && rows > avail {
		rows--
		cols = rows * 2
	}
	if cols < avatarPanelMinCols {
		return 0, 0
	}
	return cols, rows
}

// avatarPanelHeight is how many sidebar rows the panel occupies. Zero when
// it isn't drawn.
func (m Model) avatarPanelHeight() int {
	_, rows := m.avatarPanelSize()
	return rows
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
	if !ok || !hasAvatarPicture(chat.Address) {
		// Nothing to draw. The rows stay reserved — the panel's height
		// can't depend on which contact is selected without resizing the
		// chat list as the cursor moves through it — and the sidebar's own
		// padding leaves them blank, which reads as the end of the list
		// rather than as a placeholder. A large monogram here was tried
		// and it reads as a colored block with a letter in it, not as
		// anybody's identity.
		return ""
	}

	key := avatarPanelCacheKey{
		width: width, cols: cols, rows: rows,
		address: chat.Address, name: chat.Name,
		gen: avatarGeneration(),
	}
	if cached, ok := avatarPanelCache.get(key); ok {
		return cached
	}

	var sb strings.Builder
	for i, line := range strings.Split(mustRenderAvatarPicture(chat.Address, cols, rows), "\n") {
		if i > 0 {
			sb.WriteByte('\n')
		}
		writeCentered(&sb, line, cols, width)
	}

	out := sb.String()
	avatarPanelCache.put(key, out)
	return out
}

// writeCentered writes line indented so that its lineWidth columns sit in
// the middle of width. The width is passed in rather than measured because
// the picture's lines are tens of kilobytes of escape sequences apiece:
// centering them with lipgloss's Width().Align() — which rescans the string
// for grapheme widths, as does every style layered over it afterwards —
// cost more than rendering the picture in the first place, and a window
// resize paid it on every size message.
func writeCentered(sb *strings.Builder, line string, lineWidth, width int) {
	left := max(0, (width-lineWidth)/2)
	if left > 0 {
		sb.WriteString(strings.Repeat(" ", left))
	}
	sb.WriteString(line)
	// Padded out to the full width, not just indented: the panel is
	// composited over the finished frame (see avatarPanelOverlay), and an
	// overlay whose lines are different widths would leave slices of the
	// frame underneath showing through the short ones.
	if right := width - left - lineWidth; right > 0 {
		sb.WriteString(ansiReset)
		sb.WriteString(strings.Repeat(" ", right))
	}
}

// avatarPanelOverlay is the panel plus where to composite it onto the
// finished frame: the sidebar's own columns, directly below the chat list.
//
// Drawn as an overlay rather than joined into the sidebar's content because
// the picture is tens of kilobytes of escape sequences, and everything the
// sidebar's content passes through afterwards — the list style, the panel
// style, the bordered box, the join with the chat pane — is a lipgloss
// Render that rescans all of it for grapheme widths. Overlaying costs one
// pass over the frame's own lines instead, and keeps the picture out of the
// sidebar's render cache key, so scrolling the chat list no longer
// re-renders it either.
func (m Model) avatarPanelOverlay() (content string, x, y int, ok bool) {
	// The accounts panel replaces the chat list with its own content, which
	// the panel's reserved rows say nothing about — it would draw over it.
	if m.selectedView == viewAccounts || m.avatarPanelHeight() == 0 {
		return "", 0, 0, false
	}
	panel := m.renderAvatarPanel(m.sidebarContentWidth())
	if panel == "" {
		return "", 0, 0, false
	}
	// The sidebar has no top or left border (see uiStyles.sidebar), so its
	// content starts at column 0, below the two-row account bar and the
	// chat list's own rows.
	return panel, 0, sidebarStatusHeight + m.chats.Height(), true
}

// avatarPanelCacheKey is everything renderAvatarPanel's output depends on.
type avatarPanelCacheKey struct {
	width, cols, rows int
	address, name     string
	gen               uint64
}

// avatarPanelCache memoizes the centered panel, not just the picture
// inside it: the centering is cheap next to a cold picture render but not
// next to nothing, and most frames change neither.
var avatarPanelCache avatarPanelMemo

type avatarPanelMemo struct {
	mu       sync.Mutex
	key      avatarPanelCacheKey
	rendered string
}

func (c *avatarPanelMemo) get(key avatarPanelCacheKey) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rendered == "" || c.key != key {
		return "", false
	}
	return c.rendered, true
}

func (c *avatarPanelMemo) put(key avatarPanelCacheKey, rendered string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.key, c.rendered = key, rendered
}
