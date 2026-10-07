package ui

import (
	"charm.land/bubbles/v2/list"
)

// removeChatAt drops the chat at chatIdx from an account's list, re-keying
// the per-chat maps that index into it. Those maps (Messages, HistoryMore,
// HistoryNewer) are keyed by position, not address, so a plain slice splice
// silently shifts every chat after the removed one onto its neighbour's
// history - see stripHiddenChats, which has to do the same re-keying for
// the same reason.
//
// Returns false if chatIdx names no chat, in which case nothing was
// touched.
func (m *Model) removeChatAt(accountIdx, chatIdx int) bool {
	if accountIdx < 0 || accountIdx >= len(m.accounts) {
		return false
	}
	acct := m.accounts[accountIdx]
	if chatIdx < 0 || chatIdx >= len(acct.Chats) {
		return false
	}

	kept := make([]list.Item, 0, len(acct.Chats)-1)
	messages := make(map[int][]Message, len(acct.Chats)-1)
	historyMore := make(map[int]bool, len(acct.Chats)-1)
	historyNewer := make(map[int]bool, len(acct.Chats)-1)
	for i, item := range acct.Chats {
		if i == chatIdx {
			continue
		}
		next := len(kept)
		kept = append(kept, item)
		if msgs, has := acct.Messages[i]; has {
			messages[next] = msgs
		}
		if more, has := acct.HistoryMore[i]; has {
			historyMore[next] = more
		}
		if newer, has := acct.HistoryNewer[i]; has {
			historyNewer[next] = newer
		}
	}

	m.accounts[accountIdx].Chats = kept
	m.accounts[accountIdx].Messages = messages
	m.accounts[accountIdx].HistoryMore = historyMore
	m.accounts[accountIdx].HistoryNewer = historyNewer
	return true
}
