package ui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
)

// Zone IDs for the mouse-clickable regions marked in View(). Panes are
// checked in order from most to least specific — e.g. a click on a chat
// row must be handled before the enclosing sidebar pane's "switch focus"
// fallback.
const (
	zonePaneSidebar    = "pane-sidebar"
	zonePaneViewport   = "pane-viewport"
	zonePaneInput      = "pane-input"
	zonePaneAccountBar = "pane-account-bar"
	zoneSendButton     = "send-button"
)

func zoneAccountRow(i int) string { return fmt.Sprintf("account-row-%d", i) }
func zoneChatItem(i int) string   { return fmt.Sprintf("chat-item-%d", i) }
func zoneMessage(i int) string    { return fmt.Sprintf("msg-%d", i) }

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
// context-menu items) can highlight themselves.
func (m Model) handleMouseMotion(msg tea.MouseMotionMsg) (tea.Model, tea.Cmd) {
	m.hover.id = m.zoneUnderMouse(msg)
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

	if m.zone.Get(zoneSendButton).InBounds(mouse) {
		return zoneSendButton
	}
	for i := range m.accounts {
		if m.zone.Get(zoneAccountRow(i)).InBounds(mouse) {
			return zoneAccountRow(i)
		}
	}
	for i := range m.chats.Items() {
		if m.zone.Get(zoneChatItem(i)).InBounds(mouse) {
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

	switch msg.Mouse().Button {
	case tea.MouseLeft:
		return m.handleLeftClick(msg)
	case tea.MouseRight:
		return m.handleRightClick(msg)
	}
	return m, nil
}

// handleContextMenuClick is the only input the open context menu responds
// to: clicking one of its items runs that item's action and closes the
// menu; any other click (a different item's popup, or empty space) just
// closes it without acting — nothing under the popup is clicked "through".
func (m Model) handleContextMenuClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if msg.Mouse().Button != tea.MouseLeft {
		m.closeContextMenu()
		return m, nil
	}
	for i, item := range m.contextMenu.items {
		if m.zone.Get(zoneContextMenuItem(i)).InBounds(msg) {
			cmd := item.run(&m)
			m.closeContextMenu()
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
	if m.zone.Get(zonePaneAccountBar).InBounds(msg) {
		m.notifyTypingStopped()
		m.selectedView = viewAccounts
		m.input.Blur()
		return m, nil
	}

	for i := range m.accounts {
		if m.zone.Get(zoneAccountRow(i)).InBounds(msg) {
			m.selectedView = viewAccounts
			return m, m.switchAccount(i)
		}
	}

	for i := range m.chats.Items() {
		if m.zone.Get(zoneChatItem(i)).InBounds(msg) {
			m.notifyTypingStopped()
			m.selectChatItem(i)
			return m.openCurrentChat()
		}
	}

	if m.selectedView == viewChat {
		msgs := m.currentMessages()
		for i := range msgs {
			if m.zone.Get(zoneMessage(i)).InBounds(msg) {
				m.selectedMsg = i
				m.refreshViewportScrollTo(i)
				return m, nil
			}
		}
	}

	if m.zone.Get(zoneSendButton).InBounds(msg) {
		return m, m.sendCurrentInput()
	}

	if m.zone.Get(zonePaneInput).InBounds(msg) {
		if m.selectedView == viewChat {
			return m, m.input.Focus()
		}
		return m, nil
	}

	if m.zone.Get(zonePaneViewport).InBounds(msg) {
		if m.currentChatIndex() >= 0 {
			return m.openCurrentChat()
		}
		return m, nil
	}

	if m.zone.Get(zonePaneSidebar).InBounds(msg) {
		m.notifyTypingStopped()
		m.selectedView = viewChats
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
			m.selectedView = viewAccounts
			cmd := m.switchAccount(i)
			m.openContextMenu(m.accountRowContextMenuItems(i))
			return m, cmd
		}
	}

	for i := range m.chats.Items() {
		if m.zone.Get(zoneChatItem(i)).InBounds(msg) {
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
	if m.contextMenu != nil {
		return m, nil
	}

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
		m.viewport, cmd = m.viewport.Update(msg)
		if len(m.msgOffsets) > 0 {
			m.selectedMsg = m.msgIndexAtOffset(m.viewport.YOffset())
			m.refreshViewport()
			m.viewport.SetYOffset(m.viewport.YOffset())
		}
		return m, cmd
	}

	return m, nil
}
