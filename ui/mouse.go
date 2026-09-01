package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"
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
	zoneEmojiPickerPopup      = "emoji-picker-popup"
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
	zoneFilePickerPopup       = "file-picker-popup"
	zoneFilePickerBack        = "file-picker-back-button"
	zoneFilePickerForward     = "file-picker-forward-button"
)

// inputWheelScrollLines is how many lines a single wheel notch moves the
// compose box's cursor (and so its internal viewport) by.
const inputWheelScrollLines = 2

func zoneFilePickerRow(i int) string      { return fmt.Sprintf("file-picker-row-%d", i) }
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
func zoneMsgInfoAttachment(i int) string  { return fmt.Sprintf("msg-info-attachment-%d", i) }
func zoneContactRow(i int) string         { return fmt.Sprintf("contact-row-%d", i) }
func zoneDeviceRow(i int) string          { return fmt.Sprintf("device-row-%d", i) }
func zoneSearchResultRow(i int) string    { return fmt.Sprintf("search-result-row-%d", i) }

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

// messageIndexAtMouse maps a mouse position onto a message index using the
// viewport's own scroll offset and each message's known line offset
// (m.msgOffsets), instead of bubblezone's per-message Mark/Scan
// (zoneMessage(i) below). bubblezone only reports a zone for the frame in
// which *both* its start and end markers were scanned; a multi-line message
// straddling the viewport's visible edge has its tail marker scrolled out of
// view, so that zone silently keeps stale bounds from whenever it was last
// fully visible instead of being updated or cleared - Get(zoneMessage(i))
// then answers with the wrong rectangle (or none at all) even while the
// message's header is plainly on screen. Line-offset arithmetic against the
// viewport's own frame has no such blind spot: zonePaneViewport is marked
// around the already-scrolled, single-frame content (see renderChatArea), so
// its own bounds are never subject to this problem.
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

	// x, y are the pointer's last-seen column/row, updated on every motion
	// event regardless of whether id changed. Used to tell whether the
	// pointer is specifically over a nested sub-zone (the expand button, the
	// reply/react key glyphs, a reaction chip) within a hovered message row
	// - itself one zone spanning every line the message renders across -
	// without needing a zone of its own for each: re-deriving bounds from
	// the row's own known sub-marks is simpler than relying on the scanner
	// to resolve a mark nested inside another mark that itself moves as
	// messages load/scroll. Both x and y matter, not just x: a message row
	// is multiple lines tall, and a sub-zone only occupies one of them, so
	// checking x alone would match the pointer being over a *different*
	// line of the same row at the sub-zone's column.
	x, y int

	// replyKeyIdx is the index of the message whose "^r" key glyph is
	// currently drawn as hovered, or -1. refreshViewportSelection only
	// re-renders a message when its *selection* changes (see its own doc
	// comment - deliberately not on every motion event, to avoid lagging a
	// fast mouse sweep), so without this the key's reversed style would only
	// ever reflect wherever the pointer happened to be the instant the row
	// became selected, then stay frozen there as the pointer kept moving
	// within the same already-selected row. Tracking it separately lets
	// handleMouseMotion detect specifically *this* change and force the one
	// extra re-render it needs.
	replyKeyIdx int

	// reactKeyIdx is replyKeyIdx's counterpart for the "^t" key glyph.
	reactKeyIdx int

	// expandBtnIdx is replyKeyIdx's counterpart for the "show more"/"show
	// less" toggle.
	expandBtnIdx int

	// reactionMsgIdx/reactionIdx track which message's which reaction chip
	// (by index into that message's Reactions) is currently drawn as
	// hovered - replyKeyIdx's counterpart for reaction chips, one of which
	// can independently highlight without affecting its siblings.
	// reactionMsgIdx is -1 when no reaction chip anywhere is hovered.
	reactionMsgIdx int
	reactionIdx    int

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

// isReplyKeyHovered reports whether the pointer is over message i's "^r" key
// glyph specifically - not the "reply" label next to it, and not just
// somewhere else in its row. Requires the row itself to already be the
// hovered zone; within that, it checks the glyph's own nested zone bounds
// (marked wherever renderMessage placed it, inside the outer
// zoneMessageReplyBtn click zone that covers both the glyph and its label).
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
		row := m.filePickerRowUnderMouse(msg)
		if row < 0 {
			return m, nil
		}
		return m, filePickerMoveCmd(m.filePicker.SelectedRow(), row)
	}

	if m.emojiPicker != nil {
		if i, ok := m.emojiPickerCellUnderMouse(msg); ok {
			m.emojiPicker.SetCursor(i)
		}
		return m, nil
	}

	m.hover.x = msg.Mouse().X
	m.hover.y = msg.Mouse().Y

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
		// changes, but the "^r"/"^t" key glyphs' own hovered/reversed state
		// can also change from a pointer move *within* an already-selected
		// row (sliding onto or off of the glyph's column range) - that
		// needs its own re-render or the glyph freezes at whatever it
		// looked like the moment the row was entered (see
		// hoverState.replyKeyIdx).
		newReplyKeyIdx := -1
		if idx, ok := messageIndexFromZone(m.hover.id); ok && m.isReplyKeyHovered(idx) {
			newReplyKeyIdx = idx
		}
		if newReplyKeyIdx != m.hover.replyKeyIdx {
			old := m.hover.replyKeyIdx
			m.hover.replyKeyIdx = newReplyKeyIdx
			m.refreshViewportSelection(old, newReplyKeyIdx)
		}

		newReactKeyIdx := -1
		if idx, ok := messageIndexFromZone(m.hover.id); ok && m.isReactKeyHovered(idx) {
			newReactKeyIdx = idx
		}
		if newReactKeyIdx != m.hover.reactKeyIdx {
			old := m.hover.reactKeyIdx
			m.hover.reactKeyIdx = newReactKeyIdx
			m.refreshViewportSelection(old, newReactKeyIdx)
		}

		newExpandBtnIdx := -1
		if idx, ok := messageIndexFromZone(m.hover.id); ok && m.isExpandButtonHovered(idx) {
			newExpandBtnIdx = idx
		}
		if newExpandBtnIdx != m.hover.expandBtnIdx {
			old := m.hover.expandBtnIdx
			m.hover.expandBtnIdx = newExpandBtnIdx
			m.refreshViewportSelection(old, newExpandBtnIdx)
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
			old := m.hover.reactionMsgIdx
			m.hover.reactionMsgIdx = newReactionMsgIdx
			m.hover.reactionIdx = newReactionIdx
			m.refreshViewportSelection(old, newReactionMsgIdx)
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

	if m.emojiPicker != nil {
		// Cells are clickable (see the click-dispatch handler below) but
		// don't have a distinct hover style, so there's nothing for this to
		// report - a click still works without ever appearing "hovered".
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

	if m.emojiPicker != nil {
		return m.handleEmojiPickerClick(msg)
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
		for i := range msgs {
			if m.zone.Get(zoneMessageReactBtn(i)).InBounds(msg) {
				m.notifyTypingStopped()
				m.lastClickedMsgIdx = -1
				m.lastClickTime = time.Time{}
				old := m.selectedMsg
				m.selectedMsg = i
				m.refreshViewportScrollTo(old, i)
				return m, m.actionReactMessage()
			}
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
		if i, ok := m.messageIndexAtMouse(msg); ok {
			old := m.selectedMsg
			m.selectedMsg = i
			m.refreshViewportScrollTo(old, i)
			m.lastClickedMsgIdx = -1
			m.lastClickTime = time.Time{}
			m.openContextMenu(m.messageContextMenuItems(i))
			return m, nil
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

// emojiPickerCellUnderMouse returns the visible grid cell index the mouse is
// over, and whether it's over any cell at all - checked against the
// picker's own zone marks left over from its last render (see
// filePickerRowUnderMouse for why: re-deriving them means re-rendering
// View(), too expensive per mouse-motion event).
func (m Model) emojiPickerCellUnderMouse(msg tea.MouseMsg) (int, bool) {
	if m.emojiPicker.Zone == nil {
		return 0, false
	}
	for _, i := range m.emojiPicker.VisibleCells() {
		if m.zone.Get(m.emojiPicker.CellZoneID(i)).InBounds(msg) {
			return i, true
		}
	}
	return 0, false
}

// filePickerRowUnderMouse returns the file-picker row the mouse is over, or
// -1 if it isn't over any row. Checked against the picker's own zone marks
// left over from its last render, rather than re-rendering View() here to
// re-derive them — View() does symlink resolution and ANSI styling per row,
// too expensive to redo on every mouse-motion event.
func (m Model) filePickerRowUnderMouse(msg tea.MouseMsg) int {
	for i := range m.filePicker.Height() {
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

	targetRow := m.filePickerRowUnderMouse(msg)
	if !m.zone.Get(zoneFilePickerPopup).InBounds(msg) {
		m.pickingFile = false
		return m, nil
	}

	if m.zone.Get(zoneFilePickerBack).InBounds(msg) {
		if !m.filePicker.CanGoBack() {
			return m, nil
		}
		// Routed as a synthetic backspace rather than calling into the
		// picker directly, so a click behaves exactly like the keyboard
		// Back binding (h/backspace/left) — that binding, not GoForward, is
		// what already knows how to pop the cursor-position stack.
		return m, func() tea.Msg { return tea.KeyPressMsg{Code: tea.KeyBackspace} }
	}
	if m.zone.Get(zoneFilePickerForward).InBounds(msg) {
		if !m.filePicker.CanGoForward() {
			return m, nil
		}
		var cmd tea.Cmd
		*m.filePicker, cmd = m.filePicker.GoForward()
		return m, cmd
	}
	if targetRow < 0 {
		return m, nil
	}

	cursorRow := m.filePicker.SelectedRow()

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
