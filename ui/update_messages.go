package ui

import (
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"
)

// handleEventMsg handles every non-key message Update can receive (window
// resizes, mouse events, and all the async network/timer messages). handled
// is false only when msg isn't one of these — Update then falls through to
// key handling and focused-component routing.
func (m Model) handleEventMsg(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.termHeight = msg.Height
		m.updateSizes()
		m.refreshViewport()
		m.viewport.GotoBottom()
		return m, nil, true

	case tea.MouseClickMsg:
		if !m.mouseEnabled {
			return m, nil, true
		}
		model, cmd := m.handleMouseClick(msg)
		return model.(Model), cmd, true

	case tea.MouseWheelMsg:
		if !m.mouseEnabled {
			return m, nil, true
		}
		model, cmd := m.handleMouseWheel(msg)
		return model.(Model), cmd, true

	case tea.MouseMotionMsg:
		if !m.mouseEnabled {
			return m, nil, true
		}
		model, cmd := m.handleMouseMotion(msg)
		return model.(Model), cmd, true

	case tea.MouseReleaseMsg:
		if !m.mouseEnabled {
			return m, nil, true
		}
		model, cmd := m.handleMouseRelease(msg)
		return model.(Model), cmd, true

	case noticeClearMsg:
		if msg.id == m.noticeID {
			m.noticeText = ""
		}
		return m, nil, true

	case openResultMsg:
		if msg.err != nil {
			return m, m.showNotification("failed to open " + msg.target), true
		}
		return m, m.showNotification("opened " + msg.target), true

	case saveResultMsg:
		if msg.err != nil {
			return m, m.showNotification("save failed: " + msg.err.Error()), true
		}
		return m, m.showNotification("saved " + msg.path), true

	case FileSendResultMsg:
		if msg.Err != nil {
			return m, m.showNotification("file send failed: " + msg.Err.Error()), true
		}
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.To)
		if chatIdx < 0 {
			return m, m.showNotification("file sent: " + filepath.Base(msg.Path)), true
		}
		if m.accounts[msg.AccountIdx].Messages == nil {
			m.accounts[msg.AccountIdx].Messages = make(map[int][]Message)
		}
		msgs := m.accounts[msg.AccountIdx].Messages[chatIdx]
		newMsg := Message{
			ID:          msg.ID,
			Author:      "me",
			Content:     msg.URL,
			SentAt:      time.Now(),
			IsMe:        true,
			Attachments: []string{msg.URL},
		}
		if replyIdx := messageIndexByID(msgs, msg.ReplyToID); replyIdx >= 0 {
			newMsg.ReplyTo = &replyIdx
		}
		msgs = append(msgs, newMsg)
		m.accounts[msg.AccountIdx].Messages[chatIdx] = msgs
		if msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
			m.selectedMsg = len(msgs) - 1
			m.refreshViewport()
			m.viewport.GotoBottom()
		}
		return m, m.showNotification("file sent: " + filepath.Base(msg.Path)), true

	case IncomingMessageMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil, true
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
		return m, nil, true

	case MessageCorrectedMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil, true
		}
		msgs := m.accounts[msg.AccountIdx].Messages[chatIdx]
		idx := messageIndexByID(msgs, msg.ReplaceID)
		if idx < 0 {
			return m, nil, true
		}
		msgs[idx].Content = msg.NewContent
		if msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
			m.refreshViewport()
		}
		return m, nil, true

	case MessageRetractedMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil, true
		}
		msgs := m.accounts[msg.AccountIdx].Messages[chatIdx]
		idx := messageIndexByID(msgs, msg.RetractID)
		if idx < 0 {
			return m, nil, true
		}
		msgs[idx].Retracted = true
		if msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
			m.refreshViewport()
		}
		return m, nil, true

	case MessageReactionsMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil, true
		}
		msgs := m.accounts[msg.AccountIdx].Messages[chatIdx]
		idx := messageIndexByID(msgs, msg.MessageID)
		if idx < 0 {
			return m, nil, true
		}
		msgs[idx].Reactions = msg.Reactions
		if msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
			m.refreshViewport()
		}
		return m, nil, true

	case AccountAddedMsg:
		m.addingAccount = false
		m.addAccountBusy = false
		m.accounts = append(m.accounts, msg.Account)
		return m, m.showNotification("account added: " + msg.Account.Name), true

	case AccountAddErrorMsg:
		m.addAccountBusy = false
		m.addAccountErr = msg.Err.Error()
		return m, nil, true

	case AccountConnectedMsg:
		if msg.Index < 0 || msg.Index >= len(m.accounts) {
			return m, nil, true
		}
		m.accounts[msg.Index] = msg.Account
		var cmd tea.Cmd
		if msg.Index == m.currentAccount {
			cmd = m.chats.SetItems(msg.Account.Chats)
			m.refreshViewport()
			cmd = tea.Batch(cmd, m.openPendingChat())
		}
		return m, cmd, true

	case AccountLiveMsg:
		if msg.Index < 0 || msg.Index >= len(m.accounts) {
			return m, nil, true
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
			cmd = tea.Batch(cmd, m.openPendingChat())
		}
		return m, cmd, true

	case AccountConnectErrorMsg:
		if msg.Index < 0 || msg.Index >= len(m.accounts) {
			return m, nil, true
		}
		m.accounts[msg.Index].Connecting = false
		m.accounts[msg.Index].ConnectError = msg.Err.Error()
		if msg.Index == m.currentAccount {
			m.refreshViewport()
		}
		return m, m.showNotification("account " + m.accounts[msg.Index].Name + " failed to connect: " + msg.Err.Error()), true

	case HistorySyncedMsg:
		if len(msg.Messages) == 0 {
			return m, nil, true
		}
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil, true
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
		return m, nil, true

	case typingPauseMsg:
		if m.sender != nil && m.typingActiveTo == msg.addr && m.typingGen == msg.gen {
			if err := m.sender.SetTyping(m.currentAccount, msg.addr, false); err == nil {
				m.typingActiveTo = ""
			}
		}
		return m, nil, true

	case TypingMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil, true
		}
		chat, ok := m.accounts[msg.AccountIdx].Chats[chatIdx].(Chat)
		if !ok {
			return m, nil, true
		}
		chat.Typing = msg.Typing
		m.accounts[msg.AccountIdx].Chats[chatIdx] = chat
		if msg.AccountIdx == m.currentAccount {
			cmd := m.chats.SetItem(chatIdx, chat)
			return m, cmd, true
		}
		return m, nil, true

	case PresenceMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil, true
		}
		chat, ok := m.accounts[msg.AccountIdx].Chats[chatIdx].(Chat)
		if !ok {
			return m, nil, true
		}
		chat.Presence = msg.Presence
		m.accounts[msg.AccountIdx].Chats[chatIdx] = chat
		if msg.AccountIdx == m.currentAccount {
			cmd := m.chats.SetItem(chatIdx, chat)
			return m, cmd, true
		}
		return m, nil, true
	}

	return m, nil, false
}
