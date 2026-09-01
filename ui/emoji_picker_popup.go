package ui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// updateEmojiPickerKey routes a key to the open emoji picker popup: Confirm
// sends the picked reaction set and closes it, Cancel just closes it,
// anything else is forwarded to the picker's own Update (grid nav, typing
// into its filter box, toggling a cell) — mirrors ui/filepicker's
// DidSelectFile convention (re-check the same msg right after Update).
func (m Model) updateEmojiPickerKey(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	if picked, ok := m.emojiPicker.DidConfirm(msg); ok {
		model, cmd := m.confirmEmojiPick(picked)
		return model, cmd, true
	}
	if m.emojiPicker.DidCancel(msg) {
		m.emojiPicker = nil
		m.reactingMsgIdx = -1
		return m, nil, true
	}
	var cmd tea.Cmd
	*m.emojiPicker, cmd = m.emojiPicker.Update(msg)
	return m, cmd, true
}

// handleEmojiPickerClick hit-tests msg against the currently-rendered grid
// cells' zone marks (see emojipicker.Model's Zone/CellZoneID/VisibleCells)
// and, on a hit, confirms exactly as if that cell had been reached via the
// keyboard and Confirm pressed - see confirmEmojiPick.
func (m Model) handleEmojiPickerClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if m.emojiPicker.Zone == nil {
		return m, nil
	}
	// Checked before the grid: a picked emoji whose grid cell has scrolled
	// out of view (query narrowed it away) is still shown in the "Picked:"
	// row, and clicking it there removes it without touching the grid at
	// all - doesn't confirm/close the picker, unlike a grid-cell click.
	for i := range m.emojiPicker.Selection() {
		if m.zone.Get(m.emojiPicker.PickedZoneID(i)).InBounds(msg) {
			m.emojiPicker.RemovePickedAt(i)
			return m, nil
		}
	}
	// emojiPickerCellUnderMouse (not a plain InBounds loop) so a click that
	// lands just outside a mismeasured emoji's hitbox still snaps to it -
	// see that function's doc for why that happens.
	if i, ok := m.emojiPickerCellUnderMouse(msg); ok {
		return m.confirmEmojiPick(m.emojiPicker.ClickConfirm(i))
	}
	return m, nil
}

// confirmEmojiPick is the shared tail of a confirmed emoji pick (via enter
// or a click): record usage, send the reaction, and close the picker.
// Shared so the two input paths can't drift.
func (m Model) confirmEmojiPick(picked []string) (Model, tea.Cmd) {
	var cmds []tea.Cmd
	if len(picked) > 0 {
		if m.reactionEmojiUsage == nil {
			m.reactionEmojiUsage = make(map[string]int, len(picked))
		}
		for _, e := range picked {
			m.reactionEmojiUsage[e]++
		}
		if m.reactionEmojiUsageRecorder != nil {
			_ = m.reactionEmojiUsageRecorder.RecordReactionEmojiUsage(picked)
		}
	}
	cmds = append(cmds, m.sendReaction(m.reactingMsgIdx, picked))
	m.emojiPicker = nil
	m.reactingMsgIdx = -1
	m.refreshViewport()
	return m, tea.Batch(cmds...)
}

// renderEmojiPickerPopup composites the emoji picker over the chat area,
// centered the same way every other popup is (see renderContactManagerPopup
// for the pattern this mirrors).
func (m Model) renderEmojiPickerPopup() string {
	cw := m.chatAreaWidth()
	vh := m.height - m.inputAreaHeight()

	popup := m.styles.popupDialog(m.styles.colors.borderA, m.emojiPicker.View())
	popup = m.zone.Mark(zoneEmojiPickerPopup, popup)
	return lipgloss.Place(cw, vh, lipgloss.Center, lipgloss.Center, popup)
}

// popupBorderWidth/popupBorderHeight are the chrome the "popup" style (see
// styles.go) always adds around its content - Border(RoundedBorder()) plus
// Padding(1, 4) - subtracted from the available chat-area space to get the
// emoji picker's actual content budget.
const (
	popupChromeWidth  = 2 + 4*2 // border (1 each side) + horizontal padding (4 each side)
	popupChromeHeight = 2 + 1*2 // border (1 each side) + vertical padding (1 each side)
)

// emojiPickerCellWidth is a conservative per-cell column budget: Padding(0,1)
// (2) plus a bracketed "[emoji]" (up to 4, since most emoji render 2 columns
// wide) - sized for the widest a cell ever gets (picked), not the common
// case, so picked cells can never push a row wider than its column was
// sized for.
const emojiPickerCellWidth = 6

// emojiPickerChromeLines is everything in the picker's View() besides the
// grid itself: title + blank, query + blank, picked-summary + blank, a
// blank before the footer, the footer's main hint line, and a reserved line
// for the "rows X-Y of Z" scroll indicator (only sometimes shown, but
// reserving it either way keeps sizing math simple and on the safe side).
const emojiPickerChromeLines = 9

// sizeEmojiPicker recomputes the open emoji picker's Columns/VisibleRows to
// fit the current chat-area popup space instead of a fixed size that can
// overflow a small terminal - called both when the picker first opens and
// on every terminal resize (see the tea.WindowSizeMsg case).
func (m *Model) sizeEmojiPicker() {
	if m.emojiPicker == nil {
		return
	}
	innerWidth := max(1, m.chatAreaWidth()-popupChromeWidth)
	innerHeight := max(1, m.height-m.inputAreaHeight()-popupChromeHeight)

	columns := clamp(innerWidth/emojiPickerCellWidth, 4, 10)
	rows := clamp(innerHeight-emojiPickerChromeLines, 2, 8)
	m.emojiPicker.Resize(columns, rows, innerWidth)
}
