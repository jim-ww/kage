package ui

import (
	"strings"
	"sync"

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
	// avatarPanelMinFreeRows is the least free space worth drawing into —
	// below a 12x12-pixel picture's six rows there is nothing to draw.
	avatarPanelMinFreeRows = avatarPanelMinCols / 2
	// avatarPanelMinCols is the narrowest picture worth drawing: a cell
	// carries two pixels vertically, so this is a 12x12-pixel image. Below
	// roughly that a photo stops resolving into a face and becomes a
	// colored smear, which is worse than nothing — the same reason a
	// contact without an avatar gets no panel rather than a monogram. A
	// short or narrow terminal therefore simply has no avatar panel.
	avatarPanelMinCols = 12
)

// avatarPanelSize is the picture's size in cells given how many rows at the
// bottom of the chat list are currently unused, or (0, 0) when the panel
// shouldn't be drawn at all. Rows is half of cols because a half-block cell
// carries two pixels vertically, so a square avatar needs half as many rows
// as columns.
//
// Sized to free space rather than taking rows from the list: a contact
// without an avatar is the common case, and reserving rows for them left a
// blank region where chats should be. The list never changes height, so
// nothing shifts under the cursor either — the picture simply appears in
// space the list isn't using, and gives way as chats fill it.
//
// Deliberately independent of which chat is open: the panel's height feeds
// the chat list's own height (see updateSizes), and a height that changed
// with the selected contact would resize the list under the selection on
// every move.
func (m Model) avatarPanelSize(freeRows int) (cols, rows int) {
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
	if freeRows < avatarPanelMinFreeRows {
		return 0, 0
	}
	maxRows := min(freeRows, m.height*avatarPanelMaxHeightPct/100)
	cols = min(m.sidebarContentWidth()-2, maxRows*2)
	cols -= cols % 2 // an odd width can't split into whole pixel rows
	if cols < avatarPanelMinCols {
		return 0, 0
	}
	return cols, cols / 2
}

// avatarPanelHeight is how many sidebar rows the panel occupies. Zero when
// it isn't drawn.
func (m Model) avatarPanelHeight(freeRows int) int {
	_, rows := m.avatarPanelSize(freeRows)
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
func (m Model) renderAvatarPanel(width, freeRows int) string {
	cols, rows := m.avatarPanelSize(freeRows)
	if rows == 0 {
		return ""
	}
	chat, ok := m.avatarPanelChat()
	if !ok || !hasAvatarPicture(chat.Address) {
		// Nothing to draw, and nothing was taken from the list to draw it
		// into. A large monogram was tried here and reads as a colored
		// block with a letter in it, not as anybody's identity.
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
func (m Model) avatarPanelOverlay(sidebarBody string) (content string, x, y int, ok bool) {
	// The accounts panel replaces the chat list with its own content, whose
	// blank rows are its own business.
	if m.selectedView == viewAccounts {
		return "", 0, 0, false
	}
	freeRows := trailingBlankRows(sidebarBody)
	rows := m.avatarPanelHeight(freeRows)
	if rows == 0 {
		return "", 0, 0, false
	}
	panel := m.renderAvatarPanel(m.sidebarContentWidth(), freeRows)
	if panel == "" {
		return "", 0, 0, false
	}
	// The sidebar has no top or left border (see uiStyles.sidebar), so its
	// content starts at column 0. The picture sits at the bottom of the
	// list's unused rows, so it stays put as the list grows down toward it.
	return panel, 0, sidebarStatusHeight + m.chats.Height() - rows, true
}

// trailingBlankRows counts the unused rows at the end of the chat list —
// what the list is padded out with when it has fewer chats than height.
// Counted from the rendered body rather than from the item count so it
// stays right whatever chrome the list draws (pagination, filter prompt)
// without this having to know about any of it.
func trailingBlankRows(body string) int {
	lines := strings.Split(body, "\n")
	blank := 0
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(ansi.Strip(lines[i])) != "" {
			break
		}
		blank++
	}
	return blank
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
