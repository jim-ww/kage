package ui

import (
	"sort"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

// Hiding a chat takes it out of the chat list without touching anything
// else: the contact stays in the roster, presence and messages keep
// arriving, storage is untouched, and the other side sees nothing. It
// exists because the chat list *is* the roster (the daemon builds it from
// ListRoster), so every contact ever added is a row forever, and pruning
// that shouldn't require unsubscribing from someone.
//
// Real removal lives in the contact manager, which unsubscribes and says
// so. That's the destructive path and it should stay somewhere you went
// deliberately.
//
// Hidden chats are kept out of Account.Chats entirely rather than filtered
// at render time: the chat list's indices and Account.Messages' keys are
// the same index space (see currentChatIndex), so a list that skipped rows
// would misalign every message lookup. They're stashed here instead, whole,
// so unhiding restores the chat and its loaded history exactly.
type hiddenChat struct {
	chat        Chat
	messages    []Message
	historyMore bool
}

// stripHiddenChats moves every chat flagged Hidden out of the account and
// into the stash, re-keying the remaining messages and history-more flags
// to their new indices. Safe to call repeatedly — an account with nothing
// hidden is left untouched.
func (m *Model) stripHiddenChats(accountIdx int) {
	if accountIdx < 0 || accountIdx >= len(m.accounts) {
		return
	}
	acct := m.accounts[accountIdx]
	hidden := 0
	for _, item := range acct.Chats {
		if chat, ok := item.(Chat); ok && chat.Hidden {
			hidden++
		}
	}
	if hidden == 0 {
		return
	}

	kept := make([]list.Item, 0, len(acct.Chats)-hidden)
	messages := make(map[int][]Message, len(acct.Chats)-hidden)
	historyMore := make(map[int]bool, len(acct.Chats)-hidden)
	for i, item := range acct.Chats {
		chat, ok := item.(Chat)
		if ok && chat.Hidden {
			m.stashHidden(accountIdx, hiddenChat{
				chat:        chat,
				messages:    acct.Messages[i],
				historyMore: acct.HistoryMore[i],
			})
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
	}

	m.accounts[accountIdx].Chats = kept
	m.accounts[accountIdx].Messages = messages
	m.accounts[accountIdx].HistoryMore = historyMore
}

func (m *Model) stashHidden(accountIdx int, h hiddenChat) {
	if m.hiddenChats == nil {
		m.hiddenChats = map[int]map[string]hiddenChat{}
	}
	if m.hiddenChats[accountIdx] == nil {
		m.hiddenChats[accountIdx] = map[string]hiddenChat{}
	}
	m.hiddenChats[accountIdx][strings.ToLower(h.chat.Address)] = h
}

// hiddenChatsFor lists an account's hidden chats, sorted by address so the
// contact manager renders them in a stable order.
func (m Model) hiddenChatsFor(accountIdx int) []Chat {
	stash := m.hiddenChats[accountIdx]
	if len(stash) == 0 {
		return nil
	}
	out := make([]Chat, 0, len(stash))
	for _, h := range stash {
		out = append(out, h.chat)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out
}

// isChatHidden reports whether an account's chat with this address is
// currently hidden.
func (m Model) isChatHidden(accountIdx int, address string) bool {
	_, ok := m.hiddenChats[accountIdx][strings.ToLower(address)]
	return ok
}

// hideChat takes the chat at idx out of the list and remembers it. The
// persist call is what makes it survive a restart; failing it leaves the
// chat hidden for this session rather than refusing the action, since the
// user has already seen it go.
func (m *Model) hideChat(accountIdx, chatIdx int) tea.Cmd {
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
	chat.Hidden = true
	m.accounts[accountIdx].Chats[chatIdx] = chat
	m.stripHiddenChats(accountIdx)

	cmds := []tea.Cmd{m.reselectAfterHide(accountIdx, chatIdx)}
	if err := m.persistChatHidden(accountIdx, chat.Address, true); err != nil {
		cmds = append(cmds, m.showNotification("hiding "+chat.Address+": "+err.Error()))
	} else {
		cmds = append(cmds, m.showNotification("hid "+chat.Address+" — unhide from the contact manager"))
	}
	return tea.Batch(cmds...)
}

// unhideChat puts a hidden chat back at the end of the list, with whatever
// history was loaded when it was hidden.
func (m *Model) unhideChat(accountIdx int, address string) tea.Cmd {
	stash := m.hiddenChats[accountIdx]
	key := strings.ToLower(address)
	h, ok := stash[key]
	if !ok {
		return nil
	}
	delete(stash, key)

	h.chat.Hidden = false
	idx := len(m.accounts[accountIdx].Chats)
	m.accounts[accountIdx].Chats = append(m.accounts[accountIdx].Chats, h.chat)
	if len(h.messages) > 0 {
		if m.accounts[accountIdx].Messages == nil {
			m.accounts[accountIdx].Messages = map[int][]Message{}
		}
		m.accounts[accountIdx].Messages[idx] = h.messages
	}
	if h.historyMore {
		if m.accounts[accountIdx].HistoryMore == nil {
			m.accounts[accountIdx].HistoryMore = map[int]bool{}
		}
		m.accounts[accountIdx].HistoryMore[idx] = true
	}

	cmds := []tea.Cmd{m.sortChatsByActivity(accountIdx)}
	if err := m.persistChatHidden(accountIdx, h.chat.Address, false); err != nil {
		cmds = append(cmds, m.showNotification("unhiding "+h.chat.Address+": "+err.Error()))
	}
	return tea.Batch(cmds...)
}

func (m Model) persistChatHidden(accountIdx int, address string, hidden bool) error {
	if m.chatHiddenSetter == nil {
		return nil
	}
	return m.chatHiddenSetter.SetChatHidden(m.accounts[accountIdx].Name, address, hidden)
}

// reselectAfterHide keeps the chat list usable once a row disappears from
// under the cursor, mirroring what removing a chat used to do.
func (m *Model) reselectAfterHide(accountIdx, chatIdx int) tea.Cmd {
	if accountIdx != m.currentAccount {
		return nil
	}
	items := m.accounts[accountIdx].Chats
	cmd := m.chats.SetItems(items)
	if len(items) == 0 {
		m.setSelectedView(viewChats)
		m.selectedMsg = 0
		m.cancelPending()
		m.refreshViewport()
		return cmd
	}
	if chatIdx >= len(items) {
		chatIdx = len(items) - 1
	}
	m.chats.Select(chatIdx)
	if msgs := m.currentMessages(); len(msgs) > 0 {
		m.selectedMsg = len(msgs) - 1
	} else {
		m.selectedMsg = 0
	}
	m.cancelPending()
	m.refreshViewport()
	m.viewport.GotoBottom()
	return cmd
}
