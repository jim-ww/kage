package ui

import (
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"
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

func (m *Model) switchAccount(index int) tea.Cmd {
	if index < 0 || index >= len(m.accounts) || index == m.currentAccount {
		return nil
	}
	m.currentAccount = index
	m.cancelPending()
	m.chats.Select(0)
	m.selectedMsg = 0
	cmd := m.chats.SetItems(m.accounts[index].Chats)
	if msgs := m.currentMessages(); len(msgs) > 0 {
		m.selectedMsg = len(msgs) - 1
	}
	m.refreshViewport()
	m.viewport.GotoBottom()
	return cmd
}

func (m *Model) showNotification(text string) tea.Cmd {
	m.noticeID++
	m.noticeText = text
	id := m.noticeID
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		return noticeClearMsg{id: id}
	})
}

func (m *Model) openCurrentChat() (tea.Model, tea.Cmd) {
	if m.currentChatIndex() < 0 {
		return m, nil
	}
	m.selectedView = viewChat
	if msgs := m.currentMessages(); len(msgs) > 0 {
		m.selectedMsg = len(msgs) - 1
	}
	m.refreshViewport()
	return m, m.input.Focus()
}

// canEdit returns true only when selectedMsg is the last "IsMe" message.
func (m Model) canEdit(msgs []Message) bool {
	if m.selectedMsg < 0 || m.selectedMsg >= len(msgs) {
		return false
	}
	if !msgs[m.selectedMsg].IsMe {
		return false
	}
	for i := m.selectedMsg + 1; i < len(msgs); i++ {
		if msgs[i].IsMe {
			return false
		}
	}
	return true
}

// deleteSelectedMsg removes the current message and fixes up ReplyTo indices.
func (m *Model) deleteSelectedMsg() {
	if m.currentChatIndex() < 0 {
		return
	}
	msgs := m.currentMessages()
	if m.selectedMsg < 0 || m.selectedMsg >= len(msgs) {
		return
	}
	del := m.selectedMsg
	newMsgs := make([]Message, 0, len(msgs)-1)
	for i, msg := range msgs {
		if i == del {
			continue
		}
		if msg.ReplyTo != nil {
			switch {
			case *msg.ReplyTo == del:
				// reply target is gone
				msg.ReplyTo = nil
			case *msg.ReplyTo > del:
				adj := *msg.ReplyTo - 1
				msg.ReplyTo = &adj
			}
		}
		newMsgs = append(newMsgs, msg)
	}
	m.setCurrentMessages(newMsgs)
	if m.selectedMsg >= len(newMsgs) && len(newMsgs) > 0 {
		m.selectedMsg = len(newMsgs) - 1
	}
}

func (m Model) yankSelectedMsg() error {
	if m.currentChatIndex() < 0 {
		return nil
	}
	msgs := m.currentMessages()
	if m.selectedMsg < 0 || m.selectedMsg >= len(msgs) {
		return nil
	}
	return clipboard.WriteAll(msgs[m.selectedMsg].Content)
}

func (m *Model) deleteSelectedChat() tea.Cmd {
	chatIdx := m.currentChatIndex()
	if chatIdx < 0 {
		return nil
	}

	items := m.chats.Items()
	newItems := make([]list.Item, 0, len(items)-1)
	newItems = append(newItems, items[:chatIdx]...)
	newItems = append(newItems, items[chatIdx+1:]...)

	newMessages := make(map[int][]Message, len(newItems))
	oldMessages := m.accounts[m.currentAccount].Messages
	for i := range items {
		switch {
		case i < chatIdx:
			newMessages[i] = oldMessages[i]
		case i > chatIdx:
			newMessages[i-1] = oldMessages[i]
		}
	}
	m.accounts[m.currentAccount].Chats = newItems
	m.accounts[m.currentAccount].Messages = newMessages

	cmd := m.chats.SetItems(newItems)
	if len(newItems) == 0 {
		m.selectedView = viewChats
		m.selectedMsg = 0
		m.cancelPending()
		m.refreshViewport()
		return cmd
	}

	if chatIdx >= len(newItems) {
		chatIdx = len(newItems) - 1
	}
	m.chats.Select(chatIdx)
	msgs := m.currentMessages()
	if len(msgs) > 0 {
		m.selectedMsg = len(msgs) - 1
	} else {
		m.selectedMsg = 0
	}
	m.cancelPending()
	m.refreshViewport()
	m.viewport.GotoBottom()
	return cmd
}

// sendReaction updates our own reaction set on the message at idx (in the
// current chat) to newMine, both locally (optimistic aggregate update) and
// over the network via XEP-0444.
func (m *Model) sendReaction(idx int, newMine []string) tea.Cmd {
	msgs := m.currentMessages()
	if idx < 0 || idx >= len(msgs) {
		return nil
	}
	if msgs[idx].ID == "" {
		return m.showNotification("message has no id; can't react")
	}
	chat, ok := m.currentChat()
	if !ok || chat.Address == "" || m.sender == nil {
		return nil
	}

	msgs[idx].Reactions = setMyReactions(msgs[idx].Reactions, newMine)
	if _, err := m.sender.Send(m.currentAccount, chat.Address, "", SendOptions{
		ReactionTargetID: msgs[idx].ID,
		Reactions:        newMine,
	}); err != nil {
		return m.showNotification("reaction not delivered: " + err.Error())
	}
	return nil
}

// setEmojiSuggestions replaces the live suggestion list and resets which one
// is highlighted — always reset together so the highlight never points past
// the end of a freshly narrowed list.
func (m *Model) setEmojiSuggestions(sugs []emojiSuggestion) {
	m.emojiSuggestions = sugs
	m.emojiSuggestIdx = 0
}

// cancelPending clears any in-progress edit, reply, or reaction composition.
func (m *Model) cancelPending() {
	m.editingMsgIdx = -1
	m.replyToIdx = -1
	m.reactingMsgIdx = -1
	m.setEmojiSuggestions(nil)
	m.input.SetValue("")
	m.input.Placeholder = "message..."
	m.updateSizes()
}

// refreshViewport re-renders all messages and updates the viewport content.
func (m *Model) refreshViewport() {
	if m.currentChatIndex() < 0 {
		m.msgOffsets = nil
		m.viewport.SetContent("")
		return
	}
	content, offsets := m.renderMessagesWithOffsets()
	m.msgOffsets = offsets
	m.viewport.SetContent(content)
}

// refreshViewportScrollTo re-renders and scrolls so msgIdx is visible.
func (m *Model) refreshViewportScrollTo(msgIdx int) {
	m.refreshViewport()
	if msgIdx >= 0 && msgIdx < len(m.msgOffsets) {
		m.viewport.SetYOffset(m.msgOffsets[msgIdx])
	}
}

// msgIndexAtOffset returns the index of the topmost message at or above the
// given viewport line offset, used to keep message selection in sync after
// free-scrolling (paging) through the viewport.
func (m Model) msgIndexAtOffset(yOffset int) int {
	idx := 0
	for i, off := range m.msgOffsets {
		if off > yOffset {
			break
		}
		idx = i
	}
	return idx
}
