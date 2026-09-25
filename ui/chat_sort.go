package ui

import (
	"sort"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

// sortChatsByActivity reorders accountIdx's chat list by most recent
// activity first (Chat.LastActivity descending, ties keeping their existing
// relative order), re-keying Messages/HistoryMore to their new indices —
// same reason stripHiddenChats does: the chat list's indices and
// Account.Messages' keys share one index space (see currentChatIndex), so
// reordering the list without the maps would misalign every message lookup.
//
// If accountIdx is the currently displayed account, this also refreshes the
// visible list and keeps whatever chat was selected under the cursor.
// Always call it after anything that changes a chat's LastActivity or
// otherwise appends/reorders Chats, even for a backgrounded account — so its
// order is already right by the time the user switches to it.
func (m *Model) sortChatsByActivity(accountIdx int) tea.Cmd {
	if accountIdx < 0 || accountIdx >= len(m.accounts) {
		return nil
	}
	acct := m.accounts[accountIdx]

	type entry struct {
		oldIdx int
		chat   Chat
	}
	entries := make([]entry, 0, len(acct.Chats))
	for i, item := range acct.Chats {
		chat, ok := item.(Chat)
		if !ok {
			return nil
		}
		entries = append(entries, entry{i, chat})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].chat.LastActivity.After(entries[j].chat.LastActivity)
	})

	oldSelected := -1
	if accountIdx == m.currentAccount {
		oldSelected = m.chats.GlobalIndex()
	}

	chats := make([]list.Item, len(entries))
	messages := make(map[int][]Message, len(acct.Messages))
	historyMore := make(map[int]bool, len(acct.HistoryMore))
	newSelected := -1
	for newIdx, e := range entries {
		chats[newIdx] = e.chat
		if msgs, has := acct.Messages[e.oldIdx]; has {
			messages[newIdx] = msgs
		}
		if more, has := acct.HistoryMore[e.oldIdx]; has {
			historyMore[newIdx] = more
		}
		if e.oldIdx == oldSelected {
			newSelected = newIdx
		}
	}
	m.accounts[accountIdx].Chats = chats
	m.accounts[accountIdx].Messages = messages
	m.accounts[accountIdx].HistoryMore = historyMore

	if accountIdx != m.currentAccount {
		return nil
	}
	cmd := m.chats.SetItems(chats)
	if newSelected >= 0 {
		m.chats.Select(newSelected)
	}
	return cmd
}
