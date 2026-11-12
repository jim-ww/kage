package ui

import (
	"maps"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Several TUIs can be attached to one daemon at once (see the daemon's
// adapter), so anything one of them changes that the others show comes back
// to all of them as a broadcast - including the one that made the change,
// for which applying it again must be a no-op.

// applyAccountStatus applies an account's new own status, and appends the
// chats coming back online loaded, if any.
func (m *Model) applyAccountStatus(msg AccountStatusChangedMsg) tea.Cmd {
	m.accounts[msg.Index].Status = msg.Status
	if len(msg.NewChats) == 0 {
		return nil
	}
	acct := &m.accounts[msg.Index]
	acct.Chats = append(acct.Chats, msg.NewChats...)
	if acct.Messages == nil {
		acct.Messages = make(map[int][]Message)
	}
	maps.Copy(acct.Messages, msg.NewMessages)
	if acct.HistoryMore == nil {
		acct.HistoryMore = make(map[int]bool)
	}
	maps.Copy(acct.HistoryMore, msg.NewHistoryMore)
	m.stripHiddenChats(msg.Index)
	if msg.Index == m.currentAccount {
		return m.chats.SetItems(acct.Chats)
	}
	return nil
}

// applyChatSetting brings one chat's setting in line with msg, unless it
// already is.
func (m *Model) applyChatSetting(msg ChatSettingMsg) tea.Cmd {
	accountIdx := -1
	for i, a := range m.accounts {
		if a.Name == msg.AccountJID {
			accountIdx = i
			break
		}
	}
	if accountIdx < 0 {
		return nil
	}
	if msg.Setting == ChatHidden {
		if msg.On {
			if idx := m.chatIndexByAddress(accountIdx, msg.ChatAddress); idx >= 0 {
				_, cmd, _ := m.applyHideChat(accountIdx, idx)
				return cmd
			}
			return nil
		}
		cmd, _ := m.applyUnhideChat(accountIdx, msg.ChatAddress)
		return cmd
	}

	idx := m.chatIndexByAddress(accountIdx, msg.ChatAddress)
	if idx < 0 {
		// Hidden here: update the stashed copy, so unhiding shows it right.
		if h, ok := m.hiddenChats[accountIdx][strings.ToLower(msg.ChatAddress)]; ok {
			h.chat = withChatSetting(h.chat, msg)
			m.hiddenChats[accountIdx][strings.ToLower(msg.ChatAddress)] = h
		}
		return nil
	}
	chat, ok := m.accounts[accountIdx].Chats[idx].(Chat)
	if !ok {
		return nil
	}
	updated := withChatSetting(chat, msg)
	if updated.EncryptionMode == chat.EncryptionMode && updated.Pinned == chat.Pinned && updated.Muted == chat.Muted {
		return nil
	}
	m.accounts[accountIdx].Chats[idx] = updated
	if msg.Setting == ChatPinned {
		return m.sortChatsByActivity(accountIdx)
	}
	if accountIdx == m.currentAccount {
		return m.chats.SetItem(idx, updated)
	}
	return nil
}

// withChatSetting returns chat with msg's setting applied.
func withChatSetting(chat Chat, msg ChatSettingMsg) Chat {
	switch msg.Setting {
	case ChatEncryption:
		chat.EncryptionMode = msg.Mode
	case ChatPinned:
		chat.Pinned = msg.On
	case ChatMuted:
		chat.Muted = msg.On
	}
	return chat
}
