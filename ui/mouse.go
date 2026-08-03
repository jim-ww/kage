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
)

func zoneAccountRow(i int) string { return fmt.Sprintf("account-row-%d", i) }
func zoneChatItem(i int) string   { return fmt.Sprintf("chat-item-%d", i) }
func zoneMessage(i int) string    { return fmt.Sprintf("msg-%d", i) }

// handleMouseClick dispatches a left-click to whichever marked zone it
// landed in: an account row, a chat list item, a message, or one of the
// three panes (sidebar / viewport / input) when the click missed anything
// more specific inside it.
func (m Model) handleMouseClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	mouse := msg.Mouse()
	if mouse.Button != tea.MouseLeft {
		return m, nil
	}

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

// handleMouseWheel scrolls whichever pane the wheel event landed over: the
// chat list in the sidebar, or the message viewport.
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
