package ui

import (
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
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

	case tea.MouseClickMsg:
		if !m.mouseEnabled {
			return m, nil
		}
		return m.handleMouseClick(msg)

	case tea.MouseWheelMsg:
		if !m.mouseEnabled {
			return m, nil
		}
		return m.handleMouseWheel(msg)

	case tea.MouseMotionMsg:
		if !m.mouseEnabled {
			return m, nil
		}
		return m.handleMouseMotion(msg)

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

	case saveResultMsg:
		if msg.err != nil {
			return m, m.showNotification("save failed: " + msg.err.Error())
		}
		return m, m.showNotification("saved " + msg.path)

	case FileSendResultMsg:
		if msg.Err != nil {
			return m, m.showNotification("file send failed: " + msg.Err.Error())
		}
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.To)
		if chatIdx < 0 {
			return m, m.showNotification("file sent: " + filepath.Base(msg.Path))
		}
		if m.accounts[msg.AccountIdx].Messages == nil {
			m.accounts[msg.AccountIdx].Messages = make(map[int][]Message)
		}
		msgs := m.accounts[msg.AccountIdx].Messages[chatIdx]
		msgs = append(msgs, Message{
			ID:          msg.ID,
			Author:      "me",
			Content:     msg.URL,
			SentAt:      time.Now(),
			IsMe:        true,
			Attachments: []string{msg.URL},
		})
		m.accounts[msg.AccountIdx].Messages[chatIdx] = msgs
		if msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
			m.selectedMsg = len(msgs) - 1
			m.refreshViewport()
			m.viewport.GotoBottom()
		}
		return m, m.showNotification("file sent: " + filepath.Base(msg.Path))

	case IncomingMessageMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil
		}
		if m.accounts[msg.AccountIdx].Messages == nil {
			m.accounts[msg.AccountIdx].Messages = make(map[int][]Message)
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

	case AccountAddedMsg:
		m.addingAccount = false
		m.addAccountBusy = false
		m.accounts = append(m.accounts, msg.Account)
		return m, m.showNotification("account added: " + msg.Account.Name)

	case AccountAddErrorMsg:
		m.addAccountBusy = false
		m.addAccountErr = msg.Err.Error()
		return m, nil

	case AccountConnectedMsg:
		if msg.Index < 0 || msg.Index >= len(m.accounts) {
			return m, nil
		}
		m.accounts[msg.Index] = msg.Account
		var cmd tea.Cmd
		if msg.Index == m.currentAccount {
			cmd = m.chats.SetItems(msg.Account.Chats)
			m.refreshViewport()
		}
		return m, cmd

	case AccountLiveMsg:
		if msg.Index < 0 || msg.Index >= len(m.accounts) {
			return m, nil
		}
		m.accounts[msg.Index].Connecting = false
		m.accounts[msg.Index].ConnectError = ""
		if len(msg.NewChats) > 0 {
			m.accounts[msg.Index].Chats = append(m.accounts[msg.Index].Chats, msg.NewChats...)
			if m.accounts[msg.Index].Messages == nil {
				m.accounts[msg.Index].Messages = make(map[int][]Message)
			}
			for idx, msgs := range msg.NewMessages {
				m.accounts[msg.Index].Messages[idx] = msgs
			}
		}
		var cmd tea.Cmd
		if msg.Index == m.currentAccount {
			if len(msg.NewChats) > 0 {
				cmd = m.chats.SetItems(m.accounts[msg.Index].Chats)
			}
			m.refreshViewport()
		}
		return m, cmd

	case AccountConnectErrorMsg:
		if msg.Index < 0 || msg.Index >= len(m.accounts) {
			return m, nil
		}
		m.accounts[msg.Index].Connecting = false
		m.accounts[msg.Index].ConnectError = msg.Err.Error()
		if msg.Index == m.currentAccount {
			m.refreshViewport()
		}
		return m, m.showNotification("account " + m.accounts[msg.Index].Name + " failed to connect: " + msg.Err.Error())

	case HistorySyncedMsg:
		if len(msg.Messages) == 0 {
			return m, nil
		}
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil
		}
		if m.accounts[msg.AccountIdx].Messages == nil {
			m.accounts[msg.AccountIdx].Messages = make(map[int][]Message)
		}
		msgs := append(m.accounts[msg.AccountIdx].Messages[chatIdx], msg.Messages...)
		m.accounts[msg.AccountIdx].Messages[chatIdx] = msgs
		if msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
			m.selectedMsg = len(msgs) - 1
			m.refreshViewport()
			m.viewport.GotoBottom()
		}
		return m, nil

	case typingPauseMsg:
		if m.sender != nil && m.typingActiveTo == msg.addr && m.typingGen == msg.gen {
			if err := m.sender.SetTyping(m.currentAccount, msg.addr, false); err == nil {
				m.typingActiveTo = ""
			}
		}
		return m, nil

	case TypingMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil
		}
		chat, ok := m.accounts[msg.AccountIdx].Chats[chatIdx].(Chat)
		if !ok {
			return m, nil
		}
		chat.Typing = msg.Typing
		m.accounts[msg.AccountIdx].Chats[chatIdx] = chat
		if msg.AccountIdx == m.currentAccount {
			var cmd tea.Cmd
			cmd = m.chats.SetItem(chatIdx, chat)
			if chatIdx == m.currentChatIndex() {
				cmds = append(cmds, cmd)
				return m, tea.Batch(cmds...)
			}
			return m, cmd
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
		// ── Context menu intercepts all input until dismissed ───────────────
		// It's mouse-only otherwise (every action it lists already has its
		// own keybinding), so the keyboard's only job here is closing it.
		if m.contextMenu != nil {
			switch {
			case key.Matches(msg, m.keys.Back), key.Matches(msg, m.keys.ConfirmNo):
				m.closeContextMenu()
			}
			return m, nil
		}

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

		// ── Add-account form intercepts all input until submitted/canceled ──
		if m.addingAccount {
			return m.updateAddAccountForm(msg)
		}

		// ── Rename-chat prompt intercepts all input until submitted/canceled ──
		if m.renamingChat {
			return m.updateRenameChatForm(msg)
		}

		// ── File picker intercepts all input until selected or canceled ─────
		if m.pickingFile {
			if key.Matches(msg, m.keys.AttachFile) || key.Matches(msg, m.keys.Back) || key.Matches(msg, m.keys.ConfirmNo) {
				m.pickingFile = false
				return m, nil
			}
			var pickerCmd tea.Cmd
			m.filePicker, pickerCmd = m.filePicker.Update(msg)
			if selected, path := m.filePicker.DidSelectFile(msg); selected {
				m.pickingFile = false
				if m.fileSender == nil {
					return m, m.showNotification("file sending unavailable")
				}
				chat, ok := m.currentChat()
				if !ok || chat.Address == "" {
					return m, m.showNotification("no chat selected")
				}
				accountIdx, to := m.currentAccount, chat.Address
				// Uploading can take a while and runs asynchronously; make the
				// accepted selection visible immediately instead of leaving the
				// user looking at an unchanged chat.
				m.noticeID++
				m.noticeText = "uploading " + filepath.Base(path) + "..."
				return m, func() tea.Msg { return m.fileSender.SendFile(accountIdx, to, path) }
			}
			if disabled, _ := m.filePicker.DidSelectDisabledFile(msg); disabled {
				cmds = append(cmds, m.showNotification("that file type cannot be selected"))
			}
			return m, tea.Batch(append(cmds, pickerCmd)...)
		}

		// ── Open-item picker intercepts all input until a choice is made ───
		if len(m.openItems) > 0 {
			start, end := openPageBounds(len(m.openItems), m.openPage)
			if i, ok := digitKey(msg); ok && i >= 1 && i <= end-start {
				target := m.openItems[start+i-1]
				m.openItems = nil
				m.openPage = 0
				if m.openMode == pickerModeSave {
					return m, saveURLToDownloads(target)
				}
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

		case (msg.String() == "tab" || key.Matches(msg, m.keys.SelectSend)) && m.reactingMsgIdx >= 0 && len(m.emojiSuggestions) > 0:
			// While a suggestion is showing, enter accepts it (like tab)
			// instead of falling through to SelectSend and sending the
			// reaction early — matches the emoji picker, not a message send.
			m.acceptEmojiSuggestion(m.emojiSuggestIdx)
			return m, nil

		// ── Global ────────────────────────────────────────────────────────
		case key.Matches(msg, m.keys.Quit):
			if msg.String() == "ctrl+c" || m.selectedView != viewChat {
				return m, tea.Quit
			}

		case key.Matches(msg, m.keys.Back):
			if m.selectedView != viewAccounts && m.selectedView != viewChats {
				m.notifyTypingStopped()
				m.cancelPending()
				m.selectedView = viewChats
				m.input.Blur()
				return m, nil
			}

		case key.Matches(msg, m.keys.FocusChats):
			m.notifyTypingStopped()
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
				return m, m.sendCurrentInput()
			}

		case key.Matches(msg, m.keys.AddAccount):
			if m.selectedView == viewAccounts {
				m.addingAccount = true
				m.addAccountFocus = 0
				m.addAccountErr = ""
				m.addAccountInputs = m.newAddAccountForm()
				return m, textinput.Blink
			}

		case key.Matches(msg, m.keys.RenameChat):
			if m.selectedView == viewChats {
				return m, m.actionRenameChat()
			}

		case key.Matches(msg, m.keys.AttachFile):
			if m.selectedView == viewChat {
				if m.currentChatIndex() < 0 {
					cmds = append(cmds, m.showNotification("no chat selected"))
					return m, tea.Batch(cmds...)
				}
				m.pickingFile = true
				m.filePicker.SetHeight(max(1, m.height-m.inputAreaHeight()-6))
				return m, m.filePicker.Init()
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
			switch m.selectedView {
			case viewChat:
				return m, m.actionDeleteMessage()
			case viewChats:
				return m, m.actionLeaveChat()
			}

		case key.Matches(msg, m.keys.YankMsg):
			if m.selectedView == viewChat {
				return m, m.actionYankMessage()
			}

		case key.Matches(msg, m.keys.EditMsg):
			if m.selectedView == viewChat {
				return m, m.actionEditMessage()
			}

		case key.Matches(msg, m.keys.ReplyMsg):
			if m.selectedView == viewChat {
				return m, m.actionReplyMessage()
			}

		case key.Matches(msg, m.keys.InfoMsg):
			if m.selectedView == viewChat {
				return m, m.actionInfoMessage()
			}

		case key.Matches(msg, m.keys.OpenMsg):
			if m.selectedView == viewChat {
				return m, m.actionOpenMessage()
			}

		case key.Matches(msg, m.keys.SaveMsg):
			if m.selectedView == viewChat {
				return m, m.actionSaveMessage()
			}

		case key.Matches(msg, m.keys.ReactMsg):
			if m.selectedView == viewChat {
				return m, m.actionReactMessage()
			}
		}
	}

	// Route remaining events to the focused component. Checked before the
	// m.selectedView switch below since the add-account form floats on top
	// of viewAccounts: non-key messages (bracketed-paste text, the
	// textinput cursor-blink tick) aren't caught by the key interception
	// above (that only matches tea.KeyMsg), so without this they'd fall
	// through to the "Account focus is handled by global keys only" case
	// and silently vanish — e.g. paste never reaching the focused field.
	if m.addingAccount {
		var cmd tea.Cmd
		m.addAccountInputs[m.addAccountFocus], cmd = m.addAccountInputs[m.addAccountFocus].Update(msg)
		return m, tea.Batch(append(cmds, cmd)...)
	}
	if m.renamingChat {
		var cmd tea.Cmd
		m.renameInput, cmd = m.renameInput.Update(msg)
		return m, tea.Batch(append(cmds, cmd)...)
	}
	if m.pickingFile {
		// Directory reads are asynchronous messages, not key presses, so they
		// bypass the key-interception block above and must still reach the
		// picker. Without this, the picker remains permanently empty.
		var cmd tea.Cmd
		m.filePicker, cmd = m.filePicker.Update(msg)
		return m, tea.Batch(append(cmds, cmd)...)
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
		oldValue := m.input.Value()
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
		// This branch runs for every message type routed here, including
		// the textinput cursor-blink tick — not just keystrokes. Recomputing
		// (and resetting the highlighted suggestion) on every blink would
		// undo arrow-key navigation a few hundred ms after every move, so
		// only do it when the text itself actually changed.
		if m.reactingMsgIdx >= 0 && m.input.Value() != oldValue {
			if token, _, ok := currentWordToken(m.input.Value()); ok {
				m.setEmojiSuggestions(emojiSuggestionsFor(token))
			} else {
				m.setEmojiSuggestions(nil)
			}
		}
		cmds = append(cmds, m.notifyTypingChanged(oldValue))
	}

	return m, tea.Batch(cmds...)
}

// notifyTypingChanged reacts to a keystroke in the compose input by sending
// a XEP-0085 chat-state update when needed, and returns a cmd that arms (or
// re-arms) the pause timeout — see typingPauseTimer. Skipped while composing
// a reaction (m.reactingMsgIdx >= 0): that's not really "typing a message".
//
//   - Input went from empty to non-empty, or we'd previously paused/stopped
//     (m.typingActiveTo isn't this chat): send "composing" and start the
//     pause timer.
//   - Input is still non-empty and we're already marked composing to this
//     chat: no new stanza (a held-down key shouldn't resend "composing"
//     every tick) — just re-arm the pause timer so it doesn't fire early.
//   - Input went empty: send "stopped" and don't arm a timer.
func (m *Model) notifyTypingChanged(oldValue string) tea.Cmd {
	if m.sender == nil || m.reactingMsgIdx >= 0 {
		return nil
	}
	chat, ok := m.currentChat()
	if !ok || chat.Address == "" {
		return nil
	}
	newValue := m.input.Value()
	if newValue == oldValue {
		return nil
	}

	if newValue == "" {
		if m.typingActiveTo == chat.Address {
			if err := m.sender.SetTyping(m.currentAccount, chat.Address, false); err == nil {
				m.typingActiveTo = ""
			}
		}
		return nil
	}

	if m.typingActiveTo != chat.Address {
		if err := m.sender.SetTyping(m.currentAccount, chat.Address, true); err != nil {
			return nil
		}
		m.typingActiveTo = chat.Address
	}
	m.typingGen++
	return typingPauseTimer(chat.Address, m.typingGen)
}

// notifyTypingStopped tells the peer we've stopped composing, if we'd told
// them otherwise — called wherever the input is cleared programmatically
// (message actually sent, reaction sent) or the user navigates away from the
// chat, none of which go through notifyTypingChanged's normal keystroke path.
func (m *Model) notifyTypingStopped() {
	if m.sender == nil || m.typingActiveTo == "" {
		return
	}
	if err := m.sender.SetTyping(m.currentAccount, m.typingActiveTo, false); err == nil {
		m.typingActiveTo = ""
	}
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

// updateRenameChatForm handles all key input while the rename-chat prompt is
// open: enter submits (see submitRenameChat), esc cancels. Only esc cancels —
// not the full Back/ConfirmNo bindings, since those also bind plain letters
// ("n") that must reach the focused text field while typing a name.
func (m Model) updateRenameChatForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.String() == "esc":
		m.renamingChat = false
		return m, nil

	case key.Matches(msg, m.keys.SelectSend):
		return m, m.submitRenameChat()

	default:
		var cmd tea.Cmd
		m.renameInput, cmd = m.renameInput.Update(msg)
		return m, cmd
	}
}

// updateAddAccountForm handles all key input while the add-account popup is
// open: tab/shift+tab cycles the three fields, enter on any field submits
// (password/gpg key are optional), esc cancels.
func (m Model) updateAddAccountForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	// Only esc cancels — not the full Back/ConfirmNo bindings, since those
	// also bind plain letters ("n") that must reach the focused text field
	// while typing instead of being swallowed as a cancel key here.
	case msg.String() == "esc":
		if !m.addAccountBusy {
			m.addingAccount = false
			return m, nil
		}
		return m, nil

	case msg.String() == "tab", msg.String() == "down":
		m.addAccountInputs[m.addAccountFocus].Blur()
		m.addAccountFocus = (m.addAccountFocus + 1) % len(m.addAccountInputs)
		m.addAccountInputs[m.addAccountFocus].Focus()
		return m, textinput.Blink

	case msg.String() == "shift+tab", msg.String() == "up":
		m.addAccountInputs[m.addAccountFocus].Blur()
		m.addAccountFocus = (m.addAccountFocus - 1 + len(m.addAccountInputs)) % len(m.addAccountInputs)
		m.addAccountInputs[m.addAccountFocus].Focus()
		return m, textinput.Blink

	case key.Matches(msg, m.keys.SelectSend):
		if m.addAccountBusy {
			return m, nil
		}
		jid := strings.TrimSpace(m.addAccountInputs[0].Value())
		if jid == "" {
			m.addAccountErr = "JID is required"
			return m, nil
		}
		if m.accountAdder == nil {
			m.addAccountErr = "no account adder configured"
			return m, nil
		}
		password := m.addAccountInputs[1].Value()
		gpgKeyID := strings.TrimSpace(m.addAccountInputs[2].Value())
		m.addAccountBusy = true
		m.addAccountErr = ""
		adder := m.accountAdder
		return m, func() tea.Msg { return adder.AddAccount(jid, password, gpgKeyID) }

	default:
		var cmd tea.Cmd
		m.addAccountInputs[m.addAccountFocus], cmd = m.addAccountInputs[m.addAccountFocus].Update(msg)
		return m, cmd
	}
}
