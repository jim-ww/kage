package ui

import "strings"

// refreshViewport re-renders all messages and updates the viewport content.
func (m *Model) refreshViewport() {
	if m.currentChatIndex() < 0 {
		m.msgOffsets = nil
		m.viewportLines = nil
		if m.currentAccountConnecting() {
			m.viewport.SetContent("connecting...")
		} else {
			m.viewport.SetContent("")
		}
		return
	}
	if len(m.currentMessages()) == 0 && m.currentAccountConnecting() {
		m.msgOffsets = nil
		m.viewportLines = nil
		m.viewport.SetContent("connecting...")
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
		rendered := m.zone.Mark(zoneMessage(idx), padLinesToWidth(m.renderMessage(msgs[idx], idx, cw, msgs), cw))
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

// refreshViewportScrollTo re-renders and scrolls so msgIdx is visible.
func (m *Model) refreshViewportScrollTo(msgIdx int) {
	m.refreshViewport()
	if msgIdx >= 0 && msgIdx < len(m.msgOffsets) {
		m.viewport.SetYOffset(m.msgOffsets[msgIdx])
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
