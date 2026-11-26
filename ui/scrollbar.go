package ui

import (
	"math"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// scrollbarWidth is the column right of the messages the scrollbar takes.
const scrollbarWidth = 1

// messageAreaWidth is the width messages are laid out in: the chat column
// less the scrollbar's.
func (m Model) messageAreaWidth() int {
	return max(0, m.chatAreaWidth()-scrollbarWidth)
}

// scrollbarThumb is the rows [start, end) of a height-row track the thumb
// covers: where the viewport sits in the chat's whole stored history, not
// just the loaded part, measured in messages. ok is false when everything
// fits on screen.
func (m Model) scrollbarThumb(height int) (start, end int, ok bool) {
	chatIdx := m.currentChatIndex()
	if height <= 0 || chatIdx < 0 || len(m.msgOffsets) == 0 || len(m.viewportLines) == 0 {
		return 0, 0, false
	}
	var before, after int
	if chats := m.accounts[m.currentAccount].Chats; chatIdx < len(chats) {
		if chat, isChat := chats[chatIdx].(Chat); isChat {
			before, after = chat.HistoryBefore, chat.HistoryAfter
		}
	}
	lines := len(m.viewportLines)
	if before == 0 && after == 0 && lines <= height {
		return 0, 0, false
	}

	// pos is a line's place in messages: its message's index plus how far
	// down that message it is.
	n := len(m.msgOffsets)
	pos := func(line int) float64 {
		i := m.msgIndexAtOffset(line)
		next := lines
		if i+1 < n {
			next = m.msgOffsets[i+1]
		}
		h := max(1, next-m.msgOffsets[i])
		return float64(before+i) + float64(line-m.msgOffsets[i])/float64(h)
	}
	total := float64(before + n + after)
	top := m.viewport.YOffset()
	bottom := min(lines, top+height) // one past the last line on screen
	from := pos(top) / total
	to := float64(before+n) / total
	if bottom < lines {
		to = pos(bottom) / total
	}

	start = clamp(int(math.Floor(from*float64(height))), 0, height-1)
	end = clamp(int(math.Ceil(to*float64(height))), start+1, height)
	return start, end, true
}

// renderScrollbar is the scrollbar's column, height rows tall; blank when
// there's nothing to scroll.
func (m Model) renderScrollbar(height int) string {
	start, end, ok := m.scrollbarThumb(height)
	if d := m.scrollDrag; ok && d != nil && d.pending {
		// Held where it's dragged until that part of history loads.
		size := end - start
		start = clamp(d.row, 0, height-size)
		end = start + size
	}
	rows := make([]string, height)
	thumb := m.styles.scrollbarThumb.Render("┃")
	for i := range rows {
		if ok && i >= start && i < end {
			rows[i] = thumb
		} else {
			rows[i] = " "
		}
	}
	return strings.Join(rows, "\n")
}

// scrollDrag is a drag of the scrollbar under way. Where it points into
// history that isn't loaded, nothing moves until it's let go: then that
// part loads (see endScrollDrag).
type scrollDrag struct {
	grab    int // the thumb's row under the pointer
	size    int // the thumb's rows when grabbed
	row     int // the track row the thumb's top is dragged to
	pending bool
	toEnd   bool // pending the newest messages
	index   int  // else the message, of the whole history, at the top
}

// scrollJump is a chat a scrollbar drag is loading history of, for the
// HistoryWindowMsg answering it to land on the anchor.
type scrollJump struct {
	accountIdx  int
	chatAddress string
}

// unloadedCounts is the open chat's Chat.HistoryBefore/HistoryAfter.
func (m Model) unloadedCounts() (before, after int) {
	chatIdx := m.currentChatIndex()
	if chatIdx < 0 || m.currentAccount < 0 || m.currentAccount >= len(m.accounts) {
		return 0, 0
	}
	if chats := m.accounts[m.currentAccount].Chats; chatIdx < len(chats) {
		if chat, ok := chats[chatIdx].(Chat); ok {
			return chat.HistoryBefore, chat.HistoryAfter
		}
	}
	return 0, 0
}

// scrollbarTrack is the scrollbar's column and first row on screen, and
// its height; ok is false with no scrollbar showing.
func (m Model) scrollbarTrack() (x, y, height int, ok bool) {
	z := m.zone.Get(zonePaneViewport)
	if z.IsZero() || m.modals.Open() {
		return 0, 0, 0, false
	}
	height = z.EndY - z.StartY + 1
	if _, _, shown := m.scrollbarThumb(height); !shown {
		return 0, 0, 0, false
	}
	return z.EndX, z.StartY, height, true
}

// onScrollbar reports whether mouse is on the scrollbar.
func (m Model) onScrollbar(mouse tea.MouseMsg) bool {
	x, y, height, ok := m.scrollbarTrack()
	p := mouse.Mouse()
	return ok && p.X == x && p.Y >= y && p.Y < y+height
}

// dragScrollbar starts or continues a scrollbar drag with the pointer on
// screen row y, moving the thumb with it: the share of the track above the
// thumb is the share of the chat's whole history above the screen's top,
// the thumb at the bottom the newest end. Grabbed off the thumb, the thumb
// centers on the pointer.
func (m *Model) dragScrollbar(y int) tea.Cmd {
	_, top, height, ok := m.scrollbarTrack()
	if !ok {
		if m.scrollDrag == nil {
			return nil
		}
		// Kept to the end of the drag; the track came back with no thumb.
		top, height = m.zone.Get(zonePaneViewport).StartY, max(1, m.viewport.Height())
	}
	pointer := clamp(y-top, 0, height-1)
	d := m.scrollDrag
	if d == nil {
		start, end, _ := m.scrollbarThumb(height)
		d = &scrollDrag{size: max(1, end-start)}
		if pointer >= start && pointer < end {
			d.grab = pointer - start
		} else {
			d.grab = d.size / 2
		}
		m.scrollDrag = d
	}
	d.row = clamp(pointer-d.grab, 0, height-d.size)
	d.pending, d.toEnd, d.index = false, false, 0
	before, after := m.unloadedCounts()
	n := len(m.msgOffsets)

	if d.row == height-d.size {
		if after > 0 {
			d.pending, d.toEnd = true, true
			return nil
		}
		m.viewport.GotoBottom()
		m.keepSelectionOnScreen()
		return nil
	}
	g := int(float64(d.row) / float64(height) * float64(before+n+after))
	if g < before || g >= before+n {
		d.pending, d.index = true, g
		return nil
	}
	m.viewport.SetYOffset(m.msgOffsets[g-before])
	m.keepSelectionOnScreen()
	return nil
}

// endScrollDrag ends a scrollbar drag: loading the history it was let go
// over, if that isn't loaded, else paging in more at an end it reached.
func (m *Model) endScrollDrag() tea.Cmd {
	d := m.scrollDrag
	m.scrollDrag = nil
	if d == nil || !d.pending {
		return m.loadPastScrolledEdge()
	}
	chat, ok := m.currentChat()
	if !ok {
		return nil
	}
	var anchor *HistoryAnchor
	if !d.toEnd {
		anchor = &HistoryAnchor{Index: d.index}
	}
	cmd := m.daemon.LoadHistoryWindow(m.currentAccount, chat.Address, anchor)
	if cmd == nil {
		return nil
	}
	m.markHistoryWindowLoading(m.currentChatIndex(), "")
	m.pendingDateJump = nil
	m.pendingScrollJump = &scrollJump{accountIdx: m.currentAccount, chatAddress: chat.Address}
	return cmd
}

// landScrollJump shows a window a scrollbar drag loaded: its anchor at the
// top of the screen, or the newest message at the bottom without one.
func (m *Model) landScrollJump(msgs []Message, anchorStoreID int64) {
	m.refreshViewport()
	if anchorStoreID == 0 {
		m.selectedMsg = len(msgs) - 1
		m.refreshViewport()
		m.viewport.GotoBottom()
		return
	}
	for i, msg := range msgs {
		if msg.StoreID == anchorStoreID && i < len(m.msgOffsets) {
			m.selectedMsg = i
			m.refreshViewport()
			m.viewport.SetYOffset(m.msgOffsets[i])
			return
		}
	}
}
