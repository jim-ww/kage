package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// setSelectedView switches the focused view and resizes accordingly — the
// footer's row count (and so m.height) is view-dependent, so every focus
// change must go through this instead of a bare assignment. It also keeps
// the compose textarea's own focus flag in lockstep with the view: leaving
// it to callers to separately call m.input.Focus()/Blur() let the two drift
// apart (e.g. a mouse hover flipping selectedView back to viewChat without
// re-focusing the blurred textarea, so keystrokes got routed to it but
// silently dropped).
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

// buttonGap is the blank column separating the attach and send buttons —
// otherwise they render flush against each other, which reads as one
// double-wide button rather than two separate targets.
const buttonGap = 1

// inputFieldWidth is the text field's own width — chatAreaWidth minus the
// input box's Padding(0,1) border and, when the send button is drawn,
// the room it (and the attach button, plus the gap between them) needs
// beside the field. Used both to size the actual textinput (here) and to
// lay out its rendered row in View() — kept in one place so those two can't
// drift out of sync and misalign the cursor.
func (m Model) inputFieldWidth() int {
	w := m.chatAreaWidth() - 2 // -2 for Padding(0,1) on the input box
	if m.mouseEnabled {
		w -= attachButtonWidth(m.icons) + buttonGap + sendButtonWidth
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
	return m.contextMenu != nil || m.confirmTarget != confirmNone || m.showMsgInfo || m.showHelp ||
		m.addingAccount || m.renamingChat || m.savingAs || len(m.openItems) > 0 || m.pickingFile || m.deviceList != nil ||
		m.searchingChat || m.searchResults != nil
}

// narrowShowChat reports, in narrow mode, whether the single visible pane
// should be the chat (true) or the chat list (false). Meaningless outside
// narrow mode, where both panes can be shown together.
func (m Model) narrowShowChat() bool {
	return m.selectedView == viewChat || m.popupActive()
}

// sidebarPeeking reports whether, at normal (non-narrow) width, a
// manually-hidden sidebar should render anyway as an ordinary left-hand
// panel alongside the chat pane — i.e. the user pressed FocusChats/Back/Tab
// to look at the list or accounts panel without leaving the sidebar
// permanently un-hidden. It only "peeks" while one of those is actually
// focused; opening a chat (setSelectedView(viewChat)) drops back to fully
// hidden, matching the persisted sidebarHidden setting, without touching
// that setting itself.
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
	if m.sidebarHidden && !m.sidebarPeeking() {
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

// composeMaxContentHeight is textarea.Model.MaxContentHeight: the cap on
// total *visual* (soft-wrap-aware) lines before further input is refused,
// decoupled from inputMaxHeight (the visible viewport height, which the box
// scrolls past instead of blocking). Left at 0 (unset), the textarea falls
// back to blocking once the *logical* line count hits MaxHeight — i.e.
// alt+enter stops working after inputMaxHeight manual newlines, even though
// a single long line keeps soft-wrapping and scrolling forever. This just
// needs to be generous enough that no real chat message hits it.
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

// expandedComposeHeight is the compose box's height while
// composeExpanded — roughly half the chat pane, minus a little so the
// viewport above it never fully disappears, clamped to inputHeightMaxDrag
// like an ordinary drag would be.
func (m Model) expandedComposeHeight() int {
	return clamp(m.height/2-1, 1, m.inputHeightMaxDrag())
}

// toggleComposeExpand is bound to ToggleComposeExpand (ctrl+`): a quick way
// to grow the compose box to expandedComposeHeight for pasting/editing a
// longer message, and shrink it back on a second press. It reuses
// inputHeightOverride (the same field a mouse-drag on the box's top border
// sets) but, unlike a drag, never calls inputHeightSetter.SetInputHeight —
// this is a transient view state, not a persisted preference, so
// composeHeightBeforeExpand remembers whatever override (if any) was in
// place before expanding rather than always reverting to the default.
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

// composeMultiline reports whether the compose box's content actually spans
// more than one visual row — used to let the up/down arrows move the
// textarea's cursor instead of the selected message while there's more than
// one row to move between (see the MsgUp/MsgDown cases in update_keys.go).
// Checks LineCount() (logical, newline-delimited lines) and the current
// line's wrap height rather than Height() (the textarea's rendered viewport
// size): Height() also reflects MinHeight from a user-dragged-open compose
// box (see updateSizes), so a single short/empty line would otherwise read
// as "multiline" just because the box was expanded, wrongly swallowing
// arrow keys that should navigate messages. A single long line that's
// word-wrapped to several rows still reads as multiline on screen and
// should traverse the same way, even though it's one "line" internally —
// LineInfo().Height catches that case.
func (m Model) composeMultiline() bool {
	return m.selectedView == viewChat &&
		(m.input.LineCount() > 1 || m.input.LineInfo().Height > 1)
}

// fixStuckComposeCursorDown works around a textarea.Model bug (bubbles
// v2.1.0's setCursorLineRelative, used by CursorDown/the "down" key): when
// the current wrapped row's content exactly fills the field width with no
// natural trailing space of its own — e.g. one long space-free run of
// characters, as in a pasted token/URL/keysmash — its internal
// `len(line)-1` boundary clamp lands one rune short of the row's true end,
// so the LineInfo it recomputes from there still reports the *current* row,
// and `m.col = nli.StartColumn` then resets the column right back to where
// it started. Every subsequent Down press recomputes identically from that
// same stuck (line, column), so no number of repeats ever breaks out —
// verified via a standalone reproduction against the vendored library
// itself, not just kage's use of it.
//
// The fix: position the cursor at exactly len(line) (not len(line)-1) on
// the current logical line. LineInfo has a special case for a column
// landing precisely on a wrapped row's boundary — it reports the *next*
// row instead of clamping back into the current one — so this reaches the
// row CursorDown was trying (and failing) to reach, without needing to
// patch the vendored function itself.
func (m *Model) fixStuckComposeCursorDown() {
	lines := strings.Split(m.input.Value(), "\n")
	row := m.input.Line()
	if row < 0 || row >= len(lines) {
		return
	}
	m.input.SetCursorColumn(len([]rune(lines[row])))
}

// inputAreaHeight accounts for the optional reply-hint line plus however
// many rows the compose textarea currently occupies (it grows with
// multi-line content, up to inputMaxHeight). Reacting no longer adds a hint
// line here — it's a popup now (see renderEmojiPickerPopup).
func (m Model) inputAreaHeight() int {
	h := 1 + m.input.Height() // top border + input rows
	if m.replyToIdx >= 0 {
		h++ // hint line
	}
	if len(m.pendingAttachments) > 0 {
		h++ // staged-attachments row
	}
	return h
}
