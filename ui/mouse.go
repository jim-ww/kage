package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/x/ansi"
	zone "github.com/lrstanley/bubblezone/v2"
)

// Zone IDs for the mouse-clickable regions marked in View(). Panes are
// checked in order from most to least specific — e.g. a click on a chat
// row must be handled before the enclosing sidebar pane's "switch focus"
// fallback.
const (
	zonePaneSidebar           = "pane-sidebar"
	zonePaneViewport          = "pane-viewport"
	zonePaneInput             = "pane-input"
	zoneAccountBarName        = "account-bar-name"
	zoneAccountBarStatus      = "account-bar-status"
	zoneStoragePasswordButton = "storage-password-button"
	zoneChatStatusBar         = "chat-status-bar"
	zoneSendButton            = "send-button"
	zoneAttachButton          = "attach-button"
	zoneToggleSidebar         = "toggle-sidebar-button"
	zoneMsgInfoPopup          = "msg-info-popup"
	zoneCallAnswer            = "call-answer-button"
	zoneCallReject            = "call-reject-button"
	zoneCallMute              = "call-mute-button"
	zoneCallHangup            = "call-hangup-button"
)

// inputWheelScrollLines is how many lines a single wheel notch moves the
// compose box's cursor (and so its internal viewport) by.
const inputWheelScrollLines = 2

func zoneFilePickerRow(i int) string     { return fmt.Sprintf("file-picker-row-%d", i) }
func zoneAccountRow(i int) string        { return fmt.Sprintf("account-row-%d", i) }
func zoneChatItem(i int) string          { return fmt.Sprintf("chat-item-%d", i) }
func zoneMessage(i int) string           { return fmt.Sprintf("msg-%d", i) }
func zoneEmojiSuggestion(i int) string   { return fmt.Sprintf("emoji-suggest-%d", i) }
func zoneAttachmentRemove(i int) string  { return fmt.Sprintf("attachment-remove-%d", i) }
func zoneMsgInfoAttachment(i int) string { return fmt.Sprintf("msg-info-attachment-%d", i) }

// messageIndexFromZone extracts i back out of a zoneMessage(i) ID, for code
// (handleMouseMotion) that only has the zone ID from zoneUnderMouse and
// needs the index without re-scanning every message's zone bounds again.
func messageIndexFromZone(id string) (int, bool) {
	rest, ok := strings.CutPrefix(id, "msg-")
	if !ok {
		return 0, false
	}
	i, err := strconv.Atoi(rest)
	if err != nil {
		return 0, false
	}
	return i, true
}

// filePickerRowFromZone extracts i back out of a zoneFilePickerRow(i) ID,
// for handleMouseMotion to follow the mouse without re-scanning every row's
// zone bounds.
func filePickerRowFromZone(id string) (int, bool) {
	rest, ok := strings.CutPrefix(id, "file-picker-row-")
	if !ok {
		return 0, false
	}
	i, err := strconv.Atoi(rest)
	if err != nil {
		return 0, false
	}
	return i, true
}

// zoneRowContains reports whether mouse falls within z's vertical span and
// within [0, maxX) horizontally. It doesn't trust z.EndX: chat rows are
// marked inside sidebarBox, which draws a lipgloss border around the
// sidebar, and that border render doesn't treat zone.Mark's zero-width
// marker correctly — it truncates the row's content (and any padding we
// add) well short of the real right edge, so ZoneInfo.InBounds' X range
// ends up covering only the first few columns of the row. maxX (the
// sidebar's own width) is used instead, so the row is still bounded to the
// sidebar pane rather than matching anywhere on screen at that Y.
func zoneRowContains(z *zone.ZoneInfo, mouse tea.MouseMsg, maxX int) bool {
	if z.IsZero() {
		return false
	}
	m := mouse.Mouse()
	return m.Y >= z.StartY && m.Y <= z.EndY && m.X >= z.StartX && m.X < maxX
}

// hoverState holds the zone ID currently under the pointer. It's shared via
// pointer (see Model.hover) rather than copied through Model, because the
// chat list delegate needs to read it too but only ever sees list.Model,
// not the full ui.Model.
type hoverState struct {
	id string
}

// isHovered reports whether zoneID is the one currently under the pointer.
// Always false when the mouse (and therefore hover tracking) is disabled.
func (m Model) isHovered(zoneID string) bool {
	return m.hover != nil && m.hover.id == zoneID
}

// handleMouseMotion recomputes which zone is under the pointer on every
// motion event (only sent while mouseEnabled, see View's MouseModeAllMotion)
// so hoverable components (send button, chat items, account rows, messages,
// context-menu items) can highlight themselves. In viewChat, hovering a
// message also moves the message cursor to it, mirroring how the sidebar's
// list cursor already follows the mouse.
func (m Model) handleMouseMotion(msg tea.MouseMotionMsg) (tea.Model, tea.Cmd) {
	if m.resizingSidebar {
		if z := m.zone.Get(zonePaneSidebar); !z.IsZero() {
			m.sidebarWidthOverride = msg.Mouse().X - z.StartX
			m.updateSizes()
		}
		return m, nil
	}
	if m.resizingInput {
		if z := m.zone.Get(zonePaneInput); !z.IsZero() {
			// Dragging the top border up should grow the box (its bottom
			// edge stays put), so height is measured from the drag point
			// down to the box's fixed bottom edge, not from its own
			// (moving) top edge.
			m.inputHeightOverride = z.EndY - msg.Mouse().Y
			m.updateSizes()
		}
		return m, nil
	}

	if m.pickingFile {
		lines := strings.Split(m.filePicker.View(), "\n")
		row := m.filePickerRowUnderMouse(msg, lines)
		if row < 0 {
			return m, nil
		}
		cursorRow := filePickerCursorRow(lines, m.filePicker.Cursor)
		return m, filePickerMoveCmd(cursorRow, row)
	}

	m.hover.id = m.zoneUnderMouse(msg)

	if m.contextMenu == nil {
		if m.zone.Get(zonePaneSidebar).InBounds(msg) {
			// Don't steal focus away from the account manager — it renders
			// inside this same sidebar zone, so hovering an account row
			// would otherwise bounce selectedView back to viewChats and
			// the popup would look like it instantly closed.
			if m.selectedView != viewAccounts {
				m.setSelectedView(viewChats)
			}
		} else if m.zone.Get(zonePaneViewport).InBounds(msg) || m.zone.Get(zonePaneInput).InBounds(msg) {
			if m.currentChatIndex() >= 0 {
				m.setSelectedView(viewChat)
			}
		}
	}

	if m.selectedView == viewChat && m.contextMenu == nil {
		if idx, ok := messageIndexFromZone(m.hover.id); ok && idx != m.selectedMsg {
			old := m.selectedMsg
			m.selectedMsg = idx
			m.lastClickedMsgIdx = -1
			m.lastClickTime = time.Time{}
			m.refreshViewportSelection(old, idx)
		}
	}

	return m, nil
}

// zoneUnderMouse returns the most specific zone ID containing mouse, or ""
// if it's over nothing hoverable. Mirrors the zone-priority order used by
// handleLeftClick/handleRightClick, but restricted to the context menu's
// own items while one is open — nothing underneath it is reachable.
func (m Model) zoneUnderMouse(mouse tea.MouseMsg) string {
	if m.contextMenu != nil {
		for i := range m.contextMenu.items {
			if m.zone.Get(zoneContextMenuItem(i)).InBounds(mouse) {
				return zoneContextMenuItem(i)
			}
		}
		return ""
	}

	if m.showMsgInfo {
		if msgs := m.currentMessages(); m.selectedMsg >= 0 && m.selectedMsg < len(msgs) {
			for i := range msgs[m.selectedMsg].Attachments {
				if m.zone.Get(zoneMsgInfoAttachment(i)).InBounds(mouse) {
					return zoneMsgInfoAttachment(i)
				}
			}
		}
		return ""
	}

	// The call bar's own buttons take priority the same way zoneSendButton
	// etc. do — it's a fixed status-bar area, so it should win over anything
	// happening to render underneath it.
	if m.callBarActive() {
		if m.zone.Get(zoneCallAnswer).InBounds(mouse) {
			return zoneCallAnswer
		}
		if m.zone.Get(zoneCallReject).InBounds(mouse) {
			return zoneCallReject
		}
		if m.zone.Get(zoneCallMute).InBounds(mouse) {
			return zoneCallMute
		}
		if m.zone.Get(zoneCallHangup).InBounds(mouse) {
			return zoneCallHangup
		}
	}
	if m.zone.Get(zoneSendButton).InBounds(mouse) {
		return zoneSendButton
	}
	if m.zone.Get(zoneAttachButton).InBounds(mouse) {
		return zoneAttachButton
	}
	if m.zone.Get(zoneToggleSidebar).InBounds(mouse) {
		return zoneToggleSidebar
	}
	if m.zone.Get(zoneStoragePasswordButton).InBounds(mouse) {
		return zoneStoragePasswordButton
	}
	if m.zone.Get(zoneAccountBarName).InBounds(mouse) {
		return zoneAccountBarName
	}
	if m.zone.Get(zoneAccountBarStatus).InBounds(mouse) {
		return zoneAccountBarStatus
	}
	for i := range m.emojiSuggestions {
		if m.zone.Get(zoneEmojiSuggestion(i)).InBounds(mouse) {
			return zoneEmojiSuggestion(i)
		}
	}
	for i := range m.pendingAttachments {
		if m.zone.Get(zoneAttachmentRemove(i)).InBounds(mouse) {
			return zoneAttachmentRemove(i)
		}
	}
	for i := range m.accounts {
		if m.zone.Get(zoneAccountRow(i)).InBounds(mouse) {
			return zoneAccountRow(i)
		}
	}
	for i := range m.chats.Items() {
		if zoneRowContains(m.zone.Get(zoneChatItem(i)), mouse, m.sidebarWidth()) {
			return zoneChatItem(i)
		}
	}
	if m.selectedView == viewChat {
		msgs := m.currentMessages()
		for i := range msgs {
			if m.zone.Get(zoneMessage(i)).InBounds(mouse) {
				return zoneMessage(i)
			}
		}
	}
	return ""
}

// handleMouseClick routes a click to the context menu (if one is open), or
// otherwise dispatches by button: left click acts directly, right click
// opens a context menu of actions for whatever was clicked.
func (m Model) handleMouseClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if m.contextMenu != nil {
		return m.handleContextMenuClick(msg)
	}

	if m.pickingFile {
		return m.handleFilePickerClick(msg)
	}

	if m.showMsgInfo {
		if !m.zone.Get(zoneMsgInfoPopup).InBounds(msg) {
			m.showMsgInfo = false
			return m, nil
		}
		if msgs := m.currentMessages(); m.selectedMsg >= 0 && m.selectedMsg < len(msgs) {
			for i, a := range msgs[m.selectedMsg].Attachments {
				if m.zone.Get(zoneMsgInfoAttachment(i)).InBounds(msg) {
					if err := clipboard.WriteAll(a); err != nil {
						return m, m.showNotification("copy failed")
					}
					return m, m.showNotification("URL copied")
				}
			}
		}
		return m, nil
	}

	if msg.Mouse().Button == tea.MouseLeft {
		if z := m.zone.Get(zonePaneSidebar); !z.IsZero() && msg.Mouse().X == z.EndX {
			m.resizingSidebar = true
			return m, nil
		}
		if z := m.zone.Get(zonePaneInput); !z.IsZero() && msg.Mouse().Y == z.StartY {
			m.resizingInput = true
			return m, nil
		}
	}

	switch msg.Mouse().Button {
	case tea.MouseLeft:
		return m.handleLeftClick(msg)
	case tea.MouseRight:
		return m.handleRightClick(msg)
	}
	return m, nil
}

// handleMouseRelease ends a sidebar-border or compose-box-border drag
// started in handleMouseClick; the terminal only sends this once, on
// button-up. The resulting size is persisted so it's restored on the next
// launch.
func (m Model) handleMouseRelease(msg tea.MouseReleaseMsg) (tea.Model, tea.Cmd) {
	switch {
	case m.resizingSidebar:
		m.resizingSidebar = false
		if m.sidebarWidthSetter == nil {
			return m, nil
		}
		if err := m.sidebarWidthSetter.SetSidebarWidth(m.sidebarWidth()); err != nil {
			return m, m.showNotification("saving sidebar width: " + err.Error())
		}
	case m.resizingInput:
		m.resizingInput = false
		if m.inputHeightSetter == nil {
			return m, nil
		}
		if err := m.inputHeightSetter.SetInputHeight(m.inputHeightOverride); err != nil {
			return m, m.showNotification("saving compose box height: " + err.Error())
		}
	}
	return m, nil
}

// handleContextMenuClick is the only input the open context menu responds
// to: clicking one of its items runs that item's action and closes the
// menu; any other click (a different item's popup, or empty space) just
// closes it without acting — nothing under the popup is clicked "through".
// An item's run can open a submenu (e.g. the "Encryption" picker) by setting
// m.contextMenu itself — don't stomp that back to nil afterward.
func (m Model) handleContextMenuClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if msg.Mouse().Button != tea.MouseLeft {
		m.closeContextMenu()
		return m, nil
	}
	before := m.contextMenu
	for i, item := range m.contextMenu.items {
		if m.zone.Get(zoneContextMenuItem(i)).InBounds(msg) {
			cmd := item.run(&m)
			if m.contextMenu == before {
				m.closeContextMenu()
			}
			return m, cmd
		}
	}
	m.closeContextMenu()
	return m, nil
}

// handleLeftClick dispatches a left-click to whichever marked zone it
// landed in: an account row, a chat list item, a message, or one of the
// three panes (sidebar / viewport / input) when the click missed anything
// more specific inside it.
func (m Model) handleLeftClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if m.callBarActive() {
		if m.zone.Get(zoneCallAnswer).InBounds(msg) {
			return m, m.answerRingingCall()
		}
		if m.zone.Get(zoneCallReject).InBounds(msg) {
			return m, m.rejectRingingCall()
		}
		if m.zone.Get(zoneCallMute).InBounds(msg) {
			return m, m.toggleMuteCall()
		}
		if m.zone.Get(zoneCallHangup).InBounds(msg) {
			return m, m.hangupCurrentCall()
		}
	}

	if m.zone.Get(zoneToggleSidebar).InBounds(msg) {
		return m.toggleSidebar()
	}

	for i := range m.emojiSuggestions {
		if m.zone.Get(zoneEmojiSuggestion(i)).InBounds(msg) {
			m.acceptEmojiSuggestion(i)
			return m, nil
		}
	}

	for i := range m.pendingAttachments {
		if m.zone.Get(zoneAttachmentRemove(i)).InBounds(msg) {
			m.removeAttachment(i)
			m.updateSizes()
			return m, nil
		}
	}

	if m.zone.Get(zoneStoragePasswordButton).InBounds(msg) {
		m.notifyTypingStopped()
		m.lastClickedMsgIdx = -1
		m.lastClickTime = time.Time{}
		cmd := m.openChangePasswordPopup()
		return m, cmd
	}

	if m.zone.Get(zoneAccountBarStatus).InBounds(msg) {
		m.notifyTypingStopped()
		m.lastClickedMsgIdx = -1
		m.lastClickTime = time.Time{}
		return m, m.actionOpenAccountStatusMenu(m.currentAccount)
	}

	if m.zone.Get(zoneAccountBarName).InBounds(msg) {
		m.notifyTypingStopped()
		if m.selectedView == viewAccounts {
			m.setSelectedView(viewChats)
		} else {
			m.setSelectedView(viewAccounts)
		}
		m.lastClickedMsgIdx = -1
		m.lastClickTime = time.Time{}
		m.input.Blur()
		return m, nil
	}

	for i := range m.accounts {
		if m.zone.Get(zoneAccountRow(i)).InBounds(msg) {
			m.setSelectedView(viewAccounts)
			m.lastClickedMsgIdx = -1
			m.lastClickTime = time.Time{}
			return m, m.switchAccount(i)
		}
	}

	for i := range m.chats.Items() {
		if zoneRowContains(m.zone.Get(zoneChatItem(i)), msg, m.sidebarWidth()) {
			m.notifyTypingStopped()
			m.lastClickedMsgIdx = -1
			m.lastClickTime = time.Time{}
			m.selectChatItem(i)
			return m.openCurrentChat()
		}
	}

	if m.selectedView == viewChat {
		msgs := m.currentMessages()
		for i := range msgs {
			if m.zone.Get(zoneMessage(i)).InBounds(msg) {
				clickTime := time.Now()
				isDoubleClick := m.lastClickedMsgIdx == i && !m.lastClickTime.IsZero() && clickTime.Sub(m.lastClickTime) < 500*time.Millisecond

				m.selectedMsg = i
				m.refreshViewportScrollTo(i)

				if isDoubleClick {
					// Double-click: open the message
					m.lastClickedMsgIdx = -1
					m.lastClickTime = time.Time{}
					return m, m.actionOpenMessage()
				} else {
					// Single-click: reply to the message
					m.lastClickedMsgIdx = i
					m.lastClickTime = clickTime
					return m, m.actionReplyMessage()
				}
			}
		}
	}

	if m.zone.Get(zoneSendButton).InBounds(msg) {
		return m, m.sendCurrentInput()
	}

	if m.zone.Get(zoneAttachButton).InBounds(msg) {
		if m.currentChatIndex() < 0 {
			return m, m.showNotification("no chat selected")
		}
		m.pickingFile = true
		m.filePicker.SetHeight(max(1, m.height-m.inputAreaHeight()-6))
		return m, m.filePicker.Init()
	}

	if m.zone.Get(zonePaneInput).InBounds(msg) {
		// Clicking the input box should focus it even when a different pane
		// currently has focus (e.g. the sidebar) — not just when already in
		// viewChat, which left clicks on the input silently doing nothing
		// most of the time. openCurrentChat both switches to viewChat and
		// focuses the input; it's a no-op if no chat is open.
		return m.openCurrentChat()
	}

	if m.zone.Get(zonePaneViewport).InBounds(msg) {
		if m.currentChatIndex() >= 0 {
			return m.openCurrentChat()
		}
		return m, nil
	}

	if m.zone.Get(zonePaneSidebar).InBounds(msg) {
		m.notifyTypingStopped()
		m.setSelectedView(viewChats)
		m.lastClickedMsgIdx = -1
		m.lastClickTime = time.Time{}
		m.input.Blur()
		return m, nil
	}

	return m, nil
}

// handleRightClick opens a context menu of actions for whatever was
// right-clicked, first moving selection onto it exactly as the equivalent
// left-click would (see handleLeftClick) so the menu's actions — which
// read back the current selection — operate on the right thing.
func (m Model) handleRightClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	for i := range m.accounts {
		if m.zone.Get(zoneAccountRow(i)).InBounds(msg) {
			m.setSelectedView(viewAccounts)
			cmd := m.switchAccount(i)
			m.openContextMenu(m.accountRowContextMenuItems(i))
			return m, cmd
		}
	}

	for i := range m.chats.Items() {
		if zoneRowContains(m.zone.Get(zoneChatItem(i)), msg, m.sidebarWidth()) {
			m.selectChatItem(i)
			m.openContextMenu(m.chatItemContextMenuItems(i))
			return m, nil
		}
	}

	if m.selectedView == viewChat {
		msgs := m.currentMessages()
		for i := range msgs {
			if m.zone.Get(zoneMessage(i)).InBounds(msg) {
				m.selectedMsg = i
				m.refreshViewportScrollTo(i)
				m.lastClickedMsgIdx = -1
				m.lastClickTime = time.Time{}
				m.openContextMenu(m.messageContextMenuItems(i))
				return m, nil
			}
		}
	}

	return m, nil
}

// selectChatItem moves the chat-list cursor to i and keeps message
// selection/viewport in sync, without opening the chat — shared by
// handleLeftClick (which opens it right after) and handleRightClick
// (which doesn't).
func (m *Model) selectChatItem(i int) {
	m.chats.Select(i)
	m.lastClickedMsgIdx = -1
	m.lastClickTime = time.Time{}
	chatIdx := m.currentChatIndex()
	if chatIdx < 0 {
		m.selectedMsg = 0
	} else if msgs := m.currentMessages(); len(msgs) > 0 {
		m.selectedMsg = len(msgs) - 1
	} else {
		m.selectedMsg = 0
	}
	m.refreshViewport()
	m.viewport.GotoBottom()
}

// filePickerCursorRow scans the file picker's rendered lines (one per
// zoneFilePickerRow) and returns the row currently holding the cursor glyph,
// or -1 if none does (e.g. an empty directory). The picker's selected index
// isn't exported, so this is the only way to locate it from the outside.
func filePickerCursorRow(lines []string, cursor string) int {
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimLeft(ansi.Strip(line), " "), cursor) {
			return i
		}
	}
	return -1
}

// filePickerRowUnderMouse returns the file-picker row the mouse is over, or
// -1 if it isn't over any row.
func (m Model) filePickerRowUnderMouse(msg tea.MouseMsg, lines []string) int {
	for i := range lines {
		if m.zone.Get(zoneFilePickerRow(i)).InBounds(msg) {
			return i
		}
	}
	return -1
}

// filePickerMoveCmd walks the picker's cursor from cursorRow to targetRow
// via synthetic up/down key presses — the picker's selection index isn't
// exported, so this is the only way to move it to an arbitrary row from
// outside. Returned as a tea.Sequence so the presses are applied one at a
// time, in order, through the normal Update loop rather than by mutating
// m.filePicker directly here.
func filePickerMoveCmd(cursorRow, targetRow int) tea.Cmd {
	if cursorRow < 0 || cursorRow == targetRow {
		return nil
	}
	step := tea.KeyPressMsg{Code: tea.KeyDown}
	if targetRow < cursorRow {
		step = tea.KeyPressMsg{Code: tea.KeyUp}
	}
	var cmds []tea.Cmd
	for range max(targetRow, cursorRow) - min(targetRow, cursorRow) {
		cmds = append(cmds, func() tea.Msg { return step })
	}
	return tea.Sequence(cmds...)
}

// handleFilePickerClick turns a click on a file-picker row into the cursor
// moves needed to select it (see filePickerMoveCmd), applied through the
// normal pickingFile key interception in update_keys.go (which already
// knows how to stage a selected file) rather than duplicating that logic
// here. A second click on the already-selected row within the double-click
// window also opens it. In practice the cursor is usually already on the
// clicked row by the time this fires, since hovering it (handleMouseMotion)
// moves the cursor there first.
func (m Model) handleFilePickerClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if msg.Mouse().Button != tea.MouseLeft {
		return m, nil
	}

	lines := strings.Split(m.filePicker.View(), "\n")
	targetRow := m.filePickerRowUnderMouse(msg, lines)
	if targetRow < 0 {
		return m, nil
	}

	cursorRow := filePickerCursorRow(lines, m.filePicker.Cursor)

	clickTime := time.Now()
	isDoubleClick := m.lastFilePickerRow == targetRow && !m.lastFilePickerTime.IsZero() && clickTime.Sub(m.lastFilePickerTime) < 500*time.Millisecond
	m.lastFilePickerRow = targetRow
	m.lastFilePickerTime = clickTime

	cmd := filePickerMoveCmd(cursorRow, targetRow)
	if isDoubleClick {
		cmd = tea.Sequence(cmd, func() tea.Msg { return tea.KeyPressMsg{Code: tea.KeyEnter} })
	}
	return m, cmd
}

// handleMouseWheel scrolls whichever pane the wheel event landed over: the
// chat list in the sidebar, or the message viewport. Swallowed entirely
// while a context menu is open, so it can't scroll what's underneath it.
func (m Model) handleMouseWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	if m.contextMenu != nil {
		return m, nil
	}

	mouse := msg.Mouse()

	if m.pickingFile {
		key := tea.KeyPressMsg{Code: tea.KeyDown}
		if mouse.Button == tea.MouseWheelUp {
			key = tea.KeyPressMsg{Code: tea.KeyUp}
		}
		var cmd tea.Cmd
		m.filePicker, cmd = m.filePicker.Update(key)
		return m, cmd
	}

	if m.zone.Get(zonePaneSidebar).InBounds(msg) {
		switch mouse.Button {
		case tea.MouseWheelUp:
			m.chats.CursorUp()
		case tea.MouseWheelDown:
			m.chats.CursorDown()
		default:
			return m, nil
		}
		m.selectChatItem(m.currentChatIndex())
		return m, nil
	}

	if m.zone.Get(zonePaneViewport).InBounds(msg) {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		if len(m.msgOffsets) > 0 {
			m.selectedMsg = m.msgIndexAtOffset(m.viewport.YOffset())
			m.refreshViewport()
			m.viewport.SetYOffset(m.viewport.YOffset())
		}
		if m.viewport.YOffset() == 0 {
			cmd = tea.Batch(cmd, m.maybeLoadOlderHistory())
		}
		return m, cmd
	}

	if m.zone.Get(zonePaneInput).InBounds(msg) {
		// The textarea has no public "scroll by N" — moving the cursor is
		// the only way to shift its internal viewport, but that's exactly
		// what a wheel notch should do here anyway. A no-op when there's
		// nothing to scroll past (content fits within the box's height).
		// inputWheelScrollLines per notch, matching the viewport pane's feel
		// (one line per CursorUp/Down reads as sluggish for a wheel notch).
		for range inputWheelScrollLines {
			switch mouse.Button {
			case tea.MouseWheelUp:
				m.input.CursorUp()
			case tea.MouseWheelDown:
				m.input.CursorDown()
			}
		}
		return m, nil
	}

	return m, nil
}
