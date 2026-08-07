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

// setChatLastMessage updates the chat list preview text for the chat at
// chatIdx and, if that chat's account is currently displayed, refreshes the
// visible list item.
func (m *Model) setChatLastMessage(accountIdx, chatIdx int, content string) tea.Cmd {
	if accountIdx < 0 || accountIdx >= len(m.accounts) {
		return nil
	}
	chat, ok := m.accounts[accountIdx].Chats[chatIdx].(Chat)
	if !ok {
		return nil
	}
	chat.LastMessage = content
	m.accounts[accountIdx].Chats[chatIdx] = chat
	if accountIdx == m.currentAccount {
		return m.chats.SetItem(chatIdx, chat)
	}
	return nil
}

// setChatUnread updates the chat list's unread count for the chat at
// chatIdx and, if that chat's account is currently displayed, refreshes the
// visible list item.
func (m *Model) setChatUnread(accountIdx, chatIdx, count int) tea.Cmd {
	if accountIdx < 0 || accountIdx >= len(m.accounts) {
		return nil
	}
	chat, ok := m.accounts[accountIdx].Chats[chatIdx].(Chat)
	if !ok {
		return nil
	}
	chat.Unread = count
	m.accounts[accountIdx].Chats[chatIdx] = chat
	if accountIdx == m.currentAccount {
		return m.chats.SetItem(chatIdx, chat)
	}
	return nil
}

// isChatFocused reports whether chatIdx within accountIdx is the chat
// currently being actively viewed — the condition under which an incoming
// message counts as already read rather than unread.
func (m Model) isChatFocused(accountIdx, chatIdx int) bool {
	return accountIdx == m.currentAccount && chatIdx == m.currentChatIndex() && m.selectedView == viewChat
}

// incrementChatUnread bumps the in-memory unread count for chatIdx by delta
// and, if a ChatReadTracker is wired in, persists the bump so it survives a
// restart. Best-effort: a persistence failure doesn't roll back the
// in-memory count.
func (m *Model) incrementChatUnread(accountIdx, chatIdx, delta int) tea.Cmd {
	if accountIdx < 0 || accountIdx >= len(m.accounts) || delta == 0 {
		return nil
	}
	chat, ok := m.accounts[accountIdx].Chats[chatIdx].(Chat)
	if !ok {
		return nil
	}
	cmd := m.setChatUnread(accountIdx, chatIdx, chat.Unread+delta)
	if m.chatReadTracker == nil {
		return cmd
	}
	accountJID, address := m.accounts[accountIdx].Name, chat.Address
	tracker := m.chatReadTracker
	return tea.Batch(cmd, func() tea.Msg {
		_ = tracker.IncrementChatUnread(accountJID, address, delta)
		return nil
	})
}

// resetChatUnread zeroes chatIdx's unread count, in memory and (if wired)
// persisted, called when the chat becomes the actively-focused one.
func (m *Model) resetChatUnread(accountIdx, chatIdx int) tea.Cmd {
	if accountIdx < 0 || accountIdx >= len(m.accounts) {
		return nil
	}
	chat, ok := m.accounts[accountIdx].Chats[chatIdx].(Chat)
	if !ok || chat.Unread == 0 {
		return nil
	}
	cmd := m.setChatUnread(accountIdx, chatIdx, 0)
	if m.chatReadTracker == nil {
		return cmd
	}
	accountJID, address := m.accounts[accountIdx].Name, chat.Address
	tracker := m.chatReadTracker
	return tea.Batch(cmd, func() tea.Msg {
		_ = tracker.ResetChatUnread(accountJID, address)
		return nil
	})
}

// setChatDraft updates the chat list's stored draft text for the chat at
// chatIdx and, if that chat's account is currently displayed, refreshes the
// visible list item. A no-op if the draft didn't actually change.
func (m *Model) setChatDraft(accountIdx, chatIdx int, text string) tea.Cmd {
	if accountIdx < 0 || accountIdx >= len(m.accounts) {
		return nil
	}
	chat, ok := m.accounts[accountIdx].Chats[chatIdx].(Chat)
	if !ok || chat.Draft == text {
		return nil
	}
	chat.Draft = text
	m.accounts[accountIdx].Chats[chatIdx] = chat
	if accountIdx == m.currentAccount {
		return m.chats.SetItem(chatIdx, chat)
	}
	return nil
}

// saveChatDraft updates chatIdx's draft in memory and, if a DraftSaver is
// wired in, persists it so it survives a restart. Best-effort: a persistence
// failure doesn't roll back the in-memory draft.
func (m *Model) saveChatDraft(accountIdx, chatIdx int, text string) tea.Cmd {
	if accountIdx < 0 || accountIdx >= len(m.accounts) {
		return nil
	}
	chat, ok := m.accounts[accountIdx].Chats[chatIdx].(Chat)
	if !ok || chat.Draft == text {
		return nil
	}
	cmd := m.setChatDraft(accountIdx, chatIdx, text)
	if m.draftSaver == nil || chat.Address == "" {
		return cmd
	}
	accountJID, address, saver := m.accounts[accountIdx].Name, chat.Address, m.draftSaver
	return tea.Batch(cmd, func() tea.Msg {
		_ = saver.SaveDraft(accountJID, address, text)
		return nil
	})
}

// FlushDraft synchronously persists whatever's currently in the compose box
// as the open chat's draft (skipped if it already matches what's stored).
// The periodic autosave (draftSaveDebounce) only fires after a few idle
// seconds, so quitting mid-type can otherwise lose the last few keystrokes
// that hadn't been flushed yet — call this once, after the Bubble Tea
// program has stopped, before the process exits.
func (m Model) FlushDraft() {
	if m.draftSaver == nil || m.openChatAddress == "" {
		return
	}
	chatIdx := m.chatIndexByAddress(m.openChatAccountIdx, m.openChatAddress)
	if chatIdx < 0 {
		return
	}
	chat, ok := m.accounts[m.openChatAccountIdx].Chats[chatIdx].(Chat)
	if !ok || chat.Draft == m.input.Value() {
		return
	}
	_ = m.draftSaver.SaveDraft(m.accounts[m.openChatAccountIdx].Name, chat.Address, m.input.Value())
}

// swapComposeDraft saves the draft of the chat currently loaded into
// m.input (tracked by openChatAccountIdx/openChatAddress) and loads in
// newAccountIdx/newChatIdx's stored draft instead — called whenever a
// different chat becomes the active one. A same-chat "switch" (reopening the
// chat that's already open) is a no-op, so it can't clobber in-progress
// typing with the last-saved snapshot.
func (m *Model) swapComposeDraft(newAccountIdx, newChatIdx int) tea.Cmd {
	if newAccountIdx < 0 || newAccountIdx >= len(m.accounts) {
		return nil
	}
	newChat, ok := m.accounts[newAccountIdx].Chats[newChatIdx].(Chat)
	if !ok {
		return nil
	}
	if m.openChatAccountIdx == newAccountIdx && m.openChatAddress == newChat.Address {
		return nil
	}

	var cmd tea.Cmd
	if oldIdx := m.chatIndexByAddress(m.openChatAccountIdx, m.openChatAddress); oldIdx >= 0 {
		cmd = m.saveChatDraft(m.openChatAccountIdx, oldIdx, m.input.Value())
	}

	m.input.SetValue(newChat.Draft)
	m.resetDraftHistory(newChat.Draft)
	m.openChatAccountIdx = newAccountIdx
	m.openChatAddress = newChat.Address
	return cmd
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
