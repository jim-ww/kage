package ui

import (
	"slices"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"
)

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

// maybeLoadHistoryWindow loads a window around edgeMsg, the loaded edge
// reached, if storage has more that way and none is loading. Not for an
// unstored message, only ever the newest.
func (m *Model) maybeLoadHistoryWindow(older bool, edgeMsg Message) tea.Cmd {
	chatIdx := m.currentChatIndex()
	if chatIdx < 0 || m.currentAccount < 0 || m.currentAccount >= len(m.accounts) {
		return nil
	}
	if edgeMsg.StoreID == 0 || m.loadingHistoryWindow[chatIdx] {
		return nil
	}
	acct := m.accounts[m.currentAccount]
	if older && !acct.HistoryMore[chatIdx] {
		return nil
	}
	if !older && !acct.HistoryNewer[chatIdx] {
		return nil
	}
	chat, ok := m.currentChat()
	if !ok {
		return nil
	}
	anchor := &HistoryAnchor{Delay: edgeMsg.SentAt.Unix(), StoreID: edgeMsg.StoreID}
	cmd := m.daemon.LoadHistoryWindow(m.currentAccount, chat.Address, anchor)
	if cmd == nil {
		// No command means no HistoryWindowMsg will ever come back, so
		// marking the fetch in flight would block this chat's paging for the
		// rest of the session. See markHistoryWindowLoading.
		return nil
	}
	m.markHistoryWindowLoading(chatIdx, edgeMsg.ID)
	return cmd
}

// maybeLoadOlderHistory loads older history, at the top of what's loaded.
func (m *Model) maybeLoadOlderHistory() tea.Cmd {
	msgs := m.currentMessages()
	if len(msgs) == 0 {
		return nil
	}
	return m.maybeLoadHistoryWindow(true, msgs[0])
}

// maybeLoadNewerHistory loads newer history, at the bottom of a
// mid-history window.
func (m *Model) maybeLoadNewerHistory() tea.Cmd {
	msgs := m.currentMessages()
	if len(msgs) == 0 {
		return nil
	}
	return m.maybeLoadHistoryWindow(false, msgs[len(msgs)-1])
}

// setChatLastMessage sets a chat's preview and activity, re-sorting the list.
func (m *Model) setChatLastMessage(accountIdx, chatIdx int, content string) tea.Cmd {
	if accountIdx < 0 || accountIdx >= len(m.accounts) {
		return nil
	}
	chat, ok := m.accounts[accountIdx].Chats[chatIdx].(Chat)
	if !ok {
		return nil
	}
	chat.LastMessage = content
	chat.LastActivity = time.Now()
	m.accounts[accountIdx].Chats[chatIdx] = chat
	return m.sortChatsByActivity(accountIdx)
}

// setChatUnread updates the chat list's unread count for the chat at
// chatIdx and, if that chat's account is currently displayed, refreshes the
// visible list item.
func (m *Model) setChatUnread(accountIdx, chatIdx, count int) tea.Cmd {
	if accountIdx < 0 || accountIdx >= len(m.accounts) {
		return nil
	}
	if chatIdx < 0 || chatIdx >= len(m.accounts[accountIdx].Chats) {
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

// accountIndexByJID returns the index of the account with the given JID
// (Account.Name), or -1 if none matches.
func (m Model) accountIndexByJID(jid string) int {
	for i, acct := range m.accounts {
		if acct.Name == jid {
			return i
		}
	}
	return -1
}

// peerTypingKey identifies one contact's typing state across accounts.
func peerTypingKey(accountIdx int, from string) string {
	return strconv.Itoa(accountIdx) + "\x00" + from
}

// setPeerTyping records a contact's XEP-0085 state on their chat row. A
// "composing" also (re)arms the expiry timer that clears the indicator if no
// contradicting state ever arrives — see peerTypingTimeout.
func (m *Model) setPeerTyping(accountIdx int, from string, typing bool) tea.Cmd {
	if accountIdx < 0 || accountIdx >= len(m.accounts) {
		return nil
	}
	chatIdx := m.chatIndexByAddress(accountIdx, from)
	if chatIdx < 0 {
		return nil
	}
	chat, ok := m.accounts[accountIdx].Chats[chatIdx].(Chat)
	if !ok || (!typing && !chat.Typing) {
		return nil
	}
	chat.Typing = typing
	m.accounts[accountIdx].Chats[chatIdx] = chat

	if m.peerTypingGen == nil {
		m.peerTypingGen = map[string]int{}
	}
	key := peerTypingKey(accountIdx, from)
	m.peerTypingGen[key]++
	gen := m.peerTypingGen[key]

	var cmd tea.Cmd
	if accountIdx == m.currentAccount {
		cmd = m.chats.SetItem(chatIdx, chat)
	}
	if !typing {
		return cmd
	}
	return tea.Batch(cmd, peerTypingTimer(accountIdx, from, gen))
}

// activeChatKey is the chat being viewed, "" if none, reported to the
// daemon so it doesn't notify about it.
func (m Model) activeChatKey() (accountJID, chatAddress string) {
	if m.selectedView != viewChat || m.currentAccount < 0 || m.currentAccount >= len(m.accounts) {
		return "", ""
	}
	chat, ok := m.currentChat()
	if !ok {
		return "", ""
	}
	return m.accounts[m.currentAccount].Name, chat.Address
}

// isChatFocused reports whether the chat is being read, so a message counts
// as read: viewed, with focus, not idle - as the daemon decides whether to
// notify. Not for re-rendering, see isChatOnScreen.
func (m Model) isChatFocused(accountIdx, chatIdx int) bool {
	return m.isChatOnScreen(accountIdx, chatIdx) && m.selectedView == viewChat &&
		m.focused && !m.idle
}

// isChatOnScreen reports whether the message pane shows the chat, which then
// needs re-rendering on a change, whatever has focus.
func (m Model) isChatOnScreen(accountIdx, chatIdx int) bool {
	return accountIdx == m.currentAccount && chatIdx == m.currentChatIndex()
}

// resetChatUnread clears a chat's unread count, here and in the daemon - even
// when it's 0 here, which says nothing about the stored count.
func (m *Model) resetChatUnread(accountIdx, chatIdx int) tea.Cmd {
	if accountIdx < 0 || accountIdx >= len(m.accounts) {
		return nil
	}
	if chatIdx < 0 || chatIdx >= len(m.accounts[accountIdx].Chats) {
		return nil
	}
	chat, ok := m.accounts[accountIdx].Chats[chatIdx].(Chat)
	if !ok {
		return nil
	}
	cmd := m.setChatUnread(accountIdx, chatIdx, 0)
	accountJID, address := m.accounts[accountIdx].Name, chat.Address
	tracker := m.daemon
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

// saveChatDraft updates chatIdx's draft in memory and persists it so it
// survives a restart. Best-effort: a persistence
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
	if chat.Address == "" {
		return cmd
	}
	accountJID, address, saver := m.accounts[accountIdx].Name, chat.Address, m.daemon
	return tea.Batch(cmd, func() tea.Msg {
		_ = saver.SaveDraft(accountJID, address, text)
		return nil
	})
}

// FlushDraft saves the open chat's draft if changed, for after the program
// stops.
func (m Model) FlushDraft() {
	if m.openChatAddress == "" {
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
	_ = m.daemon.SaveDraft(m.accounts[m.openChatAccountIdx].Name, chat.Address, m.input.Value())
}

// swapComposeDraft saves the input's draft and loads the new chat's; a no-op
// for the same chat.
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

// msgRef identifies a message by its IDs, not index, for state outliving a
// history load or trim, which move indices.
type msgRef struct {
	id      string // stanza ID; empty until a send actually succeeds
	localID string // client-side correlation key for a locally composed message
	storeID int64  // storage rowid; zero for anything not persisted yet
}

// refMessage builds a msgRef naming msg.
func refMessage(msg Message) msgRef {
	return msgRef{id: msg.ID, localID: msg.LocalID, storeID: msg.StoreID}
}

// empty reports whether r names no message at all - the zero value, used as
// the "not editing anything" state.
func (r msgRef) empty() bool {
	return r.id == "" && r.localID == "" && r.storeID == 0
}

// index locates r within msgs, or -1 if it isn't there any more (trimmed
// off the front, or outside the currently loaded history window).
func (r msgRef) index(msgs []Message) int {
	if i := messageIndexByID(msgs, r.id); i >= 0 {
		return i
	}
	if i := messageIndexByLocalID(msgs, r.localID); i >= 0 {
		return i
	}
	if r.storeID == 0 {
		return -1
	}
	return slices.IndexFunc(msgs, func(msg Message) bool { return msg.StoreID == r.storeID })
}

// messageIndexByID returns the index of the message with the given stanza ID
// within msgs, or -1 if none matches (or id is empty).
func messageIndexByID(msgs []Message, id string) int {
	if id == "" {
		return -1
	}
	return slices.IndexFunc(msgs, func(msg Message) bool { return msg.ID == id })
}

// messageIndexByLocalID is the index of the message with localID, or -1.
func messageIndexByLocalID(msgs []Message, localID string) int {
	if localID == "" {
		return -1
	}
	return slices.IndexFunc(msgs, func(msg Message) bool { return msg.LocalID == localID })
}

// removeMessageAt removes the message at idx, for one never sent, returning
// selectedMsg adjusted to stay on its message.
func removeMessageAt(msgs []Message, idx, selectedMsg int) ([]Message, int) {
	msgs = slices.Delete(msgs, idx, idx+1)
	switch {
	case len(msgs) == 0:
		selectedMsg = -1
	case selectedMsg > idx:
		selectedMsg--
	case selectedMsg == idx && selectedMsg >= len(msgs):
		selectedMsg = len(msgs) - 1
	}
	return msgs, selectedMsg
}

// markHistoryWindowLoading marks a history load for chatIdx in flight. Only
// for a load actually started: only its HistoryWindowMsg clears it.
func (m *Model) markHistoryWindowLoading(chatIdx int, anchorID string) {
	if m.loadingHistoryWindow == nil {
		m.loadingHistoryWindow = make(map[int]bool)
	}
	if m.pendingWindowAnchor == nil {
		m.pendingWindowAnchor = make(map[int]string)
	}
	m.loadingHistoryWindow[chatIdx] = true
	m.pendingWindowAnchor[chatIdx] = anchorID
}
