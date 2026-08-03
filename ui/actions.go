package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
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

func (m *Model) switchAccount(index int) tea.Cmd {
	if index < 0 || index >= len(m.accounts) || index == m.currentAccount {
		return nil
	}
	m.currentAccount = index
	m.cancelPending()
	m.lastClickedMsgIdx = -1
	m.lastClickTime = time.Time{}
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

func (m *Model) actionMakeDefaultAccount(index int) tea.Cmd {
	if index < 0 || index >= len(m.accounts) || m.defaultAccountSetter == nil {
		return nil
	}
	if err := m.defaultAccountSetter.SetDefaultAccount(m.accounts[index].Name); err != nil {
		return m.showNotification(fmt.Sprintf("setting default account: %v", err))
	}
	return m.showNotification("Default account set")
}

// encryptionModeOrDefault returns mode, or "omemo" (kage's default) if unset.
func encryptionModeOrDefault(mode string) string {
	if mode == "" {
		return "omemo"
	}
	return mode
}

// actionCycleChatEncryption cycles the selected chat's outgoing message
// encryption: omemo -> gpg -> none -> omemo (chat-item context menu's
// "Encryption").
func (m *Model) actionCycleChatEncryption() tea.Cmd {
	idx := m.currentChatIndex()
	items := m.chats.Items()
	if idx < 0 || idx >= len(items) || m.chatEncryptionSetter == nil {
		return m.showNotification("no chat selected")
	}
	chat, ok := items[idx].(Chat)
	if !ok {
		return nil
	}

	next := map[string]string{"omemo": "gpg", "gpg": "none", "none": "omemo"}[encryptionModeOrDefault(chat.EncryptionMode)]
	if err := m.chatEncryptionSetter.SetChatEncryption(m.currentAccount, chat.Address, next); err != nil {
		return m.showNotification("setting encryption: " + err.Error())
	}
	chat.EncryptionMode = next
	m.accounts[m.currentAccount].Chats[idx] = chat
	cmd := m.chats.SetItem(idx, chat)
	return tea.Batch(cmd, m.showNotification("Encryption: "+next))
}

func (m *Model) showNotification(text string) tea.Cmd {
	m.noticeID++
	m.noticeText = text
	id := m.noticeID
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		return noticeClearMsg{id: id}
	})
}

// sendCurrentInput performs the SelectSend action for viewChat: sends the
// composed message (wired as an edit/reply/reaction as appropriate given
// the current compose state), clears the input, and refreshes the
// viewport. Shared by the SelectSend keybinding and a click on the mouse
// send button — both must produce identical behavior.
func (m *Model) sendCurrentInput() tea.Cmd {
	var cmds []tea.Cmd

	if m.reactingMsgIdx >= 0 {
		// Unlike a normal send, an empty input is meaningful here (it's how
		// you clear your reaction set), so this bypasses the "empty means
		// do nothing" rule below entirely.
		newMine := toEmojiSet(m.input.Value())
		cmds = append(cmds, m.sendReaction(m.reactingMsgIdx, newMine))
		m.notifyTypingStopped()
		m.reactingMsgIdx = -1
		m.setEmojiSuggestions(nil)
		m.input.SetValue("")
		m.input.Placeholder = "message..."
		m.updateSizes()
		m.refreshViewport()
		return tea.Batch(cmds...)
	}

	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return nil
	}
	chatIdx := m.currentChatIndex()
	if chatIdx < 0 {
		return nil
	}

	if m.editingMsgIdx >= 0 {
		// Apply edit in-place, and wire it as a XEP-0308 correction so the
		// other party actually sees the update — a message can only be
		// corrected on the network if it was sent with an ID in the first
		// place (e.g. locally-seeded/demo data never was), so degrade to a
		// local-only edit otherwise.
		msgs := m.currentMessages()
		if m.editingMsgIdx < len(msgs) {
			msgs[m.editingMsgIdx].Content = text
			m.setCurrentMessages(msgs)

			if chat, ok := m.currentChat(); ok && chat.Address != "" && m.sender != nil && msgs[m.editingMsgIdx].ID != "" {
				_, err := m.sender.Send(m.currentAccount, chat.Address, text, SendOptions{
					ReplaceID: msgs[m.editingMsgIdx].ID,
				})
				if err != nil {
					cmds = append(cmds, m.showNotification("edit not delivered: "+err.Error()))
				}
			}
		}
		m.editingMsgIdx = -1
		m.input.Placeholder = "message..."
	} else {
		// Send new message, optionally quoting a reply.
		newMsg := Message{
			Author:  "me",
			Content: text,
			SentAt:  time.Now(),
			IsMe:    true,
		}

		var sendOpts SendOptions
		if m.replyToIdx >= 0 {
			rt := m.replyToIdx
			newMsg.ReplyTo = &rt
			if msgs := m.currentMessages(); rt < len(msgs) && msgs[rt].ID != "" {
				sendOpts = SendOptions{
					ReplyToID:    msgs[rt].ID,
					QuotedAuthor: msgs[rt].Author,
					QuotedBody:   msgs[rt].Content,
				}
			}
			m.replyToIdx = -1
		}

		if chat, ok := m.currentChat(); ok && chat.Address != "" && m.sender != nil {
			id, err := m.sender.Send(m.currentAccount, chat.Address, text, sendOpts)
			if err != nil {
				cmds = append(cmds, m.showNotification("send failed: "+err.Error()))
			} else {
				newMsg.ID = id
			}
		}

		msgs := append(m.currentMessages(), newMsg)
		m.setCurrentMessages(msgs)
	}

	m.notifyTypingStopped()
	m.input.SetValue("")
	m.updateSizes()
	m.refreshViewport()
	m.viewport.GotoBottom()
	return tea.Batch(cmds...)
}

// The action* methods below each implement one message/chat action against
// the current selection (m.selectedMsg / m.currentChatIndex()). They exist
// so the DeleteMsg/YankMsg/EditMsg/etc. keybindings and the mouse
// context-menu (see ui/contextmenu.go) share one implementation instead of
// drifting apart.

// actionDeleteMessage opens the delete-confirmation popup for the selected
// message (viewChat's DeleteMsg / a message context-menu's "Delete").
func (m *Model) actionDeleteMessage() tea.Cmd {
	if m.currentChatIndex() < 0 {
		return nil
	}
	if len(m.currentMessages()) == 0 {
		return m.showNotification("no messages to delete")
	}
	m.confirmTarget = confirmDeleteMessage
	return nil
}

// actionLeaveChat opens the leave-chat confirmation popup for the selected
// chat (viewChats' DeleteMsg / a chat-item context-menu's "Leave chat").
func (m *Model) actionLeaveChat() tea.Cmd {
	if m.currentChatIndex() < 0 {
		return m.showNotification("no chat selected")
	}
	m.confirmTarget = confirmDeleteChat
	return nil
}

// actionRenameChat opens the rename-contact prompt for the selected chat
// (viewChats' RenameChat keybind / a chat-item context-menu's "Rename").
// Prefilled with the chat's current custom name if it has one; the field is
// left empty (showing the JID as a placeholder) otherwise, so submitting it
// unchanged clears any custom name rather than pinning it to the JID text.
func (m *Model) actionRenameChat() tea.Cmd {
	chat, ok := m.currentChat()
	if !ok {
		return m.showNotification("no chat selected")
	}

	ti := textinput.New()
	ti.Prompt = "Name: "
	ti.Placeholder = chat.Address
	ti.KeyMap = m.keys.TextInputKeys
	ti.SetWidth(addAccountFieldWidth)
	applyTextInputStyles(&ti, m.styles.colors)
	if chat.Name != chat.Address {
		ti.SetValue(chat.Name)
	}
	ti.CursorEnd()
	ti.Focus()

	m.renameChatIdx = m.currentChatIndex()
	m.renameInput = ti
	m.renamingChat = true
	return textinput.Blink
}

// submitRenameChat applies the rename-prompt's current value to the chat it
// was opened for: pushes it to the server as a roster set and mirrors it
// locally (see ContactRenamer), then updates the in-memory chat name shown
// in the sidebar. An empty value clears the custom name — the chat falls
// back to displaying its JID, matching what a fresh roster fetch would show.
func (m *Model) submitRenameChat() tea.Cmd {
	m.renamingChat = false

	idx := m.renameChatIdx
	items := m.chats.Items()
	if idx < 0 || idx >= len(items) {
		return nil
	}
	chat, ok := items[idx].(Chat)
	if !ok {
		return nil
	}

	name := strings.TrimSpace(m.renameInput.Value())
	if name == "" {
		name = chat.Address
	}
	if name == chat.Name {
		return nil
	}

	if m.renamer != nil && chat.Address != "" {
		if err := m.renamer.RenameContact(m.currentAccount, chat.Address, strings.TrimSpace(m.renameInput.Value())); err != nil {
			return m.showNotification("rename failed: " + err.Error())
		}
	}

	oldName := chat.Name
	chat.Name = name
	m.accounts[m.currentAccount].Chats[idx] = chat

	// Message.Author is a resolved display name, not a live lookup (see
	// IncomingMessageMsg/loadHistory) — it needs to be patched everywhere
	// it was stamped with the old name, or already-shown messages would
	// keep displaying it forever.
	msgs := m.accounts[m.currentAccount].Messages[idx]
	for i := range msgs {
		if !msgs[i].IsMe && msgs[i].Author == oldName {
			msgs[i].Author = name
		}
	}

	cmd := m.chats.SetItem(idx, chat)
	if idx == m.currentChatIndex() {
		m.refreshViewport()
	}
	return cmd
}

func (m *Model) actionYankMessage() tea.Cmd {
	if err := m.yankSelectedMsg(); err != nil {
		return m.showNotification("copy failed")
	}
	return m.showNotification("message copied")
}

func (m *Model) actionEditMessage() tea.Cmd {
	if m.currentChatIndex() < 0 {
		return nil
	}
	msgs := m.currentMessages()
	if !m.canEdit(msgs) {
		return m.showNotification("can only edit your last message")
	}
	m.editingMsgIdx = m.selectedMsg
	m.input.SetValue(msgs[m.selectedMsg].Content)
	m.input.Placeholder = "edit message..."
	return m.input.Focus()
}

func (m *Model) actionReplyMessage() tea.Cmd {
	if m.currentChatIndex() < 0 {
		return nil
	}
	if len(m.currentMessages()) == 0 {
		return m.showNotification("no message to reply to")
	}
	if m.replyToIdx == m.selectedMsg {
		m.replyToIdx = -1 // pressed/clicked again on the same message: clear reply
	} else {
		m.replyToIdx = m.selectedMsg
	}
	m.updateSizes()
	m.refreshViewport()
	return m.input.Focus()
}

func (m *Model) actionInfoMessage() tea.Cmd {
	msgs := m.currentMessages()
	if m.selectedMsg < 0 || m.selectedMsg >= len(msgs) {
		return m.showNotification("no message selected")
	}
	m.showMsgInfo = true
	return nil
}

func (m *Model) actionOpenMessage() tea.Cmd {
	msgs := m.currentMessages()
	if m.selectedMsg < 0 || m.selectedMsg >= len(msgs) {
		return m.showNotification("no message selected")
	}
	items := openableItems(msgs[m.selectedMsg])
	switch len(items) {
	case 0:
		return m.showNotification("nothing to open")
	case 1:
		return openWithXDGOpen(items[0])
	default:
		m.openItems = items
		m.openPage = 0
		m.openMode = pickerModeOpen
		return nil
	}
}

func (m *Model) actionSaveMessage() tea.Cmd {
	msgs := m.currentMessages()
	if m.selectedMsg < 0 || m.selectedMsg >= len(msgs) {
		return m.showNotification("no message selected")
	}
	items := openableItems(msgs[m.selectedMsg])
	switch len(items) {
	case 0:
		return m.showNotification("nothing to save")
	case 1:
		return saveURLToDownloads(items[0])
	default:
		m.openItems = items
		m.openPage = 0
		m.openMode = pickerModeSave
		return nil
	}
}

func (m *Model) actionReactMessage() tea.Cmd {
	msgs := m.currentMessages()
	if m.selectedMsg < 0 || m.selectedMsg >= len(msgs) {
		return m.showNotification("no message selected")
	}
	m.reactingMsgIdx = m.selectedMsg
	m.input.SetValue(myReactionsText(msgs[m.selectedMsg].Reactions))
	m.input.CursorEnd()
	m.input.Placeholder = "react: :shortcode: or emoji, enter to send..."
	m.setEmojiSuggestions(nil)
	m.updateSizes()
	return m.input.Focus()
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

// acceptEmojiSuggestion accepts emojiSuggestions[idx] into the input —
// shared by the tab keybinding (idx == emojiSuggestIdx) and a click on a
// suggestion in the react hint (see handleLeftClick).
func (m *Model) acceptEmojiSuggestion(idx int) {
	if idx < 0 || idx >= len(m.emojiSuggestions) {
		return
	}
	// Insert the resolved glyph, not the raw shortcode text — otherwise a
	// freshly-picked reaction shows as literal ":thing:" in the input while
	// the prefilled existing reactions (myReactionsText) already show as
	// real emoji, which looks inconsistent.
	chosen := m.emojiSuggestions[idx].Emoji
	m.input.SetValue(acceptEmojiSuggestion(m.input.Value(), chosen))
	m.input.CursorEnd()
	var next []emojiSuggestion
	if token, _, ok := currentWordToken(m.input.Value()); ok {
		next = emojiSuggestionsFor(token)
	}
	m.setEmojiSuggestions(next)
}

// cancelPending clears any in-progress edit, reply, or reaction composition.
func (m *Model) cancelPending() {
	m.editingMsgIdx = -1
	m.replyToIdx = -1
	m.reactingMsgIdx = -1
	m.lastClickedMsgIdx = -1
	m.setEmojiSuggestions(nil)
	m.input.SetValue("")
	m.input.Placeholder = "message..."
	m.updateSizes()
}

// refreshViewport re-renders all messages and updates the viewport content.
func (m *Model) refreshViewport() {
	if m.currentChatIndex() < 0 {
		m.msgOffsets = nil
		m.viewportLines = nil
		if m.currentAccountConnecting() {
			m.viewport.SetContent("connecting...")
		} else {
			m.viewport.SetContent("")
		}
		return
	}
	if len(m.currentMessages()) == 0 && m.currentAccountConnecting() {
		m.msgOffsets = nil
		m.viewportLines = nil
		m.viewport.SetContent("connecting...")
		return
	}
	content, offsets := m.renderMessagesWithOffsets()
	m.msgOffsets = offsets
	m.viewportLines = strings.Split(content, "\n")
	m.viewport.SetContentLines(m.viewportLines)
}

// refreshViewportSelection re-renders only the messages at oldIdx and newIdx
// (the previous and new selected/hovered message) and patches their lines
// back into the cached viewport content, instead of re-wrapping every
// message. Selection/hover only changes a message's prefix styling, never
// its wrapping (see renderMessagePrefix — both states are 2 cells wide), so
// each message's line range and count stay fixed and can be spliced in
// place. This matters because handleMouseMotion calls this on every mouse
// motion event; a full refreshViewport() there made the highlighted message
// visibly lag behind a fast-moving mouse in chats with many messages.
func (m *Model) refreshViewportSelection(oldIdx, newIdx int) {
	if m.viewportLines == nil || len(m.msgOffsets) == 0 {
		m.refreshViewport()
		return
	}

	cw := m.chatAreaWidth()
	if cw <= 10 {
		m.refreshViewport()
		return
	}
	msgs := m.currentMessages()

	for _, idx := range []int{oldIdx, newIdx} {
		if idx < 0 || idx >= len(msgs) || idx >= len(m.msgOffsets) {
			continue
		}
		start := m.msgOffsets[idx]
		end := len(m.viewportLines)
		if idx+1 < len(m.msgOffsets) {
			end = m.msgOffsets[idx+1]
		}
		if start < 0 || end > len(m.viewportLines) || start > end {
			m.refreshViewport()
			return
		}
		rendered := m.zone.Mark(zoneMessage(idx), padLinesToWidth(m.renderMessage(msgs[idx], idx, cw, msgs), cw))
		newLines := strings.Split(rendered, "\n")
		if len(newLines) != end-start {
			// Wrapping changed unexpectedly (e.g. width changed mid-flight);
			// fall back to a full re-render rather than corrupt offsets.
			m.refreshViewport()
			return
		}
		copy(m.viewportLines[start:end], newLines)
	}

	// SetContentLines takes the already-split lines directly, skipping the
	// join-then-resplit that SetContent(strings.Join(...)) would do.
	m.viewport.SetContentLines(m.viewportLines)
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
