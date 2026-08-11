package ui

// growPinnedWindow slides chatIdx's loaded window across
// accounts[accountIdx].PinnedHistory by up to half of maxMessagesPerChat in
// the given direction (older == true: toward the start; false: toward the
// end), keeping the window capped at maxMessagesPerChat throughout — unlike
// normal (non-search) older-history scrolling, which never trims what it's
// already loaded, letting the window here grow unboundedly made paging all
// the way to the start of a long history (or jumpToLatestMessage unwinding
// back to the tail) render/re-render an ever-larger message set on every
// step, visibly worse the further it went. The already-decrypted
// PinnedHistory means nothing already-viewed is lost by re-trimming — it's
// just a slice away the next time this direction is paged again.
//
// Stepping by half the window (not a full window's worth) guarantees the
// message that triggered the grow (always at the loaded window's near edge
// — see maybeLoadOlderHistory/maybeLoadNewerHistory) stays inside the new
// window rather than sliding past it. Once a direction's edge reaches
// PinnedHistory's boundary on that side, HistoryMore is updated to match;
// once the window spans the entire retained history (only possible once
// the whole chat fits within one window), PinnedHistory/PinnedWindow for
// this chat are removed — ordinary storage-backed paging / live-tail
// appending takes back over.
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

	limit := m.maxMessagesPerChat
	if limit <= 0 || limit > len(full) {
		limit = len(full)
	}
	step := max(1, limit/2)

	var newStart, newEnd int
	switch {
	case older && start > 0:
		newStart = max(0, start-step)
		newEnd = min(len(full), newStart+limit)
	case !older && end < len(full):
		newEnd = min(len(full), end+step)
		newStart = max(0, newEnd-limit)
	default:
		return false
	}

	windowed := make([]Message, newEnd-newStart)
	copy(windowed, full[newStart:newEnd])
	for i := range windowed {
		if windowed[i].ReplyTo == nil {
			continue
		}
		rt := *windowed[i].ReplyTo
		if rt < newStart || rt >= newEnd {
			windowed[i].ReplyTo = nil
		} else {
			shifted := rt - newStart
			windowed[i].ReplyTo = &shifted
		}
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
		// same underlying message — the step<limit/2 guarantee above means
		// it's always still inside the new window, no clamping needed.
		m.selectedMsg += start - newStart
		m.refreshViewportFullScrollTo(m.selectedMsg)
	}
	return true
}

// unstickPinnedWindow drops accounts[accountIdx].PinnedHistory for chatIdx
// (if any), replacing the loaded window in one step with the last
// maxMessagesPerChat messages of the retained full history — used by
// jumpToLatestMessage instead of repeatedly calling growPinnedWindow, which
// would re-render an ever-larger window on every one of however many steps
// it takes to reach the tail of a long history. Returns true if there was a
// pinned window to unstick.
func (m *Model) unstickPinnedWindow(accountIdx, chatIdx int) bool {
	if accountIdx < 0 || accountIdx >= len(m.accounts) {
		return false
	}
	full, ok := m.accounts[accountIdx].PinnedHistory[chatIdx]
	if !ok {
		return false
	}

	windowed, dropped := trimMessagesFront(full, m.maxMessagesPerChat)
	m.accounts[accountIdx].Messages[chatIdx] = windowed
	m.accounts[accountIdx].HistoryMore[chatIdx] = dropped > 0
	delete(m.accounts[accountIdx].PinnedHistory, chatIdx)
	delete(m.accounts[accountIdx].PinnedWindow, chatIdx)
	return true
}
