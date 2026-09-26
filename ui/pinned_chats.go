package ui

import (
	tea "charm.land/bubbletea/v2"
)

// Pinning a chat holds it at the top of the chat list: pinned chats sort as
// a block above every unpinned one, activity-sorted among themselves (see
// sortChatsByActivity). It's purely local ordering — nothing is sent, the
// peer sees nothing, and storage is untouched — persisted per account in
// state.toml so the list comes back in the same order next launch.
//
// Unlike hiding, a pinned chat stays in Account.Chats, so there's no stash
// and no index re-keying beyond the sort itself.

// togglePinChat flips the pinned flag on accountIdx's chat at chatIdx and
// re-sorts the list. The persist call failing leaves the chat pinned for
// this session rather than refusing the action, since the user has already
// seen the row move — same trade as hideChat.
func (m *Model) togglePinChat(accountIdx, chatIdx int) tea.Cmd {
	if accountIdx < 0 || accountIdx >= len(m.accounts) {
		return nil
	}
	items := m.accounts[accountIdx].Chats
	if chatIdx < 0 || chatIdx >= len(items) {
		return nil
	}
	chat, ok := items[chatIdx].(Chat)
	if !ok {
		return nil
	}
	chat.Pinned = !chat.Pinned
	m.accounts[accountIdx].Chats[chatIdx] = chat

	cmds := []tea.Cmd{m.sortChatsByActivity(accountIdx)}
	if err := m.persistChatPinned(accountIdx, chat.Address, chat.Pinned); err != nil {
		cmds = append(cmds, m.showNotification("pinning "+chat.Address+": "+err.Error()))
	} else if chat.Pinned {
		cmds = append(cmds, m.showNotification("pinned "+chat.Address))
	} else {
		cmds = append(cmds, m.showNotification("unpinned "+chat.Address))
	}
	return tea.Batch(cmds...)
}

func (m Model) persistChatPinned(accountIdx int, address string, pinned bool) error {
	if m.chatPinnedSetter == nil {
		return nil
	}
	return m.chatPinnedSetter.SetChatPinned(m.accounts[accountIdx].Name, address, pinned)
}

// actionTogglePinChat pins or unpins the selected chat (viewChats' PinChat
// keybind / a chat-item context menu's "Pin chat").
func (m *Model) actionTogglePinChat() tea.Cmd {
	chatIdx := m.currentChatIndex()
	if chatIdx < 0 {
		return m.showNotification("no chat selected")
	}
	return m.togglePinChat(m.currentAccount, chatIdx)
}
