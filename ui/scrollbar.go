package ui

import (
	"math"
	"strings"
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
