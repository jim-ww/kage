package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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
	zoneInputTextarea         = "input-textarea"
	zoneAccountBarName        = "account-bar-name"
	zoneAccountBarStatus      = "account-bar-status"
	zoneStoragePasswordButton = "storage-password-button"
	zoneChatStatusBar         = "chat-status-bar"
	zoneSendButton            = "send-button"
	zoneAttachButton          = "attach-button"
	zoneReplyHintCancel       = "reply-hint-cancel"
	zoneToggleSidebar         = "toggle-sidebar-button"
	zoneMsgInfoPopup          = "msg-info-popup"
	zoneContactManagerPopup   = "contact-manager-popup"
	zoneDeviceListPopup       = "device-list-popup"
	zoneSearchResultsPopup    = "search-results-popup"
	zoneCallAnswer            = "call-answer-button"
	zoneCallReject            = "call-reject-button"
	zoneCallMute              = "call-mute-button"
	zoneCallHangup            = "call-hangup-button"
	zoneCallVideo             = "call-video-button"
	zoneCallVideoCamera       = "call-video-camera-button"
	zoneCallVideoScreen       = "call-video-screen-button"
	zoneCallReopenVideo       = "call-reopen-video-button"
	zoneJumpToBottom          = "jump-to-bottom-button"
)

// inputWheelScrollLines is how many lines a single wheel notch moves the
// compose box's cursor (and so its internal viewport) by.
const inputWheelScrollLines = 2

func zoneFilePickerRow(i int) string     { return fmt.Sprintf("file-picker-row-%d", i) }
func zoneAccountRow(i int) string        { return fmt.Sprintf("account-row-%d", i) }
func zoneChatItem(i int) string          { return fmt.Sprintf("chat-item-%d", i) }
func zoneMessage(i int) string           { return fmt.Sprintf("msg-%d", i) }
func zoneMessageReply(i int) string      { return fmt.Sprintf("msg-reply-%d", i) }
func zoneMessageReplyBtn(i int) string   { return fmt.Sprintf("msg-reply-btn-%d", i) }
func zoneEmojiSuggestion(i int) string   { return fmt.Sprintf("emoji-suggest-%d", i) }
func zoneAttachmentRemove(i int) string  { return fmt.Sprintf("attachment-remove-%d", i) }
func zoneMsgInfoAttachment(i int) string { return fmt.Sprintf("msg-info-attachment-%d", i) }
func zoneContactRow(i int) string        { return fmt.Sprintf("contact-row-%d", i) }
func zoneDeviceRow(i int) string         { return fmt.Sprintf("device-row-%d", i) }
func zoneSearchResultRow(i int) string   { return fmt.Sprintf("search-result-row-%d", i) }

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

	// x is the pointer's last-seen column, updated on every motion event
	// regardless of whether id changed. Used to tell whether the pointer is
	// specifically over the hover reply button within a hovered message row
	// (see isReplyButtonHovered) without needing a zone of its own - the
	// button's zone is nested inside zoneMessage's, and re-deriving its
	// bounds from the row's own zone plus its known reserved width is
	// simpler than relying on the scanner to resolve a mark nested inside
	// another mark that itself moves as messages load/scroll.
	x int

	// replyBtnIdx is the index of the message whose reply button is
	// currently drawn as hovered, or -1. refreshViewportSelection only
	// re-renders a message when its *selection* changes (see its own doc
	// comment - deliberately not on every motion event, to avoid lagging a
	// fast mouse sweep), so without this the reply button's reversed style
	// would only ever reflect wherever the pointer happened to be the
	// instant the row became selected, then stay frozen there as the
	// pointer kept moving within the same already-selected row. Tracking it
	// separately lets handleMouseMotion detect specifically *this* change
	// and force the one extra re-render it needs.
	replyBtnIdx int

	// devicesID is the chat-item zone ID currently showing its online-device
	// list in place of the row's normal description (see
	// renderHoverChatRow), or "" if none is. Set by hoverDevicesRevealMsg
	// once hoverDevicesDelay has passed without the hovered row changing.
	devicesID string
}

// isHovered reports whether zoneID is the one currently under the pointer.
// Always false when the mouse (and therefore hover tracking) is disabled.
func (m Model) isHovered(zoneID string) bool {
	return m.hover != nil && m.hover.id == zoneID
}

// isReplyButtonHovered reports whether the pointer is over message i's hover
// reply button specifically, rather than just somewhere else in its row.
// btnWidth is the button's rendered width (constant regardless of its own
// hover state - see renderReplyButton). Requires the row itself to already
// be the hovered zone; within that, the button occupies the last btnWidth
// columns of the row (see renderMessage's flush-right layout).
func (m Model) isReplyButtonHovered(i, btnWidth int) bool {
	if m.hover == nil || !m.isHovered(zoneMessage(i)) {
		return false
	}
	z := m.zone.Get(zoneMessage(i))
	if z.IsZero() {
		return false
	}
	return m.hover.x > z.EndX-btnWidth
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

	m.hover.x = msg.Mouse().X

	newHoverID := m.zoneUnderMouse(msg)
	var hoverCmd tea.Cmd
	if newHoverID != m.hover.id {
		m.hover.id = newHoverID
		m.hover.devicesID = ""
		m.hoverGen++
		if _, ok := chatIndexFromZone(newHoverID); ok {
			hoverCmd = hoverDevicesTimer(newHoverID, m.hoverGen)
		}
	}

	if m.contextMenu == nil {
		if m.zone.Get(zonePaneSidebar).InBounds(msg) {
			// Don't steal focus away from the account manager — it renders
			// inside this same sidebar zone, so hovering an account row
			// would otherwise bounce selectedView back to viewChats and
			// the popup would look like it instantly closed.
			//
			// setSelectedView also calls updateSizes() (layout/textarea
			// recalculation), so this must only run on an actual change —
			// every mouse-motion event lands in this same branch while the
			// mouse sweeps the sidebar, and re-running updateSizes() on
			// every single one of them (instead of just once, on entry) is
			// what made the hovered-row highlight visibly lag behind a fast
			// mouse sweep.
			if m.selectedView != viewAccounts && m.selectedView != viewChats {
				m.setSelectedView(viewChats)
			}
		} else if m.zone.Get(zonePaneViewport).InBounds(msg) || m.zone.Get(zonePaneInput).InBounds(msg) {
			if m.currentChatIndex() >= 0 && m.selectedView != viewChat {
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

		// The row-selection refresh above only fires when selectedMsg
		// changes, but the reply button's own hovered/reversed state can
		// also change from a pointer move *within* an already-selected row
		// (sliding onto or off of the button's column range) - that needs
		// its own re-render or the button freezes at whatever it looked
		// like the moment the row was entered (see hoverState.replyBtnIdx).
		newBtnIdx := -1
		if idx, ok := messageIndexFromZone(m.hover.id); ok && m.isReplyButtonHovered(idx, m.replyButtonWidth()) {
			newBtnIdx = idx
		}
		if newBtnIdx != m.hover.replyBtnIdx {
			old := m.hover.replyBtnIdx
			m.hover.replyBtnIdx = newBtnIdx
			m.refreshViewportSelection(old, newBtnIdx)
		}
	}

	return m, hoverCmd
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

	if m.contactManagerState != nil {
		cs := m.contactManagerState
		if !cs.adding && cs.pendingRemove == "" && cs.err == "" {
			contacts := cs.contacts(m)
			start, end := cs.bounds(len(contacts))
			for i := 0; i < end-start; i++ {
				if m.zone.Get(zoneContactRow(i)).InBounds(mouse) {
					return zoneContactRow(i)
				}
			}
		}
		return ""
	}

	if m.searchResults != nil {
		sr := m.searchResults
		if matches := sr.filteredMatches(); !sr.busy && sr.err == "" && !sr.filtering && len(matches) > 0 {
			start, end := sr.bounds(len(matches))
			for i := 0; i < end-start; i++ {
				if m.zone.Get(zoneSearchResultRow(i)).InBounds(mouse) {
					return zoneSearchResultRow(i)
				}
			}
		}
		return ""
	}

	if m.deviceList != nil {
		dl := m.deviceList
		if !dl.busy && !dl.confirming && dl.err == "" {
			removable := dl.removableDevices()
			start, end := dl.bounds(len(removable))
			for i := 0; i < end-start; i++ {
				if m.zone.Get(zoneDeviceRow(i)).InBounds(mouse) {
					return zoneDeviceRow(i)
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
		if m.zone.Get(zoneCallVideo).InBounds(mouse) {
			return zoneCallVideo
		}
		if m.zone.Get(zoneCallVideoCamera).InBounds(mouse) {
			return zoneCallVideoCamera
		}
		if m.zone.Get(zoneCallVideoScreen).InBounds(mouse) {
			return zoneCallVideoScreen
		}
		if m.zone.Get(zoneCallReopenVideo).InBounds(mouse) {
			return zoneCallReopenVideo
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

	if m.contactManagerState != nil {
		return m.handleContactManagerClick(msg)
	}

	if m.searchResults != nil {
		return m.handleSearchResultsClick(msg)
	}

	if m.deviceList != nil {
		return m.handleDeviceListClick(msg)
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
		if m.videoDialPrompt {
			if m.zone.Get(zoneCallVideoCamera).InBounds(msg) {
				m, cmd := m.startVideoCall(true)
				return m, cmd
			}
			if m.zone.Get(zoneCallVideoScreen).InBounds(msg) {
				m, cmd := m.startVideoCall(false)
				return m, cmd
			}
			return m, nil
		}
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
		if m.zone.Get(zoneCallReopenVideo).InBounds(msg) {
			return m, m.reopenRemoteVideo()
		}
		if m.videoSourcePrompt {
			if m.zone.Get(zoneCallVideoCamera).InBounds(msg) {
				m, cmd := m.startVideo(true)
				return m, cmd
			}
			if m.zone.Get(zoneCallVideoScreen).InBounds(msg) {
				m, cmd := m.startVideo(false)
				return m, cmd
			}
		} else if m.zone.Get(zoneCallVideo).InBounds(msg) {
			return m.startVideoPrompt(), nil
		}
	}

	if m.zone.Get(zoneToggleSidebar).InBounds(msg) {
		return m.toggleSidebar()
	}

	if m.zone.Get(zoneJumpToBottom).InBounds(msg) {
		return m, m.jumpToLatestMessage()
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
			if m.zone.Get(zoneMessageReplyBtn(i)).InBounds(msg) {
				m.notifyTypingStopped()
				m.lastClickedMsgIdx = -1
				m.lastClickTime = time.Time{}
				old := m.selectedMsg
				m.selectedMsg = i
				m.refreshViewportScrollTo(old, i)
				return m, m.actionReplyMessage()
			}
		}
		for i, mm := range msgs {
			if mm.ReplyTo != nil && m.zone.Get(zoneMessageReply(i)).InBounds(msg) {
				m.notifyTypingStopped()
				m.lastClickedMsgIdx = -1
				m.lastClickTime = time.Time{}
				m.selectedMsg = *mm.ReplyTo
				m.flashMsgIdx = *mm.ReplyTo
				m.flashGen++
				m.refreshViewportFullScrollTo(*mm.ReplyTo)
				return m, flashTimer(m.flashGen)
			}
		}
		for i := range msgs {
			if m.zone.Get(zoneMessage(i)).InBounds(msg) {
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
		m.pickingFile = true
		m.filePicker.SetHeight(max(1, m.height-m.inputAreaHeight()-6))
		return m, m.filePicker.Init()
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

// positionInputCursorAt moves the compose box's cursor to the character
// under a click inside zoneInputTextarea, mirroring native terminal apps'
// click-to-place-caret behavior. The textarea only exposes cursor movement
// in terms of (visual-line, logical-column) deltas, not "row/col at screen
// position", so the target visual line (line-wrap-aware, via
// ScrollYOffset()+clickRow) is reached by walking there from the top with
// MoveToBegin()+CursorDown() — same trick handleMouseWheel already uses to
// scroll the box by moving the cursor — and then the column is nudged right
// from that line's start by the click's X offset past the prompt.
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
				old := m.selectedMsg
				m.selectedMsg = i
				m.refreshViewportScrollTo(old, i)
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
// here. A single click on a directory opens it immediately, since a click
// there can never be a final selection (Enter just navigates into it). A
// second click on the already-selected row within the double-click window
// opens it for anything else (i.e. files). In practice the cursor is
// usually already on the clicked row by the time this fires, since
// hovering it (handleMouseMotion) moves the cursor there first.
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
	if isDoubleClick || m.filePicker.IsDirRow(targetRow) {
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
		*m.filePicker, cmd = m.filePicker.Update(key)
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
		if m.viewport.AtBottom() {
			cmd = tea.Batch(cmd, m.maybeLoadNewerHistory())
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
