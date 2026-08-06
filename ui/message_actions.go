package ui

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"
)

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
	hasAttachments := len(m.pendingAttachments) > 0 && m.editingMsgIdx < 0
	if text == "" && !hasAttachments {
		return nil
	}
	chatIdx := m.currentChatIndex()
	if chatIdx < 0 {
		return nil
	}

	if hasAttachments {
		// Uploading (and the send it feeds into) runs async, same as
		// SendFile — the compose box is cleared optimistically, but unlike
		// a plain text send there's no message to echo into the chat until
		// ComposedSendResultMsg reports the upload actually succeeded.
		chat, ok := m.currentChat()
		if !ok || chat.Address == "" {
			return m.showNotification("no chat selected")
		}
		var sendOpts SendOptions
		if m.replyToIdx >= 0 {
			if msgs := m.currentMessages(); m.replyToIdx < len(msgs) && msgs[m.replyToIdx].ID != "" {
				sendOpts = SendOptions{
					ReplyToID:    msgs[m.replyToIdx].ID,
					QuotedAuthor: msgs[m.replyToIdx].Author,
					QuotedBody:   MessagePreviewContent(msgs[m.replyToIdx]),
				}
			}
			m.replyToIdx = -1
		}
		cmds = append(cmds, m.startAttachedSend(text, chat.Address, sendOpts))
		m.pendingAttachments = nil
		m.selectedAttachment = -1
		m.notifyTypingStopped()
		m.input.SetValue("")
		m.updateSizes()
		return tea.Batch(cmds...)
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
			if m.editingMsgIdx == len(msgs)-1 {
				if chatIdx := m.currentChatIndex(); chatIdx >= 0 {
					cmds = append(cmds, m.setChatLastMessage(m.currentAccount, chatIdx, text))
				}
			}

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
					QuotedBody:   MessagePreviewContent(msgs[rt]),
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
				// Send only succeeds without falling back to plaintext, so a
				// configured encryption mode here means the message really
				// went out encrypted - mirrors what adapter.go's send()
				// computes and persists to storage, which this local optimistic
				// echo has no way to read back before the next reload.
				switch mode := encryptionModeOrDefault(chat.EncryptionMode); {
				case mode == "gpg":
					newMsg.Encrypted, newMsg.EncMethod = true, "gpg"
				case mode == "omemo-v1", mode == "omemo-v2":
					newMsg.Encrypted, newMsg.EncMethod = true, mode
				case mode != "none":
					// Legacy stored mode (e.g. removed "omemo-auto") - actual
					// protocol was resolved server-side; unknown here.
					newMsg.Encrypted, newMsg.EncMethod = true, "omemo"
				}
			}
		}

		msgs := append(m.currentMessages(), newMsg)
		m.setCurrentMessages(msgs)
		if chatIdx := m.currentChatIndex(); chatIdx >= 0 {
			cmds = append(cmds, m.setChatLastMessage(m.currentAccount, chatIdx, newMsg.Content))
		}
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
		// openableItems puts every attachment before any plain link found in
		// Content, so index 0 is an attachment iff there is at least one.
		isAttachment := len(msgs[m.selectedMsg].Attachments) > 0
		return m.startOpen(items[0], isAttachment)
	default:
		m.openItems = items
		m.openItemsAttachCount = len(msgs[m.selectedMsg].Attachments)
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
		return m.startSave(items[0])
	default:
		m.openItems = items
		m.openItemsAttachCount = len(msgs[m.selectedMsg].Attachments)
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

// openPendingChat opens pendingOpenChatAddress (set from config's
// open_last_chat/last_chat_address on startup) once the current account's
// chats have loaded and contain a matching chat, then clears it so it's
// only attempted once.
func (m *Model) openPendingChat() tea.Cmd {
	if m.pendingOpenChatAddress == "" {
		return nil
	}
	addr := m.pendingOpenChatAddress
	m.pendingOpenChatAddress = ""
	for i, item := range m.chats.Items() {
		if chat, ok := item.(Chat); ok && chat.Address == addr {
			m.chats.Select(i)
			model, cmd := m.openCurrentChat()
			*m = model.(Model)
			return cmd
		}
	}
	return nil
}

func (m Model) openCurrentChat() (tea.Model, tea.Cmd) {
	if m.currentChatIndex() < 0 {
		return m, nil
	}
	m.setSelectedView(viewChat)
	if msgs := m.currentMessages(); len(msgs) > 0 {
		m.selectedMsg = len(msgs) - 1
	}
	m.refreshViewport()
	m.viewport.GotoBottom()
	if m.lastChatSetter != nil && m.currentAccount >= 0 && m.currentAccount < len(m.accounts) {
		if chat, ok := m.currentChat(); ok {
			_ = m.lastChatSetter.SetLastChat(m.accounts[m.currentAccount].Name, chat.Address)
		}
	}
	unreadCmd := m.resetChatUnread(m.currentAccount, m.currentChatIndex())
	return m, tea.Batch(unreadCmd, m.input.Focus())
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

// retractSelectedMsg flags the current message as deleted rather than
// removing it — content is kept (visible in the info popup, attachments
// stay openable) but the chat view shows it as retracted. Mirrors the
// MessageRetractedMsg handling for a remotely-received retraction.
func (m *Model) retractSelectedMsg() tea.Cmd {
	chatIdx := m.currentChatIndex()
	if chatIdx < 0 {
		return nil
	}
	msgs := m.currentMessages()
	if m.selectedMsg < 0 || m.selectedMsg >= len(msgs) {
		return nil
	}
	msgs[m.selectedMsg].Retracted = true
	var cmd tea.Cmd
	if m.selectedMsg == len(msgs)-1 {
		cmd = m.setChatLastMessage(m.currentAccount, chatIdx, "message deleted")
	}
	return cmd
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
		m.setSelectedView(viewChats)
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
	var cmd tea.Cmd
	if idx == len(msgs)-1 {
		if chatIdx := m.currentChatIndex(); chatIdx >= 0 {
			preview := msgs[idx].Content
			if len(msgs[idx].Reactions) > 0 {
				preview = "reacted " + renderReactions(msgs[idx].Reactions)
			}
			cmd = m.setChatLastMessage(m.currentAccount, chatIdx, preview)
		}
	}
	if _, err := m.sender.Send(m.currentAccount, chat.Address, "", SendOptions{
		ReactionTargetID: msgs[idx].ID,
		Reactions:        newMine,
	}); err != nil {
		return tea.Batch(cmd, m.showNotification("reaction not delivered: "+err.Error()))
	}
	return cmd
}
