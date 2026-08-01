package ui

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = max(0, msg.Height-footerHeight)
		m.updateSizes()
		m.refreshViewport()
		m.viewport.GotoBottom()
		return m, nil

	case noticeClearMsg:
		if msg.id == m.noticeID {
			m.noticeText = ""
		}
		return m, nil

	case tea.KeyMsg:
		// ── Delete confirmation popup intercepts all input ─────────────────
		if m.confirmTarget != confirmNone {
			switch {
			case key.Matches(msg, m.keys.ConfirmYes):
				switch m.confirmTarget {
				case confirmDeleteMessage:
					m.deleteSelectedMsg()
				case confirmDeleteChat:
					cmds = append(cmds, m.deleteSelectedChat())
				}
				m.confirmTarget = confirmNone
				m.refreshViewport()
				return m, tea.Batch(cmds...)
			case key.Matches(msg, m.keys.ConfirmNo):
				m.confirmTarget = confirmNone
			}
			return m, nil
		}

		switch {

		// ── Global ────────────────────────────────────────────────────────
		case key.Matches(msg, m.keys.Quit):
			if msg.String() == "ctrl+c" || m.selectedView != viewChat {
				return m, tea.Quit
			}

		case key.Matches(msg, m.keys.Back):
			if m.selectedView != viewAccounts && m.selectedView != viewChats {
				m.cancelPending()
				m.selectedView = viewChats
				m.input.Blur()
				return m, nil
			}

		case key.Matches(msg, m.keys.FocusChats):
			m.selectedView = viewChats
			m.input.Blur()
			return m, nil

		case key.Matches(msg, m.keys.ChatOpen):
			if m.selectedView == viewChats {
				return m.openCurrentChat()
			}

		case key.Matches(msg, m.keys.SelectSend):
			switch m.selectedView {
			case viewChats:
				return m.openCurrentChat()
			case viewChat:
				text := strings.TrimSpace(m.input.Value())
				if text == "" {
					return m, nil
				}
				chatIdx := m.currentChatIndex()
				if chatIdx < 0 {
					return m, nil
				}

				if m.editingMsgIdx >= 0 {
					// Apply edit in-place.
					msgs := m.currentMessages()
					if m.editingMsgIdx < len(msgs) {
						msgs[m.editingMsgIdx].Content = text
						m.setCurrentMessages(msgs)
					}
					m.editingMsgIdx = -1
					m.input.Placeholder = "message..."
				} else {
					// Send new message, optionally quoting a reply.
					newMsg := Message{
						Author:  "me",
						Content: text,
						SentAt:  time.Now(),
						IsMe:    true,
					}
					if m.replyToIdx >= 0 {
						rt := m.replyToIdx
						newMsg.ReplyTo = &rt
						m.replyToIdx = -1
					}
					msgs := append(m.currentMessages(), newMsg)
					m.setCurrentMessages(msgs)
				}

				m.input.SetValue("")
				m.updateSizes()
				m.refreshViewport()
				m.viewport.GotoBottom()
				// cmds = append(cmds, m.showNotification("message sent")) # TODO: only show notification on error
				return m, tea.Batch(cmds...)
			}

		case key.Matches(msg, m.keys.Switch):
			switch m.selectedView {
			case viewAccounts:
				m.selectedView = viewChats
				return m, nil
			case viewChats:
				m.selectedView = viewAccounts
				return m, nil
			}

		// ── Viewport paging (chat view) ───────────────────────────────────
		case isViewportPagingKey(msg):
			if m.selectedView == viewChat {
				var viewportCmd tea.Cmd
				m.viewport, viewportCmd = m.viewport.Update(msg)
				cmds = append(cmds, viewportCmd)
				return m, tea.Batch(cmds...)
			}

		// ── Message navigation ─────────────────────────────────────────────
		case key.Matches(msg, m.keys.MsgUp):
			if m.selectedView == viewAccounts && m.currentAccount > 0 {
				cmds = append(cmds, m.switchAccount(m.currentAccount-1))
				return m, tea.Batch(cmds...)
			}
			if m.selectedView == viewChat && m.selectedMsg > 0 {
				m.selectedMsg--
				m.refreshViewportScrollTo(m.selectedMsg)
				return m, nil
			}

		case key.Matches(msg, m.keys.MsgDown):
			if m.selectedView == viewAccounts && m.currentAccount < len(m.accounts)-1 {
				cmds = append(cmds, m.switchAccount(m.currentAccount+1))
				return m, tea.Batch(cmds...)
			}
			if m.selectedView == viewChat {
				chatIdx := m.currentChatIndex()
				if chatIdx < 0 {
					return m, nil
				}
				if m.selectedMsg < len(m.currentMessages())-1 {
					m.selectedMsg++
					m.refreshViewportScrollTo(m.selectedMsg)
					return m, nil
				}
			}

		// ── Message actions ────────────────────────────────────────────────
		case key.Matches(msg, m.keys.DeleteMsg):
			if m.selectedView == viewChat {
				chatIdx := m.currentChatIndex()
				if chatIdx < 0 {
					return m, nil
				}
				if len(m.currentMessages()) > 0 {
					m.confirmTarget = confirmDeleteMessage
				}
				return m, nil
			}
			if m.selectedView == viewChats && m.currentChatIndex() >= 0 {
				m.confirmTarget = confirmDeleteChat
				return m, nil
			}

		case key.Matches(msg, m.keys.YankMsg):
			if m.selectedView == viewChat {
				if err := m.yankSelectedMsg(); err != nil {
					cmds = append(cmds, m.showNotification("copy failed"))
				} else {
					cmds = append(cmds, m.showNotification("message copied"))
				}
				return m, tea.Batch(cmds...)
			}

		case key.Matches(msg, m.keys.EditMsg):
			if m.selectedView == viewChat {
				if m.currentChatIndex() < 0 {
					return m, nil
				}
				msgs := m.currentMessages()
				if m.canEdit(msgs) {
					m.editingMsgIdx = m.selectedMsg
					m.input.SetValue(msgs[m.selectedMsg].Content)
					m.input.Placeholder = "edit message..."
					cmds = append(cmds, m.input.Focus())
					return m, tea.Batch(cmds...)
				}
			}

		case key.Matches(msg, m.keys.ReplyMsg):
			if m.selectedView == viewChat {
				if m.currentChatIndex() < 0 {
					return m, nil
				}
				if len(m.currentMessages()) > 0 {
					m.replyToIdx = m.selectedMsg
					m.updateSizes()
					m.refreshViewport()
					cmds = append(cmds, m.input.Focus())
					return m, tea.Batch(cmds...)
				}
			}
		}
	}

	// Route remaining events to the focused component.
	var cmd tea.Cmd
	switch m.selectedView {
	case viewAccounts:
		// Account focus is handled by global keys only.
	case viewChats:
		prev := m.chats.Index()
		m.chats, cmd = m.chats.Update(msg)
		cmds = append(cmds, cmd)
		if m.chats.Index() != prev {
			chatIdx := m.currentChatIndex()
			if chatIdx < 0 {
				m.selectedMsg = 0
				m.refreshViewport()
				break
			}
			msgs := m.currentMessages()
			if len(msgs) > 0 {
				m.selectedMsg = len(msgs) - 1
			} else {
				m.selectedMsg = 0
			}
			m.refreshViewport()
			m.viewport.GotoBottom()
		}
	case viewChat:
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func isViewportPagingKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "pgup", "pgdown", "pageup", "pagedown":
		return true
	default:
		return false
	}
}
