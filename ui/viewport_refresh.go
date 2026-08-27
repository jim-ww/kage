package ui

import "strings"

// refreshViewport re-renders all messages and updates the viewport content.
func (m *Model) refreshViewport() {
	if m.currentChatIndex() < 0 {
		m.msgOffsets = nil
		m.viewportLines = nil
		if m.currentAccountConnecting() {
			m.viewport.SetContent(m.styles.plainText.Render("connecting..."))
		} else {
			m.viewport.SetContent("")
		}
		return
	}
	if len(m.currentMessages()) == 0 && m.currentAccountConnecting() {
		m.msgOffsets = nil
		m.viewportLines = nil
		m.viewport.SetContent(m.styles.plainText.Render("connecting..."))
		return
	}
	content, offsets := m.renderMessagesWithOffsets()
	m.msgOffsets = offsets
	m.viewportLines = strings.Split(content, "\n")
	m.viewport.SetContentLines(m.viewportLines)
}

// refreshViewportSelection re-renders only the messages at oldIdx and newIdx
// (the previous and new selected/hovered message) and patches their lines
// back into the cached viewport content, instead of re-wrapping every
// message. Selection/hover only changes a message's prefix styling, never
// its wrapping (see renderMessagePrefix — both states are 2 cells wide), so
// each message's line range and count stay fixed and can be spliced in
// place. This matters because handleMouseMotion calls this on every mouse
// motion event; a full refreshViewport() there made the highlighted message
// visibly lag behind a fast-moving mouse in chats with many messages.
func (m *Model) refreshViewportSelection(oldIdx, newIdx int) {
	if m.viewportLines == nil || len(m.msgOffsets) == 0 {
		m.refreshViewport()
		return
	}

	cw := m.chatAreaWidth()
	if cw <= 10 {
		m.refreshViewport()
		return
	}
	msgs := m.currentMessages()
	nameWidth := maxSenderNameWidth(msgs)

	for _, idx := range []int{oldIdx, newIdx} {
		if idx < 0 || idx >= len(msgs) || idx >= len(m.msgOffsets) {
			continue
		}
		start := m.msgOffsets[idx]
		end := len(m.viewportLines)
		if idx+1 < len(m.msgOffsets) {
			end = m.msgOffsets[idx+1]
		}
		if start < 0 || end > len(m.viewportLines) || start > end {
			m.refreshViewport()
			return
		}
		rendered := m.zone.Mark(zoneMessage(idx), padLinesToWidth(m.renderMessage(msgs[idx], idx, cw, msgs, nameWidth), cw))
		newLines := strings.Split(rendered, "\n")
		if len(newLines) != end-start {
			// Wrapping changed unexpectedly (e.g. width changed mid-flight);
			// fall back to a full re-render rather than corrupt offsets.
			m.refreshViewport()
			return
		}
		copy(m.viewportLines[start:end], newLines)
	}

	// SetContentLines takes the already-split lines directly, skipping the
	// join-then-resplit that SetContent(strings.Join(...)) would do.
	m.viewport.SetContentLines(m.viewportLines)
}

// refreshViewportScrollTo re-renders only the messages at oldIdx and msgIdx
// (like refreshViewportSelection — see its doc comment) and, like vim's
// scrolloff, keeps the selected message at least scrollMargin lines away
// from whichever edge of the viewport it's approaching — once it gets that
// close, the viewport scrolls in lockstep so the message holds that same
// distance from the edge on every subsequent move, instead of sitting still
// until it hits the edge and then snapping back. If the message is already
// further than the margin from both edges, the viewport doesn't move at
// all. Used for keyboard navigation (MsgUp/MsgDown/HalfPageUp/HalfPageDown):
// a full refreshViewport() there made the selection visibly lag behind rapid
// key presses in chats with many messages, the same issue
// refreshViewportSelection already fixed for mouse motion.
func (m *Model) refreshViewportScrollTo(oldIdx, msgIdx int) {
	extra := m.clearStaleMessageHover(oldIdx, msgIdx)
	m.refreshViewportSelection(oldIdx, msgIdx)
	if extra >= 0 {
		m.refreshViewportSelection(extra, extra)
	}
	m.applyScrollMargin(msgIdx)
}

// clearStaleMessageHover drops a mouse-hover left over on some other message
// row before a keyboard-driven selection change - selection moving via the
// keyboard never itself moves the pointer, so without this a message the
// mouse is still physically sitting over keeps showing its hover-only
// affordances (the underlined header, the reply button) even after the
// keyboard selection has moved on to a different row entirely. Returns the
// index of that other row if it needs its own re-render (it isn't already
// covered by refreshViewportScrollTo's oldIdx/msgIdx patch), or -1.
func (m *Model) clearStaleMessageHover(oldIdx, newIdx int) int {
	if m.hover == nil || m.hover.id == "" {
		return -1
	}
	hoveredIdx, ok := messageIndexFromZone(m.hover.id)
	if !ok {
		return -1
	}
	m.hover.id = ""
	m.hover.replyBtnIdx = -1
	m.hover.reactBtnIdx = -1
	if hoveredIdx == oldIdx || hoveredIdx == newIdx {
		return -1
	}
	return hoveredIdx
}

// refreshViewportFullScrollTo is refreshViewportScrollTo's full-re-render
// counterpart, for callers where every message's offset may have shifted
// (e.g. HistoryWindowMsg replacing the whole loaded window) so the
// incremental refreshViewportSelection patch — which assumes only
// oldIdx/msgIdx's own lines changed — cannot be used.
func (m *Model) refreshViewportFullScrollTo(msgIdx int) {
	m.refreshViewport()
	m.applyScrollMargin(msgIdx)
}

// applyScrollMargin is the scrolloff logic shared by
// refreshViewportScrollTo/refreshViewportFullScrollTo — see
// refreshViewportScrollTo's doc comment.
func (m *Model) applyScrollMargin(msgIdx int) {
	if msgIdx < 0 || msgIdx >= len(m.msgOffsets) {
		return
	}

	start := m.msgOffsets[msgIdx]
	end := len(m.viewportLines) - 1
	if msgIdx+1 < len(m.msgOffsets) {
		end = m.msgOffsets[msgIdx+1] - 1
	}

	height := m.viewport.Height()
	if height <= 0 {
		return
	}
	margin := min(height/3, (height-1)/2)
	top := m.viewport.YOffset()

	switch {
	case start < top+margin:
		m.viewport.SetYOffset(max(0, start-margin))
	case end > top+height-1-margin:
		m.viewport.SetYOffset(end - height + 1 + margin)
	}
}

// msgIndexAtOffset returns the index of the topmost message at or above the
// given viewport line offset, used to keep message selection in sync after
// free-scrolling (paging) through the viewport.
func (m Model) msgIndexAtOffset(yOffset int) int {
	idx := 0
	for i, off := range m.msgOffsets {
		if off > yOffset {
			break
		}
		idx = i
	}
	return idx
}

// visibleMessageCount returns how many messages currently have at least one
// line on screen, given the viewport's current scroll position — used to
// size a "half page" jump in message-count terms rather than raw line count.
// Sizing by lines instead would jump by however many messages happen to fit
// in half the pane's height, which balloons on chats with multi-line
// messages (attachments, replies, reactions) until it feels like a full-page
// jump instead of a half one.
func (m Model) visibleMessageCount() int {
	height := m.viewport.Height()
	if len(m.msgOffsets) == 0 || height <= 0 {
		return 0
	}
	top := m.viewport.YOffset()
	bottom := top + height - 1
	return m.msgIndexAtOffset(bottom) - m.msgIndexAtOffset(top) + 1
}
