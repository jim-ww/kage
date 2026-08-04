package ui

import tea "charm.land/bubbletea/v2"

// setSelectedView switches the focused view and resizes accordingly — the
// footer's row count (and so m.height) is view-dependent, so every focus
// change must go through this instead of a bare assignment.
func (m *Model) setSelectedView(v selectedView) {
	m.selectedView = v
	m.updateSizes()
}

func (m *Model) updateSizes() {
	fl := footerLineCount(m.keys.helpHint(m.selectedView), max(1, m.width-2), footerMaxLines)
	m.height = max(0, m.termHeight-fl-footerMarginTop)

	// Apply any user-dragged compose height (see zonePaneInput in
	// ui/mouse.go) as the textarea's floor before SetWidth below triggers
	// its DynamicHeight recalculation — Height() must already reflect the
	// override by the time inputAreaHeight() reads it a few lines down.
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
}

// inputFieldWidth is the text field's own width — chatAreaWidth minus the
// input box's Padding(0,1) border and, when the send button is drawn,
// the room it needs beside the field. Used both to size the actual
// textinput (here) and to lay out its rendered row in View() — kept in one
// place so those two can't drift out of sync and misalign the cursor.
func (m Model) inputFieldWidth() int {
	w := m.chatAreaWidth() - 2 // -2 for Padding(0,1) on the input box
	if m.mouseEnabled {
		w -= sendButtonWidth // room for the send button beside it
	}
	return max(0, w)
}

const sidebarMinWidth = 20

// narrowWidth is the terminal width below which there isn't room to show the
// chat list and a chat side by side at all — below it the layout collapses
// to a single full-width pane (list or chat), switched with ToggleSidebar /
// opening a chat, like a phone-sized single-column view. Unlike sidebarHidden
// this is never persisted: it's a property of the current terminal size, not
// a user preference, and stops applying the moment the window is widened.
const narrowWidth = 60

func (m Model) narrow() bool { return m.width < narrowWidth }

// popupActive reports whether a popup/form is on screen that renders inside
// the chat area regardless of which pane is logically focused (context menu,
// delete/info dialogs, add-account/rename-chat forms, the open-item or file
// picker). In narrow mode these need the full-width chat pane to render
// into even while the chat list is the logically selected view.
func (m Model) popupActive() bool {
	return m.contextMenu != nil || m.confirmTarget != confirmNone || m.showMsgInfo ||
		m.addingAccount || m.renamingChat || len(m.openItems) > 0 || m.pickingFile || m.deviceList != nil
}

// narrowShowChat reports, in narrow mode, whether the single visible pane
// should be the chat (true) or the chat list (false). Meaningless outside
// narrow mode, where both panes are shown together.
func (m Model) narrowShowChat() bool {
	return m.selectedView == viewChat || m.popupActive()
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
	if m.sidebarHidden {
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

// toggleSidebar is bound to ToggleSidebar (Ctrl+\) and the chat-status-bar
// button. In narrow mode it just flips which single pane is shown (chat vs.
// list) — an ephemeral, unpersisted choice driven by the current terminal
// size, mirroring how opening a chat already replaces the list. At normal
// width it flips sidebarHidden and persists the choice (if a
// SidebarHiddenSetter is wired up) so it survives across launches like the
// dragged sidebar width does — see SidebarHiddenSetter.
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
	// The chat list's own width (m.chats.SetWidth, set from
	// sidebarContentWidth) is only recomputed in updateSizes — without this
	// call it stays stuck at whatever it last was (possibly 0, if hidden
	// while some other setSelectedView call ran updateSizes in between),
	// leaving the list rendering empty even though its selection still moves.
	m.updateSizes()
	if m.sidebarHiddenSetter == nil {
		return m, nil
	}
	if err := m.sidebarHiddenSetter.SetSidebarHidden(m.sidebarHidden); err != nil {
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
	if m.sidebarHidden {
		return m.width
	}
	return m.width - m.sidebarWidth() - 1
}

// sidebarContentWidth is how wide content rendered *inside* the sidebar box
// (the chat list, the account bar, the accounts list) may be — sidebarWidth
// minus 1 for the box's own right border. lipgloss word-wraps (rather than
// truncates) content that exceeds a Style's Width, so content sized to the
// full sidebarWidth would overflow the bordered box by exactly that one
// column and wrap every single line.
func (m Model) sidebarContentWidth() int { return max(0, m.sidebarWidth()-1) }

// inputMaxHeight caps how many rows the compose box (a DynamicHeight
// textarea) auto-grows to before it starts scrolling internally instead of
// pushing the viewport further up. A user drag (inputHeightOverride) can
// still push it taller than this, up to inputHeightMaxDrag.
const inputMaxHeight = 6

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

// composeMultiline reports whether the compose box currently renders on more
// than one row — used to let the up/down arrows move the textarea's cursor
// instead of the selected message while there's more than one row to move
// between (see the MsgUp/MsgDown cases in update_keys.go). Deliberately
// checks Height() (visual rows, DynamicHeight-recalculated from soft-wrap)
// rather than LineCount() (logical, newline-delimited lines): a single long
// line that's word-wrapped to several rows reads as multiline on screen and
// should traverse the same way, even though it's one "line" internally.
func (m Model) composeMultiline() bool {
	return m.selectedView == viewChat && m.input.Height() > 1
}

// inputAreaHeight accounts for the optional reply-hint / reacting-hint line
// plus however many rows the compose textarea currently occupies (it grows
// with multi-line content, up to inputMaxHeight).
func (m Model) inputAreaHeight() int {
	h := 1 + m.input.Height() // top border + input rows
	if m.replyToIdx >= 0 || m.reactingMsgIdx >= 0 {
		h++ // hint line
	}
	return h
}
