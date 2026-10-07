package ui

import (
	"charm.land/bubbles/v2/list"
)

// remapChatIndices re-keys every piece of per-chat state that is keyed by
// the chat's position in the list, given newIdxByOld (an old index absent
// from it means that chat is gone). Any chat-list reorder or removal has to
// call this, or each of those maps silently hands one chat's state to its
// neighbour.
//
// The maps are:
//
//   - Messages, HistoryMore, HistoryNewer on the account - the loaded
//     history window and which direction it can still be paged in. Dropping
//     HistoryNewer in particular lets IncomingMessageMsg splice a live
//     message onto the end of a mid-history window, which is exactly the
//     thing that flag exists to prevent.
//   - loadingHistoryWindow and pendingWindowAnchor on the Model - in-flight
//     history fetches. A HistoryWindowMsg resolves its own chat by address,
//     so a drifted entry doesn't misfile the response, but it does leave
//     another chat marked as permanently fetching (its paging then never
//     fires again) and reads back the wrong scroll anchor. These two carry
//     no account dimension, so they are only remapped for the account
//     actually on screen - the one they can be about.
func (m *Model) remapChatIndices(accountIdx int, newIdxByOld map[int]int) {
	if accountIdx < 0 || accountIdx >= len(m.accounts) {
		return
	}
	acct := m.accounts[accountIdx]

	messages := make(map[int][]Message, len(acct.Messages))
	historyMore := make(map[int]bool, len(acct.HistoryMore))
	historyNewer := make(map[int]bool, len(acct.HistoryNewer))
	for old, next := range newIdxByOld {
		if msgs, has := acct.Messages[old]; has {
			messages[next] = msgs
		}
		if more, has := acct.HistoryMore[old]; has {
			historyMore[next] = more
		}
		if newer, has := acct.HistoryNewer[old]; has {
			historyNewer[next] = newer
		}
	}
	m.accounts[accountIdx].Messages = messages
	m.accounts[accountIdx].HistoryMore = historyMore
	m.accounts[accountIdx].HistoryNewer = historyNewer

	if accountIdx != m.currentAccount {
		return
	}
	loading := make(map[int]bool, len(m.loadingHistoryWindow))
	anchors := make(map[int]string, len(m.pendingWindowAnchor))
	for old, next := range newIdxByOld {
		if l, has := m.loadingHistoryWindow[old]; has {
			loading[next] = l
		}
		if a, has := m.pendingWindowAnchor[old]; has {
			anchors[next] = a
		}
	}
	m.loadingHistoryWindow = loading
	m.pendingWindowAnchor = anchors
}

// removeChatAt drops the chat at chatIdx from an account's list, re-keying
// the per-chat state that indexes into it (see remapChatIndices). Returns
// false if chatIdx names no chat, in which case nothing was touched.
func (m *Model) removeChatAt(accountIdx, chatIdx int) bool {
	if accountIdx < 0 || accountIdx >= len(m.accounts) {
		return false
	}
	items := m.accounts[accountIdx].Chats
	if chatIdx < 0 || chatIdx >= len(items) {
		return false
	}

	kept := make([]list.Item, 0, len(items)-1)
	newIdxByOld := make(map[int]int, len(items)-1)
	for i, item := range items {
		if i == chatIdx {
			continue
		}
		newIdxByOld[i] = len(kept)
		kept = append(kept, item)
	}
	m.accounts[accountIdx].Chats = kept
	m.remapChatIndices(accountIdx, newIdxByOld)
	return true
}
