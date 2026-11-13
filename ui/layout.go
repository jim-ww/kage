package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// setSelectedView switches the focused view; always through here, as the
// layout depends on it and the compose box's focus must follow it.
func (m *Model) setSelectedView(v selectedView) {
	m.selectedView = v
	if v == viewChat {
		m.input.Focus()
	} else {
		m.input.Blur()
	}
	m.updateSizes()
}

func (m *Model) updateSizes() {
	fl := footerLineCount(m.keys.helpHint(m.selectedView, len(m.pendingAttachments) > 0), max(1, m.width-2), footerMaxLines)
	callBar := 0
	if m.callBarActive() {
		callBar = callBarHeight
	}
	m.height = max(0, m.termHeight-fl-footerMarginTop-callBar)

	// The dragged height is the textarea's floor, set before SetWidth
	// recomputes its height.
	if m.inputHeightOverride > 0 {
		h := clamp(m.inputHeightOverride, 1, m.inputHeightMaxDrag())
		m.inputHeightOverride = h
		m.input.MinHeight = h
		m.input.MaxHeight = max(inputMaxHeight, h)
	} else {
		m.input.MinHeight = 1
		m.input.MaxHeight = inputMaxHeight
	}

	cw := m.chatAreaWidth()
	m.input.SetWidth(m.inputFieldWidth())
	ih := m.inputAreaHeight()

	m.chats.SetHeight(max(0, m.height-sidebarStatusHeight))
	m.chats.SetWidth(m.sidebarContentWidth())

	m.viewport.SetWidth(cw)
	m.viewport.SetHeight(max(0, m.height-ih-chatStatusHeight))
	m.modals.SetSize(m.chatAreaWidth(), m.popupAreaHeight())
}

// buttonGap is the blank column separating the attach and send buttons —
// otherwise they render flush against each other, which reads as one
// double-wide button rather than two separate targets.
const buttonGap = 1

// inputFieldWidth is the text field's width, beside the box's padding and
// buttons, for sizing it and laying out its row alike.
func (m Model) inputFieldWidth() int {
	w := m.chatAreaWidth() - 2 // -2 for Padding(0,1) on the input box
	if m.mouseEnabled {
		w -= attachButtonWidth(m.icons) + buttonGap + sendButtonWidth
	}
	return max(0, w)
}

const sidebarMinWidth = 20

// narrowWidth is the width below which the layout is one pane, the list or
// the chat.
const narrowWidth = 60

func (m Model) narrow() bool { return m.width < narrowWidth }

// narrowShowChat reports, in narrow mode, whether the single visible pane
// should be the chat (true) or the chat list (false). Meaningless outside
// narrow mode, where both panes can be shown together.
func (m Model) narrowShowChat() bool {
	return m.selectedView == viewChat || m.modals.Open()
}

// sidebarPeeking reports whether a hidden sidebar shows anyway because the
// list or accounts is focused, without unhiding it.
func (m Model) sidebarPeeking() bool {
	if m.narrow() || !m.sidebarHidden {
		return false
	}
	return m.selectedView == viewChats || m.selectedView == viewAccounts
}

// sidebarMaxWidth caps how wide a user-drag can push the sidebar, leaving
// at least this much room for the chat area.
func (m Model) sidebarMaxWidth() int {
	return max(sidebarMinWidth, m.width-20)
}

func (m Model) sidebarWidth() int {
	if m.narrow() {
		if m.narrowShowChat() {
			return 0
		}
		return m.width
	}
	if m.sidebarHidden && !m.sidebarPeeking() {
		return 0
	}
	if m.sidebarWidthOverride > 0 {
		return min(max(m.sidebarWidthOverride, sidebarMinWidth), m.sidebarMaxWidth())
	}
	const maxWidth = 36
	w := m.width / 4
	if w < sidebarMinWidth {
		w = sidebarMinWidth
	}
	if w > maxWidth {
		w = maxWidth
	}
	return min(w, m.width)
}

// toggleSidebar shows or hides the chat list, saving it; when narrow, it
// switches the pane shown, not saved.
func (m Model) toggleSidebar() (Model, tea.Cmd) {
	if m.narrow() {
		if m.selectedView == viewChat {
			m.setSelectedView(viewChats)
			return m, nil
		}
		model, cmd := m.openCurrentChat()
		return model.(Model), cmd
	}
	m.sidebarHidden = !m.sidebarHidden
	// The chat list's width is only set there.
	m.updateSizes()
	if err := m.daemon.SaveState(SidebarHidden(m.sidebarHidden)); err != nil {
		return m, m.showNotification("saving chat list visibility: " + err.Error())
	}
	return m, nil
}

func (m Model) chatAreaWidth() int {
	if m.narrow() {
		if m.narrowShowChat() {
			return m.width
		}
		return 0
	}
	if m.sidebarHidden && !m.sidebarPeeking() {
		return m.width
	}
	// sidebarWidth includes its border.
	return m.width - m.sidebarWidth()
}

// popupAreaHeight is the height of the chat area a popup replaces the
// message viewport in: everything between the chat status line and the
// compose box.
func (m Model) popupAreaHeight() int {
	return max(0, m.height-m.inputAreaHeight()-chatStatusHeight)
}

// sidebarContentWidth is the sidebar's width inside its border; wider
// content would wrap every line.
func (m Model) sidebarContentWidth() int { return max(0, m.sidebarWidth()-1) }

// inputMaxHeight is how tall the compose box grows before scrolling; a drag
// goes up to inputHeightMaxDrag.
const inputMaxHeight = 6

// composeMaxContentHeight caps the compose box's lines; unset, the textarea
// refuses newlines past MaxHeight.
const composeMaxContentHeight = 500

// inputPrompt is the compose box's textarea.Prompt, shared with the
// click-to-position-cursor math in mouse.go (positionInputCursorAt) which
// needs to know how many screen columns the prompt occupies on every line.
const inputPrompt = "› "

// clamp restricts v to [lo, hi]. hi < lo (a degenerate/negative available
// space) collapses to lo, same as min/max composed the naive way would.
func clamp(v, lo, hi int) int {
	return max(lo, min(v, max(lo, hi)))
}

// inputHeightMaxDrag caps how tall a user can drag the compose box's top
// border — leaves at least a couple of rows for the viewport above it so
// the chat pane never disappears entirely behind the input.
func (m Model) inputHeightMaxDrag() int {
	return max(1, m.height-chatStatusHeight-3)
}

// expandedComposeHeight is about half the chat pane, up to
// inputHeightMaxDrag.
func (m Model) expandedComposeHeight() int {
	return clamp(m.height/2-1, 1, m.inputHeightMaxDrag())
}

// toggleComposeExpand grows the compose box to expandedComposeHeight, or
// back to its height before; not saved.
func (m Model) toggleComposeExpand() Model {
	if m.composeExpanded {
		m.inputHeightOverride = m.composeHeightBeforeExpand
		m.composeExpanded = false
	} else {
		m.composeHeightBeforeExpand = m.inputHeightOverride
		m.inputHeightOverride = m.expandedComposeHeight()
		m.composeExpanded = true
	}
	m.updateSizes()
	return m
}

// composeMultiline reports whether the text spans several rows, wrapped or
// not, for up/down to move in it. Not Height(): a dragged box is taller.
func (m Model) composeMultiline() bool {
	return m.selectedView == viewChat &&
		(m.input.LineCount() > 1 || m.input.LineInfo().Height > 1)
}

// fixStuckComposeCursorDown works around bubbles v2.1.0's CursorDown never
// leaving a wrapped row that exactly fills the width without spaces: it
// clamps one rune short. A cursor at len(line), on the row boundary, is
// reported on the next row.
func (m *Model) fixStuckComposeCursorDown() {
	lines := strings.Split(m.input.Value(), "\n")
	row := m.input.Line()
	if row < 0 || row >= len(lines) {
		return
	}
	m.input.SetCursorColumn(len([]rune(lines[row])))
}

// inputAreaHeight is the reply hint line, if any, and the compose box's rows.
func (m Model) inputAreaHeight() int {
	h := 1 + m.input.Height() // top border + input rows
	if !m.replyTo.empty() {
		h++ // hint line
	}
	if len(m.pendingAttachments) > 0 {
		h++ // staged-attachments row
	}
	return h
}
