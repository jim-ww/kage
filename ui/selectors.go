package ui

import tea "charm.land/bubbletea/v2"

func (m Model) currentChatIndex() int {
	idx := m.chats.GlobalIndex()
	if idx < 0 || idx >= len(m.chats.Items()) {
		return -1
	}
	return idx
}

func (m Model) currentMessages() []Message {
	if m.currentAccount < 0 || m.currentAccount >= len(m.accounts) {
		return nil
	}
	chatIdx := m.currentChatIndex()
	if chatIdx < 0 {
		return nil
	}
	return m.accounts[m.currentAccount].Messages[chatIdx]
}

// currentAccountConnecting reports whether the current account's background
// connect (see AccountConnectedMsg/AccountLiveMsg) hasn't finished yet.
func (m Model) currentAccountConnecting() bool {
	return m.currentAccount >= 0 && m.currentAccount < len(m.accounts) && m.accounts[m.currentAccount].Connecting
}

func (m Model) currentChat() (Chat, bool) {
	if chatIdx := m.currentChatIndex(); chatIdx >= 0 {
		if chat, ok := m.chats.Items()[chatIdx].(Chat); ok {
			return chat, true
		}
	}
	return Chat{}, false
}

func (m *Model) setCurrentMessages(msgs []Message) {
	if m.currentAccount < 0 || m.currentAccount >= len(m.accounts) {
		return
	}
	chatIdx := m.currentChatIndex()
	if chatIdx < 0 {
		return
	}
	if m.accounts[m.currentAccount].Messages == nil {
		m.accounts[m.currentAccount].Messages = make(map[int][]Message)
	}
	m.accounts[m.currentAccount].Messages[chatIdx] = msgs
}

// chatIndexByAddress returns the index of the chat with the given address
// within the given account, or -1 if none matches.
func (m Model) chatIndexByAddress(accountIdx int, address string) int {
	if accountIdx < 0 || accountIdx >= len(m.accounts) {
		return -1
	}
	for i, item := range m.accounts[accountIdx].Chats {
		if chat, ok := item.(Chat); ok && chat.Address == address {
			return i
		}
	}
	return -1
}

// maybeLoadOlderHistory fires a HistoryLoader fetch for the current chat's
// next older page, if one is configured, more history is known to exist,
// and a fetch isn't already in flight for this chat. Called when the
// message selection/viewport reaches the top of what's currently loaded.
func (m *Model) maybeLoadOlderHistory() tea.Cmd {
	if m.historyLoader == nil {
		return nil
	}
	chatIdx := m.currentChatIndex()
	if chatIdx < 0 || m.currentAccount < 0 || m.currentAccount >= len(m.accounts) {
		return nil
	}
	if !m.accounts[m.currentAccount].HistoryMore[chatIdx] || m.loadingOlderHistory[chatIdx] {
		return nil
	}
	chat, ok := m.currentChat()
	if !ok {
		return nil
	}
	m.loadingOlderHistory[chatIdx] = true
	return m.historyLoader.LoadOlderHistory(m.currentAccount, chat.Address)
}

// messageIndexByID returns the index of the message with the given stanza ID
// within msgs, or -1 if none matches (or id is empty).
func messageIndexByID(msgs []Message, id string) int {
	if id == "" {
		return -1
	}
	for i, msg := range msgs {
		if msg.ID == id {
			return i
		}
	}
	return -1
}
