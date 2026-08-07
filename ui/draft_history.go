package ui

// pushDraftSnapshot records value as a new undo point for the compose box,
// discarding any redo history beyond the current position. Called whenever
// the draft text changes by a means other than undoDraft/redoDraft
// themselves. A no-op if value is already the most recent snapshot, so
// non-edit updates (cursor moves, blink ticks) routed through the same
// change-detection don't pad the history with duplicates.
func (m *Model) pushDraftSnapshot(value string) {
	if m.draftHistIdx < len(m.draftHistory)-1 {
		m.draftHistory = m.draftHistory[:m.draftHistIdx+1]
	}
	if len(m.draftHistory) > 0 && m.draftHistory[len(m.draftHistory)-1] == value {
		return
	}
	m.draftHistory = append(m.draftHistory, value)
	m.draftHistIdx = len(m.draftHistory) - 1
}

// undoDraft steps the compose box back to its previous snapshot, if any.
// Reports whether it moved.
func (m *Model) undoDraft() bool {
	if m.draftHistIdx == 0 {
		return false
	}
	m.draftHistIdx--
	m.input.SetValue(m.draftHistory[m.draftHistIdx])
	return true
}

// redoDraft steps the compose box forward to the snapshot undone by the most
// recent undoDraft, if any. Reports whether it moved.
func (m *Model) redoDraft() bool {
	if m.draftHistIdx >= len(m.draftHistory)-1 {
		return false
	}
	m.draftHistIdx++
	m.input.SetValue(m.draftHistory[m.draftHistIdx])
	return true
}

// resetDraftHistory clears undo/redo history for the compose box, starting
// fresh from value — used whenever the box's content changes for a reason
// other than typing (message sent, draft explicitly cleared, or a different
// message's content loaded in for editing/reacting) so undo can't reach back
// across those boundaries into an unrelated draft.
func (m *Model) resetDraftHistory(value string) {
	m.draftHistory = []string{value}
	m.draftHistIdx = 0
}
