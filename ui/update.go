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

	case openResultMsg:
		if msg.err != nil {
			return m, m.showNotification("failed to open " + msg.target)
		}
		return m, m.showNotification("opened " + msg.target)

	case IncomingMessageMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil
		}
		msgs := m.accounts[msg.AccountIdx].Messages[chatIdx]
		newMsg := msg.Message
		if replyIdx := messageIndexByID(msgs, msg.ReplyToID); replyIdx >= 0 {
			newMsg.ReplyTo = &replyIdx
		}
		m.accounts[msg.AccountIdx].Messages[chatIdx] = append(msgs, newMsg)
		if msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
			m.selectedMsg = len(m.accounts[msg.AccountIdx].Messages[chatIdx]) - 1
			m.refreshViewport()
			m.viewport.GotoBottom()
		}
		return m, nil

	case MessageCorrectedMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil
		}
		msgs := m.accounts[msg.AccountIdx].Messages[chatIdx]
		idx := messageIndexByID(msgs, msg.ReplaceID)
		if idx < 0 {
			return m, nil
		}
		msgs[idx].Content = msg.NewContent
		if msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
			m.refreshViewport()
		}
		return m, nil

	case MessageRetractedMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil
		}
		msgs := m.accounts[msg.AccountIdx].Messages[chatIdx]
		idx := messageIndexByID(msgs, msg.RetractID)
		if idx < 0 {
			return m, nil
		}
		msgs[idx].Retracted = true
		if msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
			m.refreshViewport()
		}
		return m, nil

	case MessageReactionsMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil
		}
		msgs := m.accounts[msg.AccountIdx].Messages[chatIdx]
		idx := messageIndexByID(msgs, msg.MessageID)
		if idx < 0 {
			return m, nil
		}
		msgs[idx].Reactions = msg.Reactions
		if msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
			m.refreshViewport()
		}
		return m, nil

	case PresenceMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil
		}
		chat, ok := m.accounts[msg.AccountIdx].Chats[chatIdx].(Chat)
		if !ok {
			return m, nil
		}
		chat.Presence = msg.Presence
		m.accounts[msg.AccountIdx].Chats[chatIdx] = chat
		if msg.AccountIdx == m.currentAccount {
			var cmd tea.Cmd
			cmd = m.chats.SetItem(chatIdx, chat)
			return m, cmd
		}
		return m, nil

	case tea.KeyMsg:
		// ── Delete confirmation popup intercepts all input ─────────────────
		if m.confirmTarget != confirmNone {
			switch {
			case key.Matches(msg, m.keys.ConfirmYes):
				switch m.confirmTarget {
				case confirmDeleteMessage:
					// Only our own messages can meaningfully be retracted on
					// the network (XEP-0424); deleting someone else's
					// message is always local-only, same as before.
					if msgs := m.currentMessages(); m.selectedMsg >= 0 && m.selectedMsg < len(msgs) {
						target := msgs[m.selectedMsg]
						if target.IsMe && target.ID != "" {
							if chat, ok := m.currentChat(); ok && chat.Address != "" && m.sender != nil {
								if _, err := m.sender.Send(m.currentAccount, chat.Address, "", SendOptions{RetractID: target.ID}); err != nil {
									cmds = append(cmds, m.showNotification("retract not delivered: "+err.Error()))
								}
							}
						}
					}
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

		// ── Message-info popup intercepts all input until dismissed ────────
		if m.showMsgInfo {
			switch {
			case key.Matches(msg, m.keys.InfoMsg), key.Matches(msg, m.keys.Back), key.Matches(msg, m.keys.ConfirmNo):
				m.showMsgInfo = false
			}
			return m, nil
		}

		// ── Open-item picker intercepts all input until a choice is made ───
		if len(m.openItems) > 0 {
			start, end := openPageBounds(len(m.openItems), m.openPage)
			if i, ok := digitKey(msg); ok && i >= 1 && i <= end-start {
				target := m.openItems[start+i-1]
				m.openItems = nil
				m.openPage = 0
				return m, openWithXDGOpen(target)
			}
			switch msg.String() {
			case "left", "h":
				m.openPage = max(0, m.openPage-1)
			case "right", "l":
				if m.openPage < openPageCount(len(m.openItems))-1 {
					m.openPage++
				}
			default:
				switch {
				case key.Matches(msg, m.keys.Back), key.Matches(msg, m.keys.ConfirmNo):
					m.openItems = nil
					m.openPage = 0
				}
			}
			return m, nil
		}

		switch {

		// ── Reaction-composition suggestion nav (must precede ChatOpen,
		// which also binds "right", and Switch, which binds "tab") ────────
		case m.reactingMsgIdx >= 0 && len(m.emojiSuggestions) > 0 && (msg.String() == "left" || msg.String() == "right"):
			n := len(m.emojiSuggestions)
			if msg.String() == "left" {
				m.emojiSuggestIdx = (m.emojiSuggestIdx - 1 + n) % n
			} else {
				m.emojiSuggestIdx = (m.emojiSuggestIdx + 1) % n
			}
			return m, nil

		case msg.String() == "tab" && m.reactingMsgIdx >= 0 && len(m.emojiSuggestions) > 0:
			chosen := m.emojiSuggestions[m.emojiSuggestIdx].Shortcode
			m.input.SetValue(acceptEmojiSuggestion(m.input.Value(), chosen))
			m.input.CursorEnd()
			var next []emojiSuggestion
			if token, _, ok := currentWordToken(m.input.Value()); ok {
				next = emojiSuggestionsFor(token)
			}
			m.setEmojiSuggestions(next)
			return m, nil

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
			case viewAccounts:
				m.selectedView = viewChats
				return m, nil
			case viewChats:
				return m.openCurrentChat()
			case viewChat:
				if m.reactingMsgIdx >= 0 {
					// Unlike a normal send, an empty input is meaningful here
					// (it's how you clear your reaction set), so this bypasses
					// the "empty means do nothing" rule below entirely.
					newMine := toEmojiSet(m.input.Value())
					cmds = append(cmds, m.sendReaction(m.reactingMsgIdx, newMine))
					m.reactingMsgIdx = -1
					m.setEmojiSuggestions(nil)
					m.input.SetValue("")
					m.input.Placeholder = "message..."
					m.updateSizes()
					m.refreshViewport()
					return m, tea.Batch(cmds...)
				}

				text := strings.TrimSpace(m.input.Value())
				if text == "" {
					return m, nil
				}
				chatIdx := m.currentChatIndex()
				if chatIdx < 0 {
					return m, nil
				}

				if m.editingMsgIdx >= 0 {
					// Apply edit in-place, and wire it as a XEP-0308 correction
					// so the other party actually sees the update — a message
					// can only be corrected on the network if it was sent with
					// an ID in the first place (e.g. locally-seeded/demo data
					// never was), so degrade to a local-only edit otherwise.
					msgs := m.currentMessages()
					if m.editingMsgIdx < len(msgs) {
						msgs[m.editingMsgIdx].Content = text
						m.setCurrentMessages(msgs)

						if chat, ok := m.currentChat(); ok && chat.Address != "" && m.sender != nil && msgs[m.editingMsgIdx].ID != "" {
							_, err := m.sender.Send(m.currentAccount, chat.Address, text, SendOptions{
								ReplaceID: msgs[m.editingMsgIdx].ID,
							})
							if err != nil {
								cmds = append(cmds, m.showNotification("edit not delivered: "+err.Error()))
							}
						}
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

					var sendOpts SendOptions
					if m.replyToIdx >= 0 {
						rt := m.replyToIdx
						newMsg.ReplyTo = &rt
						if msgs := m.currentMessages(); rt < len(msgs) && msgs[rt].ID != "" {
							sendOpts = SendOptions{
								ReplyToID:    msgs[rt].ID,
								QuotedAuthor: msgs[rt].Author,
								QuotedBody:   msgs[rt].Content,
							}
						}
						m.replyToIdx = -1
					}

					if chat, ok := m.currentChat(); ok && chat.Address != "" && m.sender != nil {
						id, err := m.sender.Send(m.currentAccount, chat.Address, text, sendOpts)
						if err != nil {
							cmds = append(cmds, m.showNotification("send failed: "+err.Error()))
						} else {
							newMsg.ID = id
						}
					}

					msgs := append(m.currentMessages(), newMsg)
					m.setCurrentMessages(msgs)
				}

				m.input.SetValue("")
				m.updateSizes()
				m.refreshViewport()
				m.viewport.GotoBottom()
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
				oldOffset := m.viewport.YOffset()
				var viewportCmd tea.Cmd
				m.viewport, viewportCmd = m.viewport.Update(msg)
				cmds = append(cmds, viewportCmd)
				// Keep the message selection in sync with whatever paging
				// just scrolled into view, instead of leaving it pointing
				// at a message that's no longer on screen. Only do this if
				// paging actually moved the viewport — e.g. short chats that
				// fit on screen have nothing to page, and pgup/pgdown must
				// leave the current selection untouched in that case.
				newOffset := m.viewport.YOffset()
				if len(m.msgOffsets) > 0 && newOffset != oldOffset {
					m.selectedMsg = m.msgIndexAtOffset(newOffset)
					m.refreshViewport()
					m.viewport.SetYOffset(newOffset)
				}
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
				if m.currentChatIndex() < 0 {
					return m, nil
				}
				if len(m.currentMessages()) > 0 {
					m.confirmTarget = confirmDeleteMessage
				} else {
					cmds = append(cmds, m.showNotification("no messages to delete"))
				}
				return m, tea.Batch(cmds...)
			}
			if m.selectedView == viewChats {
				if m.currentChatIndex() >= 0 {
					m.confirmTarget = confirmDeleteChat
				} else {
					cmds = append(cmds, m.showNotification("no chat selected"))
				}
				return m, tea.Batch(cmds...)
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
				} else {
					cmds = append(cmds, m.showNotification("can only edit your last message"))
				}
				return m, tea.Batch(cmds...)
			}

		case key.Matches(msg, m.keys.ReplyMsg):
			if m.selectedView == viewChat {
				if m.currentChatIndex() < 0 {
					return m, nil
				}
				if len(m.currentMessages()) > 0 {
					if m.replyToIdx == m.selectedMsg {
						m.replyToIdx = -1 // pressed again on the same message: clear reply
					} else {
						m.replyToIdx = m.selectedMsg
					}
					m.updateSizes()
					m.refreshViewport()
					cmds = append(cmds, m.input.Focus())
				} else {
					cmds = append(cmds, m.showNotification("no message to reply to"))
				}
				return m, tea.Batch(cmds...)
			}

		case key.Matches(msg, m.keys.InfoMsg):
			if m.selectedView == viewChat {
				msgs := m.currentMessages()
				if m.selectedMsg >= 0 && m.selectedMsg < len(msgs) {
					m.showMsgInfo = true
				} else {
					cmds = append(cmds, m.showNotification("no message selected"))
				}
				return m, tea.Batch(cmds...)
			}

		case key.Matches(msg, m.keys.OpenMsg):
			if m.selectedView == viewChat {
				msgs := m.currentMessages()
				if m.selectedMsg < 0 || m.selectedMsg >= len(msgs) {
					cmds = append(cmds, m.showNotification("no message selected"))
					return m, tea.Batch(cmds...)
				}
				items := openableItems(msgs[m.selectedMsg])
				switch len(items) {
				case 0:
					cmds = append(cmds, m.showNotification("nothing to open"))
				case 1:
					cmds = append(cmds, openWithXDGOpen(items[0]))
				default:
					m.openItems = items
					m.openPage = 0
				}
				return m, tea.Batch(cmds...)
			}

		case key.Matches(msg, m.keys.QuickReact):
			if m.selectedView == viewChat {
				msgs := m.currentMessages()
				if m.selectedMsg < 0 || m.selectedMsg >= len(msgs) {
					cmds = append(cmds, m.showNotification("no message selected"))
					return m, tea.Batch(cmds...)
				}
				newMine := toggleEmoji(mineEmojis(msgs[m.selectedMsg].Reactions), "👍")
				cmds = append(cmds, m.sendReaction(m.selectedMsg, newMine))
				m.refreshViewport()
				return m, tea.Batch(cmds...)
			}

		case key.Matches(msg, m.keys.ReactMsg):
			if m.selectedView == viewChat {
				msgs := m.currentMessages()
				if m.selectedMsg < 0 || m.selectedMsg >= len(msgs) {
					cmds = append(cmds, m.showNotification("no message selected"))
					return m, tea.Batch(cmds...)
				}
				m.reactingMsgIdx = m.selectedMsg
				m.input.SetValue("")
				m.input.Placeholder = "react: :shortcode: or emoji, enter to send..."
				m.setEmojiSuggestions(nil)
				m.updateSizes()
				cmds = append(cmds, m.input.Focus())
				return m, tea.Batch(cmds...)
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
		if m.reactingMsgIdx >= 0 {
			if token, _, ok := currentWordToken(m.input.Value()); ok {
				m.setEmojiSuggestions(emojiSuggestionsFor(token))
			} else {
				m.setEmojiSuggestions(nil)
			}
		}
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

// digitKey reports whether msg is a single '1'-'9' keypress, and which digit.
func digitKey(msg tea.KeyMsg) (int, bool) {
	s := msg.String()
	if len(s) != 1 || s[0] < '1' || s[0] > '9' {
		return 0, false
	}
	return int(s[0] - '0'), true
}
