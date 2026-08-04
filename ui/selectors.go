package ui

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
