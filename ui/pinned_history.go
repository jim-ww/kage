package ui

// pinnedWindowStep is how many additional messages growPinnedWindow reveals
// per trigger when extending the loaded window across a chat's
// PinnedHistory (see Account.PinnedHistory).
const pinnedWindowStep = 100

// growPinnedWindow extends chatIdx's loaded window across
// accounts[accountIdx].PinnedHistory by up to pinnedWindowStep messages in
// the given direction (older == true: toward the start; false: toward the
// end) — unlike trimMessagesAround (which capped the *initial* jump so it
// doesn't dump a search's entire scanned history into memory at once),
// further growth here only ever extends the window, never re-trims it, the
// same way normal (non-search) older-history scrolling already works in
// this app: only live tail growth (trimMessagesFront) caps the loaded
// window, scrolling up through history you're actively viewing does not.
// Once a direction's edge reaches PinnedHistory's boundary on that side,
// HistoryMore is updated to match; once both edges have been reached,
// PinnedHistory/PinnedWindow for this chat are removed entirely — the
// window is now an ordinary loaded window and normal storage-backed
// older-history fetches / live message appends take back over.
//
// Returns false (does nothing) if there's no PinnedHistory for this chat,
// or the requested direction is already at its edge.
func (m *Model) growPinnedWindow(accountIdx, chatIdx int, older bool) bool {
	if accountIdx < 0 || accountIdx >= len(m.accounts) {
		return false
	}
	full, ok := m.accounts[accountIdx].PinnedHistory[chatIdx]
	if !ok {
		return false
	}
	win := m.accounts[accountIdx].PinnedWindow[chatIdx]
	start, end := win[0], win[1]

	var newStart, newEnd int
	switch {
	case older && start > 0:
		newStart, newEnd = max(0, start-pinnedWindowStep), end
	case !older && end < len(full):
		newStart, newEnd = start, min(len(full), end+pinnedWindowStep)
	default:
		return false
	}

	windowed := make([]Message, newEnd-newStart)
	copy(windowed, full[newStart:newEnd])
	for i := range windowed {
		if windowed[i].ReplyTo == nil {
			continue
		}
		shifted := *windowed[i].ReplyTo - newStart
		windowed[i].ReplyTo = &shifted
	}

	m.accounts[accountIdx].Messages[chatIdx] = windowed
	m.accounts[accountIdx].HistoryMore[chatIdx] = newStart > 0

	if newStart == 0 && newEnd == len(full) {
		delete(m.accounts[accountIdx].PinnedHistory, chatIdx)
		delete(m.accounts[accountIdx].PinnedWindow, chatIdx)
	} else {
		m.accounts[accountIdx].PinnedWindow[chatIdx] = [2]int{newStart, newEnd}
	}

	if accountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
		// selectedMsg pointed into the old window's indexing; shift it by
		// however much the window's start moved so it still points at the
		// same underlying message — nothing already visible is ever
		// dropped by a grow, so no clamping is needed.
		m.selectedMsg += start - newStart
		m.refreshViewportFullScrollTo(m.selectedMsg)
	}
	return true
}
