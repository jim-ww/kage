package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	zone "github.com/lrstanley/bubblezone/v2"
)

// Zone IDs of the clickable regions marked in View(), checked most specific
// first: a chat row before the sidebar pane around it.
const (
	zonePaneSidebar           = "pane-sidebar"
	zonePaneViewport          = "pane-viewport"
	zonePaneInput             = "pane-input"
	zoneInputTextarea         = "input-textarea"
	zoneAccountBarName        = "account-bar-name"
	zoneAccountBarStatus      = "account-bar-status"
	zoneAccountMenuButton     = "account-menu-button"
	zoneStoragePasswordButton = "storage-password-button"
	zoneChatStatusBar         = "chat-status-bar"
	zoneSendButton            = "send-button"
	zoneAttachButton          = "attach-button"
	zoneReplyHintCancel       = "reply-hint-cancel"
	zoneToggleSidebar         = "toggle-sidebar-button"
	zoneCallAnswer            = "call-answer-button"
	zoneCallReject            = "call-reject-button"
	zoneCallMute              = "call-mute-button"
	zoneCallHangup            = "call-hangup-button"
	zoneCallMenu              = "call-menu-button"
	zoneCallStopScreen        = "call-stop-screen-button"
	zoneCallStopCamera        = "call-stop-camera-button"
	zoneCallDesktopAudio      = "call-desktop-audio-button"
	zoneJumpToBottom          = "jump-to-bottom-button"
	zoneOpenPickerPopup       = "open-picker-popup"
)

// inputWheelScrollLines is how many lines a single wheel notch moves the
// compose box's cursor (and so its internal viewport) by.
const inputWheelScrollLines = 2

func zoneAccountRow(i int) string         { return fmt.Sprintf("account-row-%d", i) }
func zoneChatItem(i int) string           { return fmt.Sprintf("chat-item-%d", i) }
func zoneMessage(i int) string            { return fmt.Sprintf("msg-%d", i) }
func zoneMessageReply(i int) string       { return fmt.Sprintf("msg-reply-%d", i) }
func zoneMessageReplyBtn(i int) string    { return fmt.Sprintf("msg-reply-btn-%d", i) }
func zoneMessageReactBtn(i int) string    { return fmt.Sprintf("msg-react-btn-%d", i) }
func zoneMessageReplyKey(i int) string    { return fmt.Sprintf("msg-reply-key-%d", i) }
func zoneMessageReactKey(i int) string    { return fmt.Sprintf("msg-react-key-%d", i) }
func zoneMessageExpand(i int) string      { return fmt.Sprintf("msg-expand-%d", i) }
func zoneMessageReaction(i, j int) string { return fmt.Sprintf("msg-reaction-%d-%d", i, j) }
func zoneAttachmentRemove(i int) string   { return fmt.Sprintf("attachment-remove-%d", i) }

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

// messageIndexAtMouse maps a mouse position to a message by the viewport's
// offset and m.msgOffsets, not bubblezone's message zones: a zone whose end
// marker is scrolled out of view keeps stale bounds.
func (m Model) messageIndexAtMouse(mouse tea.MouseMsg) (int, bool) {
	if len(m.msgOffsets) == 0 {
		return 0, false
	}
	vp := m.zone.Get(zonePaneViewport)
	if vp.IsZero() || !vp.InBounds(mouse) {
		return 0, false
	}
	line := m.viewport.YOffset() + (mouse.Mouse().Y - vp.StartY)
	if line < 0 || line >= len(m.viewportLines) {
		return 0, false
	}
	idx := m.msgIndexAtOffset(line)
	if idx >= len(m.currentMessages()) {
		return 0, false
	}
	return idx, true
}

// chatIndexFromZone extracts i back out of a zoneChatItem(i) ID, for
// handleMouseMotion to tell whether the newly hovered zone is a chat row
// without re-scanning the list.
func chatIndexFromZone(id string) (int, bool) {
	rest, ok := strings.CutPrefix(id, "chat-item-")
	if !ok {
		return 0, false
	}
	i, err := strconv.Atoi(rest)
	if err != nil {
		return 0, false
	}
	return i, true
}

// zoneRowContains reports whether mouse is within z's rows and [0, maxX).
// z.EndX can't be trusted: the sidebar's lipgloss border miscounts zone
// markers and cuts the row's zone short.
func zoneRowContains(z *zone.ZoneInfo, mouse tea.MouseMsg, maxX int) bool {
	if z.IsZero() {
		return false
	}
	m := mouse.Mouse()
	return m.Y >= z.StartY && m.Y <= z.EndY && m.X >= z.StartX && m.X < maxX
}

// hoverState is what's under the pointer, see Model.hover.
type hoverState struct {
	id string

	// x, y are the pointer's position, to tell which part of a hovered
	// message row (expand button, key glyphs, reaction chip) it's over.
	x, y int

	// replyKeyIdx is the message whose "^r" glyph is drawn hovered, or -1,
	// so moving within a selected row re-renders it when that changes.
	replyKeyIdx int

	// reactKeyIdx is replyKeyIdx's counterpart for the "^t" key glyph.
	reactKeyIdx int

	// expandBtnIdx is replyKeyIdx's counterpart for the "show more"/"show
	// less" toggle.
	expandBtnIdx int

	// reactionMsgIdx/reactionIdx are the reaction chip drawn hovered, as
	// replyKeyIdx; reactionMsgIdx -1 for none.
	reactionMsgIdx int
	reactionIdx    int

	// devicesID is the chat row showing its devices (see
	// hoverDevicesRevealMsg), "" if none.
	devicesID string
}

// isHovered reports whether zoneID is the one currently under the pointer.
// Always false when the mouse (and therefore hover tracking) is disabled.
func (m Model) isHovered(zoneID string) bool {
	return m.hover != nil && m.hover.id == zoneID
}

// isExpandButtonHovered reports whether the pointer is over message i's
// "show more"/"show less" toggle specifically, rather than just somewhere
// else in its row.
func (m Model) isExpandButtonHovered(i int) bool {
	if m.hover == nil || !m.isHovered(zoneMessage(i)) {
		return false
	}
	z := m.zone.Get(zoneMessageExpand(i))
	if z.IsZero() {
		return false
	}
	return m.hover.x >= z.StartX && m.hover.x <= z.EndX && m.hover.y >= z.StartY && m.hover.y <= z.EndY
}

// isReactionHovered reports whether the pointer is over message i's j'th
// reaction chip specifically, rather than just somewhere else in its row -
// each chip highlights independently of its siblings.
func (m Model) isReactionHovered(i, j int) bool {
	if m.hover == nil || !m.isHovered(zoneMessage(i)) {
		return false
	}
	z := m.zone.Get(zoneMessageReaction(i, j))
	if z.IsZero() {
		return false
	}
	return m.hover.x >= z.StartX && m.hover.x <= z.EndX && m.hover.y >= z.StartY && m.hover.y <= z.EndY
}

// isReplyKeyHovered reports whether the pointer is on message i's "^r"
// glyph itself, in its hovered row.
func (m Model) isReplyKeyHovered(i int) bool {
	if m.hover == nil || !m.isHovered(zoneMessage(i)) {
		return false
	}
	z := m.zone.Get(zoneMessageReplyKey(i))
	if z.IsZero() {
		return false
	}
	return m.hover.x >= z.StartX && m.hover.x <= z.EndX && m.hover.y >= z.StartY && m.hover.y <= z.EndY
}

// isReactKeyHovered is isReplyKeyHovered's counterpart for the "^t" key
// glyph.
func (m Model) isReactKeyHovered(i int) bool {
	if m.hover == nil || !m.isHovered(zoneMessage(i)) {
		return false
	}
	z := m.zone.Get(zoneMessageReactKey(i))
	if z.IsZero() {
		return false
	}
	return m.hover.x >= z.StartX && m.hover.x <= z.EndX && m.hover.y >= z.StartY && m.hover.y <= z.EndY
}

// handleMouseMotion updates what's hovered. In a chat, hovering a message
// selects it, as the chat list cursor follows the mouse.
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
			// Measured to the box's fixed bottom edge.
			m.inputHeightOverride = z.EndY - msg.Mouse().Y
			m.updateSizes()
		}
		return m, nil
	}

	m.hover.x = msg.Mouse().X
	m.hover.y = msg.Mouse().Y

	newHoverID := m.zoneUnderMouse(msg)
	prevHoverID := m.hover.id
	var hoverCmd tea.Cmd
	if newHoverID != m.hover.id {
		m.hover.id = newHoverID
		m.hover.devicesID = ""
		m.hoverGen++
		if _, ok := chatIndexFromZone(newHoverID); ok {
			hoverCmd = hoverDevicesTimer(newHoverID, m.hoverGen)
		}
	}

	{
		if m.zone.Get(zonePaneSidebar).InBounds(msg) {
			// Not from the account manager, which is in this zone too. And
			// only on a change: setSelectedView re-lays out everything.
			if m.selectedView != viewAccounts && m.selectedView != viewChats {
				m.setSelectedView(viewChats)
			}
		} else if m.zone.Get(zonePaneViewport).InBounds(msg) || m.zone.Get(zonePaneInput).InBounds(msg) {
			if m.currentChatIndex() >= 0 && m.selectedView != viewChat {
				m.setSelectedView(viewChat)
			}
		}
	}

	if m.selectedView == viewChat {
		// One move can change several hover states; their rows are
		// re-rendered in one refreshViewportSelectionMulti, which is costly.
		var changed []int

		// The hovered row's tint follows the pointer even when the
		// selection doesn't (hoverSelectDisabled).
		prevRow, prevOK := messageIndexFromZone(prevHoverID)
		newRow, newOK := messageIndexFromZone(m.hover.id)
		if prevOK != newOK || prevRow != newRow {
			if prevOK {
				changed = append(changed, prevRow)
			}
			if newOK {
				changed = append(changed, newRow)
			}
		}

		if idx, ok := messageIndexFromZone(m.hover.id); ok && idx != m.selectedMsg && !m.hoverSelectDisabled {
			changed = append(changed, m.selectedMsg, idx)
			m.selectedMsg = idx
			m.lastClickedMsgIdx = -1
			m.lastClickTime = time.Time{}
		}

		// See hoverState.replyKeyIdx.
		newReplyKeyIdx := -1
		if idx, ok := messageIndexFromZone(m.hover.id); ok && m.isReplyKeyHovered(idx) {
			newReplyKeyIdx = idx
		}
		if newReplyKeyIdx != m.hover.replyKeyIdx {
			changed = append(changed, m.hover.replyKeyIdx, newReplyKeyIdx)
			m.hover.replyKeyIdx = newReplyKeyIdx
		}

		newReactKeyIdx := -1
		if idx, ok := messageIndexFromZone(m.hover.id); ok && m.isReactKeyHovered(idx) {
			newReactKeyIdx = idx
		}
		if newReactKeyIdx != m.hover.reactKeyIdx {
			changed = append(changed, m.hover.reactKeyIdx, newReactKeyIdx)
			m.hover.reactKeyIdx = newReactKeyIdx
		}

		newExpandBtnIdx := -1
		if idx, ok := messageIndexFromZone(m.hover.id); ok && m.isExpandButtonHovered(idx) {
			newExpandBtnIdx = idx
		}
		if newExpandBtnIdx != m.hover.expandBtnIdx {
			changed = append(changed, m.hover.expandBtnIdx, newExpandBtnIdx)
			m.hover.expandBtnIdx = newExpandBtnIdx
		}

		newReactionMsgIdx, newReactionIdx := -1, -1
		if idx, ok := messageIndexFromZone(m.hover.id); ok {
			msgs := m.currentMessages()
			if idx >= 0 && idx < len(msgs) {
				for j := range msgs[idx].Reactions {
					if m.isReactionHovered(idx, j) {
						newReactionMsgIdx, newReactionIdx = idx, j
						break
					}
				}
			}
		}
		if newReactionMsgIdx != m.hover.reactionMsgIdx || newReactionIdx != m.hover.reactionIdx {
			changed = append(changed, m.hover.reactionMsgIdx, newReactionMsgIdx)
			m.hover.reactionMsgIdx = newReactionMsgIdx
			m.hover.reactionIdx = newReactionIdx
		}

		if len(changed) > 0 {
			m.refreshViewportSelectionMulti(changed...)
		}
	}

	return m, hoverCmd
}

// zoneUnderMouse is the most specific zone under mouse, "" for none, in
// handleLeftClick's order; only the context menu's while one is open.
func (m Model) zoneUnderMouse(mouse tea.MouseMsg) string {
	// The call bar's own buttons take priority the same way zoneSendButton
	// etc. do — it's a fixed status-bar area, so it should win over anything
	// happening to render underneath it.
	for _, b := range m.callBarButtons() {
		if m.zone.Get(b.zone).InBounds(mouse) {
			return b.zone
		}
	}
	if m.zone.Get(zoneJumpToBottom).InBounds(mouse) {
		return zoneJumpToBottom
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
	if m.zone.Get(zoneAccountMenuButton).InBounds(mouse) {
		return zoneAccountMenuButton
	}
	if m.zone.Get(zoneAccountBarName).InBounds(mouse) {
		return zoneAccountBarName
	}
	if m.zone.Get(zoneAccountBarStatus).InBounds(mouse) {
		return zoneAccountBarStatus
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
		if idx, ok := m.messageIndexAtMouse(mouse); ok {
			return zoneMessage(idx)
		}
	}
	return ""
}

// handleMouseClick routes a click to the context menu (if one is open), or
// otherwise dispatches by button: left click acts directly, right click
// opens a context menu of actions for whatever was clicked.
func (m Model) handleMouseClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
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

// handleMouseRelease ends a border drag, saving the size.
func (m Model) handleMouseRelease(msg tea.MouseReleaseMsg) (tea.Model, tea.Cmd) {
	switch {
	case m.resizingSidebar:
		m.resizingSidebar = false
		if err := m.daemon.SaveState(SidebarWidth(m.sidebarWidth())); err != nil {
			return m, m.showNotification("saving sidebar width: " + err.Error())
		}
	case m.resizingInput:
		m.resizingInput = false
		if err := m.daemon.SaveState(InputHeight(m.inputHeightOverride)); err != nil {
			return m, m.showNotification("saving compose box height: " + err.Error())
		}
	}
	return m, nil
}

// handleLeftClick acts on the zone clicked, falling back to its pane.
func (m Model) handleLeftClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	for _, b := range m.callBarButtons() {
		if m.zone.Get(b.zone).InBounds(msg) {
			return m, b.run(&m)
		}
	}

	if m.zone.Get(zoneToggleSidebar).InBounds(msg) {
		return m.toggleSidebar()
	}

	if m.zone.Get(zoneJumpToBottom).InBounds(msg) {
		return m, m.jumpToLatestMessage()
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

	if m.zone.Get(zoneAccountMenuButton).InBounds(msg) {
		return m, m.openAccountMenu()
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
			action := (*Model).actionReplyMessage
			switch {
			case m.zone.Get(zoneMessageReplyBtn(i)).InBounds(msg):
			case m.zone.Get(zoneMessageReactBtn(i)).InBounds(msg):
				action = (*Model).actionReactMessage
			default:
				continue
			}
			m.notifyTypingStopped()
			m.lastClickedMsgIdx = -1
			m.lastClickTime = time.Time{}
			old := m.selectedMsg
			m.selectedMsg = i
			m.refreshViewportScrollTo(old, i)
			return m, action(&m)
		}
		for i, mm := range msgs {
			if m.zone.Get(zoneMessageExpand(i)).InBounds(msg) {
				m.notifyTypingStopped()
				m.expandedMsgs[msgKey(mm, i)] = !m.expandedMsgs[msgKey(mm, i)]
				m.refreshViewportFullScrollTo(i)
				return m, nil
			}
		}
		for i, mm := range msgs {
			for j, r := range mm.Reactions {
				if m.zone.Get(zoneMessageReaction(i, j)).InBounds(msg) {
					m.notifyTypingStopped()
					return m, m.sendReaction(i, toggleMyReaction(mm.Reactions, r.Emoji))
				}
			}
		}
		for i, mm := range msgs {
			target := messageIndexByID(msgs, mm.ReplyToID)
			if target >= 0 && m.zone.Get(zoneMessageReply(i)).InBounds(msg) {
				m.notifyTypingStopped()
				m.lastClickedMsgIdx = -1
				m.lastClickTime = time.Time{}
				m.selectedMsg = target
				m.flashMsgIdx = target
				m.flashGen++
				m.refreshViewportFullScrollTo(target)
				return m, flashTimer(m.flashGen)
			}
		}
		if i, ok := m.messageIndexAtMouse(msg); ok {
			clickTime := time.Now()
			isDoubleClick := m.lastClickedMsgIdx == i && !m.lastClickTime.IsZero() && clickTime.Sub(m.lastClickTime) < 500*time.Millisecond

			old := m.selectedMsg
			m.selectedMsg = i
			m.refreshViewportScrollTo(old, i)

			if isDoubleClick {
				// Double-click: open the message
				m.lastClickedMsgIdx = -1
				m.lastClickTime = time.Time{}
				return m, m.actionOpenMessage()
			}
			// Single-click: just select it - replying now happens via the
			// hover reply button (zoneMessageReplyBtn) or the keybind, not
			// as a side effect of selecting a message.
			m.lastClickedMsgIdx = i
			m.lastClickTime = clickTime
			return m, nil
		}
	}

	if m.zone.Get(zoneReplyHintCancel).InBounds(msg) {
		m.cancelPending()
		return m, nil
	}

	if m.zone.Get(zoneSendButton).InBounds(msg) {
		return m, m.sendCurrentInput()
	}

	if m.zone.Get(zoneAttachButton).InBounds(msg) {
		if m.currentChatIndex() < 0 {
			return m, m.showNotification("no chat selected")
		}
		return m, m.openFilePicker(false)
	}

	if z := m.zone.Get(zoneInputTextarea); !z.IsZero() && z.InBounds(msg) {
		tm, cmd := m.openCurrentChat()
		if mm, ok := tm.(Model); ok {
			mm.positionInputCursorAt(msg, z)
			return mm, cmd
		}
		return tm, cmd
	}

	if m.zone.Get(zonePaneInput).InBounds(msg) {
		// Focuses the input from any pane.
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

// positionInputCursorAt moves the compose cursor to the clicked character.
// The textarea can't place it at a screen position, so it walks there: down
// from the top to the wrapped line, then right by the click's column.
func (m *Model) positionInputCursorAt(msg tea.MouseClickMsg, z *zone.ZoneInfo) {
	mouse := msg.Mouse()
	clickRow := mouse.Y - z.StartY
	clickCol := mouse.X - z.StartX
	if clickRow < 0 || clickCol < 0 {
		return
	}

	targetRow := m.input.ScrollYOffset() + clickRow
	m.input.MoveToBegin()
	for range targetRow {
		before := m.input.Line()
		beforeCol := m.input.Column()
		m.input.CursorDown()
		if m.input.Line() == before && m.input.Column() == beforeCol {
			break // already on the last visual line
		}
	}

	rowStart := m.input.Column()
	rowWidth := m.input.LineInfo().Width
	m.input.SetCursorColumn(rowStart + max(0, min(clickCol-lipgloss.Width(inputPrompt), rowWidth)))
}

// handleRightClick selects what was clicked, as a left-click would, and
// opens its menu, whose actions act on the selection.
func (m Model) handleRightClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	for i := range m.accounts {
		if m.zone.Get(zoneAccountRow(i)).InBounds(msg) {
			m.setSelectedView(viewAccounts)
			cmd := m.switchAccount(i)
			m.openContextMenu(m.accounts[i].NameWithJID(), m.accountRowContextMenuItems(i))
			return m, cmd
		}
	}

	for i := range m.chats.Items() {
		if zoneRowContains(m.zone.Get(zoneChatItem(i)), msg, m.sidebarWidth()) {
			m.selectChatItem(i)
			m.openContextMenu(chatMenuTitle(m.chats.Items(), i), m.chatItemContextMenuItems(i))
			return m, nil
		}
	}

	if m.selectedView == viewChat {
		if i, ok := m.messageIndexAtMouse(msg); ok {
			old := m.selectedMsg
			m.selectedMsg = i
			m.refreshViewportScrollTo(old, i)
			m.lastClickedMsgIdx = -1
			m.lastClickTime = time.Time{}
			m.openContextMenu("Message", m.messageContextMenuItems(i))
			return m, nil
		}
	}

	return m, nil
}

// selectChatItem moves the chat-list cursor to i without opening the chat.
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

// handleMouseWheel scrolls whichever pane the wheel event landed over: the
// chat list in the sidebar, or the message viewport. Swallowed entirely
// while a context menu is open, so it can't scroll what's underneath it.
func (m Model) handleMouseWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	mouse := msg.Mouse()

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
		cmd = m.viewport.Update(msg)
		if len(m.msgOffsets) > 0 {
			yOffset := m.viewport.YOffset()
			top := m.msgIndexAtOffset(yOffset)
			bottom := m.msgIndexAtOffset(yOffset + max(0, m.viewport.Height()-1))
			old := m.selectedMsg
			switch {
			case m.selectedMsg < top:
				m.selectedMsg = top
			case m.selectedMsg > bottom:
				m.selectedMsg = bottom
			}
			if m.selectedMsg != old {
				m.refreshViewportSelection(old, m.selectedMsg)
			}
		}
		if m.viewport.YOffset() == 0 {
			cmd = tea.Batch(cmd, m.maybeLoadOlderHistory())
		}
		if m.viewport.AtBottom() {
			cmd = tea.Batch(cmd, m.maybeLoadNewerHistory())
		}
		return m, cmd
	}

	if m.zone.Get(zonePaneInput).InBounds(msg) {
		// The textarea only scrolls by moving its cursor.
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
