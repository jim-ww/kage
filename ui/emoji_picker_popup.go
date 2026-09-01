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
		return m, tea.Batch(cmds...), true
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
