package ui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/lrstanley/bubblezone/v2"

	"github.com/jim-ww/kage/ui/emojipicker"
	"github.com/jim-ww/kage/ui/modal"
)

// emojiPickerBody is the reaction picker (ui/emojipicker) as a modal body:
// it submits the picked emoji ([]string) on enter or a click on a cell.
type emojiPickerBody struct {
	picker *emojipicker.Model
	zone   *zone.Manager
}

func (b *emojiPickerBody) Update(msg tea.Msg) (tea.Cmd, modal.Outcome) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if picked, ok := b.picker.DidConfirm(msg); ok {
			return nil, modal.Submit(picked)
		}
	case tea.MouseMotionMsg:
		if i, ok := b.pickedUnderMouse(msg); ok {
			b.picker.SetPickedHover(i)
		} else {
			b.picker.ClearPickedHover()
			if i, ok := b.cellUnderMouse(msg); ok {
				b.picker.SetCursor(i)
			}
		}
		return nil, modal.Continue
	case tea.MouseClickMsg:
		// Checked before the grid: a picked emoji whose grid cell has
		// scrolled out of view (query narrowed it away) is still shown in
		// the "Picked:" row, and clicking it there removes it without
		// touching the grid at all.
		if i, ok := b.pickedUnderMouse(msg); ok {
			b.picker.RemovePickedAt(i)
			return nil, modal.Continue
		}
		if i, ok := b.cellUnderMouse(msg); ok {
			return nil, modal.Submit(b.picker.ClickConfirm(i))
		}
		return nil, modal.Continue
	}
	return b.picker.Update(msg), modal.Continue
}

func (b *emojiPickerBody) View() string { return b.picker.View() }

// SetSize fits the grid: up to 10 columns and 8 rows of emoji, less the
// search and picked rows.
func (b *emojiPickerBody) SetSize(width, height int) {
	b.picker.Resize(clamp(width/emojipicker.CellWidth, 4, 10), clamp(height-2, 2, 8), width)
}

func (b *emojiPickerBody) PreferredWidth() int { return 10 * emojipicker.CellWidth }

func (b *emojiPickerBody) Bind(z *zone.Manager, id string) {
	b.zone, b.picker.Zone, b.picker.ID = z, z, id
}

func (b *emojiPickerBody) Hints() []modal.Hint {
	return []modal.Hint{
		modal.KeyHint(tea.KeyPressMsg{Code: tea.KeyEnter}, "react"),
		modal.KeyHint(tea.KeyPressMsg{Code: tea.KeyTab}, "pick several"),
		modal.KeyHint(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}, "clear"),
	}
}

// pickedUnderMouse is the index into the picker's Selection() the mouse is
// over, if it's over a picked chip - checked against the "Picked: ..."
// row's zone marks (see emojipicker.Model.PickedZoneID).
func (b *emojiPickerBody) pickedUnderMouse(msg tea.MouseMsg) (int, bool) {
	if b.zone == nil {
		return 0, false
	}
	for i := range b.picker.Selection() {
		if b.zone.Get(b.picker.PickedZoneID(i)).InBounds(msg) {
			return i, true
		}
	}
	return 0, false
}

// cellUnderMouse is the visible grid cell the mouse is over, if any.
//
// Falls back to the nearest cell in the same row (by X-center distance) when
// no zone's box strictly contains the point: many emoji (flags, ZWJ family/
// skin-tone sequences, a bare codepoint missing its U+FE0F presentation
// selector) render at a display width the terminal disagrees with versus
// what Go's width-measuring libraries computed when bubblezone recorded the
// cell's box, so a click aimed at the visibly-drawn glyph can land just
// outside its (slightly mismeasured) hitbox - intermittently, only for the
// specific glyphs affected. Snapping to the nearest same-row cell turns that
// into "always picks the intended glyph" instead of "click does nothing".
func (b *emojiPickerBody) cellUnderMouse(msg tea.MouseMsg) (int, bool) {
	if b.zone == nil {
		return 0, false
	}
	cells := b.picker.VisibleCells()
	for _, i := range cells {
		if b.zone.Get(b.picker.CellZoneID(i)).InBounds(msg) {
			return i, true
		}
	}
	y, x := msg.Mouse().Y, msg.Mouse().X
	best, bestDist := -1, 0
	for _, i := range cells {
		z := b.zone.Get(b.picker.CellZoneID(i))
		if z.IsZero() || y < z.StartY || y > z.EndY {
			continue
		}
		dist := (z.StartX+z.EndX)/2 - x
		if dist < 0 {
			dist = -dist
		}
		if best < 0 || dist < bestDist {
			best, bestDist = i, dist
		}
	}
	return best, best >= 0
}

// confirmEmojiPick records a confirmed pick's usage and sends the reaction
// to message idx.
func (m *Model) confirmEmojiPick(idx int, picked []string) tea.Cmd {
	var cmds []tea.Cmd
	if len(picked) > 0 {
		if m.reactionEmojiUsage == nil {
			m.reactionEmojiUsage = make(map[string]int, len(picked))
		}
		for _, e := range picked {
			m.reactionEmojiUsage[e]++
		}
		_ = m.daemon.RecordReactionEmojiUsage(picked)
	}
	cmds = append(cmds, m.sendReaction(idx, picked))
	m.refreshViewport()
	return tea.Batch(cmds...)
}
