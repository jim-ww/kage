package ui

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"

	"github.com/jim-ww/kage/ui/emojipicker"
	"github.com/jim-ww/kage/ui/modal"
)

// localIDSeq backs newLocalID.
var localIDSeq atomic.Uint64

// newLocalID returns a fresh Message.LocalID.
func newLocalID() string {
	return fmt.Sprintf("local-%d-%d", time.Now().UnixNano(), localIDSeq.Add(1))
}

// trimComposed drops blank lines and trailing whitespace, keeping the first
// line's indentation so a pasted block stays aligned. Text with nothing
// visible comes back empty.
func trimComposed(s string) string {
	if isBlank(s) {
		return ""
	}
	s = strings.TrimRightFunc(s, unicode.IsSpace)
	for {
		line, rest, ok := strings.Cut(s, "\n")
		if !ok || !isBlank(line) {
			return s
		}
		s = rest
	}
}

// isBlank reports whether s shows nothing: only whitespace and invisible
// format characters (zero-width space, joiners, direction marks...).
func isBlank(s string) bool {
	for _, r := range s {
		if !unicode.IsSpace(r) && !unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

// sendCurrentInput sends what's composed - a message, reply, edit or
// attachments - and clears the input.
func (m *Model) sendCurrentInput() tea.Cmd {
	var cmds []tea.Cmd

	text := trimComposed(m.input.Value())
	hasAttachments := len(m.pendingAttachments) > 0 && m.editingMsg.empty()
	if text == "" && !hasAttachments {
		return nil
	}
	chatIdx := m.currentChatIndex()
	if chatIdx < 0 {
		return nil
	}
	shown := len(m.currentMessages())

	if hasAttachments {
		chat, ok := m.currentChat()
		if !ok || chat.Address == "" {
			return m.showNotification("no chat selected")
		}
		var sendOpts SendOptions
		if !m.replyTo.empty() {
			sendOpts = m.pendingReplyOptions()
			m.replyTo = msgRef{}
		}
		for _, a := range m.pendingAttachments {
			delete(m.finishedTransfers, a.path)
		}
		if text != "" {
			// The caption is its own message (see startAttachedSend).
			cmds = append(cmds, m.sendText(text, chat, sendOpts))
		}
		cmds = append(cmds, m.startAttachedSend(chat.Address, sendOpts))
		m.pendingAttachments = nil
		m.selectedAttachment = -1
		m.notifyTypingStopped()
		m.restoreStashedDraft()
		cmds = append(cmds, m.saveChatDraft(m.currentAccount, chatIdx, m.input.Value()))
		m.updateSizes()
		return tea.Batch(cmds...)
	}

	if !m.editingMsg.empty() {
		// An edit, sent as a XEP-0308 correction if the message has an ID.
		// Looked up by identity: indices move while the edit is typed.
		msgs := m.currentMessages()
		editIdx := m.editingMsg.index(msgs)
		switch {
		case editIdx < 0:
			cmds = append(cmds, m.showNotification("message no longer loaded; edit not saved"))
		case text == msgs[editIdx].Content:
			// Unchanged edit: nothing to send or record.
		default:
			msgs[editIdx].Content = text
			msgs[editIdx].Edited = true
			m.setCurrentMessages(msgs)
			if editIdx == len(msgs)-1 {
				if chatIdx := m.currentChatIndex(); chatIdx >= 0 {
					cmds = append(cmds, m.setChatLastMessage(m.currentAccount, chatIdx, MessagePreviewContent(msgs[editIdx])))
				}
			}

			if chat, ok := m.currentChat(); ok && chat.Address != "" && msgs[editIdx].ID != "" {
				_, err := m.daemon.Send(m.currentAccount, chat.Address, text, SendOptions{
					ReplaceID: msgs[editIdx].ID,
				})
				if err != nil {
					cmds = append(cmds, m.showNotification("edit not delivered: "+err.Error()))
				}
			}
		}
		m.editingMsg = msgRef{}
		m.input.Placeholder = "message..."
	} else {
		// Send new message, optionally quoting a reply.
		var sendOpts SendOptions
		if !m.replyTo.empty() {
			sendOpts = m.pendingReplyOptions()
			m.replyTo = msgRef{}
		}
		chat, ok := m.currentChat()
		if !ok || chat.Address == "" {
			// Nothing sent: keep the typed text rather than clearing it.
			cmds = append(cmds, m.showNotification("not connected; message not sent"))
			if chatIdx := m.currentChatIndex(); chatIdx >= 0 {
				cmds = append(cmds, m.saveChatDraft(m.currentAccount, chatIdx, m.input.Value()))
			}
			m.updateSizes()
			return tea.Batch(cmds...)
		}
		cmds = append(cmds, m.sendText(text, chat, sendOpts))
	}

	m.notifyTypingStopped()
	m.restoreStashedDraft()
	// Re-read: the send re-sorted the chat list.
	if chatIdx := m.currentChatIndex(); chatIdx >= 0 {
		cmds = append(cmds, m.saveChatDraft(m.currentAccount, chatIdx, m.input.Value()))
	}
	m.updateSizes()
	if len(m.currentMessages()) > shown && m.autofocus.covers(true) {
		m.selectedMsg = len(m.currentMessages()) - 1
	}
	m.refreshViewport()
	m.viewport.GotoBottom()
	return tea.Batch(cmds...)
}

// pendingReplyOptions is the reply half of a SendOptions for m.replyTo. A
// target no longer loaded is still replied to by ID (XEP-0461), only without
// the quote; one without an ID yet can't be.
func (m Model) pendingReplyOptions() SendOptions {
	msgs := m.currentMessages()
	if idx := m.replyTo.index(msgs); idx >= 0 {
		rt := msgs[idx]
		if rt.ID == "" {
			return SendOptions{}
		}
		return SendOptions{
			ReplyToID:    rt.ID,
			QuotedAuthor: rt.Author,
			QuotedBody:   MessagePreviewContent(rt),
		}
	}
	if m.replyTo.id == "" {
		return SendOptions{}
	}
	return SendOptions{ReplyToID: m.replyTo.id}
}

// replySendOptions is the reply half of a SendOptions for replyToID, quoting
// the target if it's loaded.
func replySendOptions(msgs []Message, replyToID string) SendOptions {
	if replyToID == "" {
		return SendOptions{}
	}
	opts := SendOptions{ReplyToID: replyToID}
	if rt := messageIndexByID(msgs, replyToID); rt >= 0 {
		opts.QuotedAuthor = msgs[rt].Author
		opts.QuotedBody = MessagePreviewContent(msgs[rt])
	}
	return opts
}

// sendText sends text to chat and echoes it into the chat right away -
// a plain message, or the caption typed alongside staged attachments, which
// is always its own message, never bundled into an attachment's body.
// sendOpts carries the reply target (if any), captured by the caller before
// m.replyTo is cleared.
func (m *Model) sendText(text string, chat Chat, sendOpts SendOptions) tea.Cmd {
	newMsg := Message{
		Author:    "me",
		Content:   text,
		SentAt:    time.Now(),
		IsMe:      true,
		ReplyToID: sendOpts.ReplyToID,
		LocalID:   newLocalID(),
	}
	sendOpts.LocalID = newMsg.LocalID

	var cmds []tea.Cmd
	id, err := m.daemon.Send(m.currentAccount, chat.Address, text, sendOpts)
	switch {
	case err == nil:
		newMsg.ID = id
		// Send never falls back to plaintext, so the chat's mode is how it
		// went out.
		switch mode := m.encryptionModeOrDefault(chat.EncryptionMode); {
		case mode == "gpg", mode == "omemo-v1", mode == "omemo-v2":
			newMsg.Encrypted, newMsg.EncMethod = true, mode
		case mode != "none":
			// Legacy stored mode (e.g. removed "omemo-auto") - the actual
			// protocol was resolved by the daemon; unknown here.
			newMsg.Encrypted, newMsg.EncMethod = true, "omemo"
		}
	case errors.Is(err, ErrQueued):
		// Queued for replay once the account reconnects (see the daemon's
		// flushOutbox), shown as Pending until MessageSendResolvedMsg
		// reconciles it by LocalID.
		newMsg.Pending = true
		cmds = append(cmds, m.showNotification("offline; message queued"))
	default:
		// A real failure - still shown (the text isn't lost) but flagged.
		newMsg.Failed = true
		cmds = append(cmds, m.showNotification("send failed: "+err.Error()))
	}

	msgs := append(m.currentMessages(), newMsg)
	m.setCurrentMessages(msgs)
	if chatIdx := m.currentChatIndex(); chatIdx >= 0 {
		cmds = append(cmds, m.setChatLastMessage(m.currentAccount, chatIdx, MessagePreviewContent(newMsg)))
	}
	return tea.Batch(cmds...)
}

// The action* methods act on the selected message or chat, for keys and
// menus alike.

// actionDeleteMessage asks to delete the selected message; a retracted one,
// to purge it (see purgeSelectedMsg).
func (m *Model) actionDeleteMessage() tea.Cmd {
	if m.currentChatIndex() < 0 {
		return nil
	}
	msgs := m.currentMessages()
	if len(msgs) == 0 {
		return m.showNotification("no messages to delete")
	}
	if m.selectedMsg < 0 || m.selectedMsg >= len(msgs) {
		return nil
	}
	msg := msgs[m.selectedMsg]
	if msg.Retracted {
		// Second delete on an already-retracted message: this one really
		// erases it, here and in storage. The detail shows the hidden
		// original, not MessagePreviewContent's "message deleted".
		detail := fmt.Sprintf("%s: %s\n", msg.Author, previewText(messagePreviewBody(msg), previewLen))
		return m.confirm("Erase this message for good?",
			detail+"Wipes the original from local storage. Can't be undone; the peer's copy stays.",
			func(m *Model) tea.Cmd {
				cmd := m.purgeSelectedMsg()
				m.refreshViewport()
				return cmd
			})
	}
	return m.confirm("Delete message?", fmt.Sprintf("%s: %s", msg.Author, previewText(MessagePreviewContent(msg), previewLen)),
		(*Model).deleteSelectedMessage)
}

// actionRetryMessage resends a failed message under its LocalID, so a
// MessageSendResolvedMsg for a queued retry still finds it.
func (m *Model) actionRetryMessage() tea.Cmd {
	if m.currentChatIndex() < 0 {
		return nil
	}
	msgs := m.currentMessages()
	if m.selectedMsg < 0 || m.selectedMsg >= len(msgs) || !msgs[m.selectedMsg].Failed {
		return m.showNotification("no failed message selected")
	}
	idx := m.selectedMsg
	if len(msgs[idx].PendingAttachmentPaths) > 0 {
		// Failed attachments (see failedAttachmentPlaceholder): upload again.
		chat, ok := m.currentChat()
		if !ok || chat.Address == "" {
			return m.showNotification("not connected; message not sent")
		}
		reply := replySendOptions(msgs, msgs[idx].ReplyToID)
		return m.retryAttachedSend(chat.Address, reply, msgs[idx].PendingAttachmentPaths, msgs[idx].LocalID)
	}
	if len(msgs[idx].Attachments) > 0 {
		return m.showNotification("can't retry attachments")
	}
	chat, ok := m.currentChat()
	if !ok || chat.Address == "" {
		return m.showNotification("not connected; message not sent")
	}

	sendOpts := SendOptions{LocalID: msgs[idx].LocalID}
	if sendOpts.LocalID == "" {
		sendOpts.LocalID = newLocalID()
	}
	if msgs[idx].ID != "" {
		// See SendOptions.SupersedesID.
		sendOpts.SupersedesID = msgs[idx].ID
	}
	if reply := replySendOptions(msgs, msgs[idx].ReplyToID); reply.ReplyToID != "" {
		sendOpts.ReplyToID = reply.ReplyToID
		sendOpts.QuotedAuthor = reply.QuotedAuthor
		sendOpts.QuotedBody = reply.QuotedBody
	}

	var cmds []tea.Cmd
	id, err := m.daemon.Send(m.currentAccount, chat.Address, msgs[idx].Content, sendOpts)
	msgs[idx].LocalID = sendOpts.LocalID
	switch {
	case err == nil:
		msgs[idx].Failed = false
		msgs[idx].ID = id
		switch mode := m.encryptionModeOrDefault(chat.EncryptionMode); {
		case mode == "gpg":
			msgs[idx].Encrypted, msgs[idx].EncMethod = true, "gpg"
		case mode == "omemo-v1", mode == "omemo-v2":
			msgs[idx].Encrypted, msgs[idx].EncMethod = true, mode
		case mode != "none":
			msgs[idx].Encrypted, msgs[idx].EncMethod = true, "omemo"
		}
	case errors.Is(err, ErrQueued):
		msgs[idx].Failed = false
		msgs[idx].Pending = true
		cmds = append(cmds, m.showNotification("offline; message queued"))
	default:
		cmds = append(cmds, m.showNotification("retry failed: "+err.Error()))
	}

	m.setCurrentMessages(msgs)
	if chatIdx := m.currentChatIndex(); chatIdx >= 0 && idx == len(msgs)-1 {
		cmds = append(cmds, m.setChatLastMessage(m.currentAccount, chatIdx, MessagePreviewContent(msgs[idx])))
	}
	m.refreshViewport()
	return tea.Batch(cmds...)
}

// actionLeaveChat opens the leave-chat confirmation popup for the selected
// chat (viewChats' DeleteMsg / a chat-item context-menu's "Leave chat").
func (m *Model) actionHideChat() tea.Cmd {
	chat, ok := m.currentChat()
	if !ok {
		return m.showNotification("no chat selected")
	}
	detail := chat.Name
	if chat.Address != "" {
		detail = fmt.Sprintf("%s <%s>", chat.Name, chat.Address)
	}
	return m.confirm("Hide chat?", detail+"\nNothing is deleted — unhide it from the contact manager.",
		(*Model).hideSelectedChat)
}

// actionRenameChat asks for the selected chat's name, prefilled with its
// custom one if any - not with the JID, which would pin it as the name.
func (m *Model) actionRenameChat() tea.Cmd {
	chat, ok := m.currentChat()
	if !ok {
		return m.showNotification("no chat selected")
	}

	value := ""
	if chat.Name != chat.Address {
		value = chat.Name
	}
	idx := m.currentChatIndex()
	return m.openModal(&modal.Modal[*Model]{
		Title: "Rename chat",
		Body:  m.newInput(value, chat.Address, "Name: ", "rename"),
		OnSubmit: func(m *Model, v any) tea.Cmd {
			return m.renameChat(idx, v.(string))
		},
	})
}

// renameChat names the chat at idx; "" clears the name.
func (m *Model) renameChat(idx int, typed string) tea.Cmd {
	items := m.chats.Items()
	if idx < 0 || idx >= len(items) {
		return nil
	}
	chat, ok := items[idx].(Chat)
	if !ok {
		return nil
	}

	name := strings.TrimSpace(typed)
	if name == "" {
		name = chat.Address
	}
	if name == chat.Name {
		return nil
	}

	if chat.Address != "" {
		if err := m.daemon.RenameContact(m.currentAccount, chat.Address, strings.TrimSpace(typed)); err != nil {
			return m.showNotification("rename failed: " + err.Error())
		}
	}

	oldName := chat.Name
	chat.Name = name
	m.accounts[m.currentAccount].Chats[idx] = chat

	// Message.Author is the name, so loaded messages need it changed too.
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

// actionSearchChat asks for a query to search the chat's whole history for.
func (m *Model) actionSearchChat() tea.Cmd {
	chat, ok := m.currentChat()
	if !ok || m.currentChatIndex() < 0 {
		return m.showNotification("no chat selected")
	}

	days, err := m.daemon.MessageDays(m.currentAccount, chat.Address)
	if err != nil {
		slog.Warn("listing message days", "chat", chat.Address, "err", err)
	}
	date := modal.NewDate(m.newSearchDatePicker(days))
	date.SubmitLabel = "go"
	return m.openModal(&modal.Modal[*Model]{
		Title: "Search",
		Body: modal.NewTabs(
			modal.Tab{Title: "Search text", Body: m.newInput("", "search messages", "/ ", "search")},
			modal.Tab{Title: "Go to date", Body: date},
		),
		OnSubmit: func(m *Model, v any) tea.Cmd {
			r := v.(modal.TabResult)
			if day, ok := r.Value.(time.Time); ok {
				return m.jumpToDate(day)
			}
			return m.searchChat(r.Value.(string))
		},
	})
}

// searchChat searches the chat, opening the results busy meanwhile.
func (m *Model) searchChat(query string) tea.Cmd {
	query = strings.TrimSpace(query)
	chat, ok := m.currentChat()
	if !ok || query == "" {
		return nil
	}

	accountIdx := m.currentAccount
	to := chat.Address
	return tea.Batch(m.openSearchResults(accountIdx, chat, query), m.daemon.SearchHistory(accountIdx, to, query))
}

func (m *Model) actionYankMessage() tea.Cmd {
	if err := m.yankSelectedMsg(); err != nil {
		return m.showNotification("copy failed")
	}
	return m.showNotification("message copied")
}

func (m *Model) actionYankDraft() tea.Cmd {
	if m.input.Value() == "" {
		return nil
	}
	if err := clipboard.WriteAll(m.input.Value()); err != nil {
		return m.showNotification("copy failed")
	}
	return m.showNotification("draft copied")
}

func (m *Model) actionEditMessage() tea.Cmd {
	if m.currentChatIndex() < 0 {
		return nil
	}
	msgs := m.currentMessages()
	idx := m.selectedMsg
	if idx < 0 || idx >= len(msgs) || !msgs[idx].IsMe {
		idx = lastOwnMessageIdx(msgs)
	}
	if idx < 0 {
		return m.showNotification("no message to edit")
	}
	if len(msgs[idx].Attachments) > 0 {
		return m.showNotification("can't edit attachments")
	}
	old := m.selectedMsg
	m.selectedMsg = idx
	m.refreshViewportScrollTo(old, idx)
	m.stashDraftForCompose()
	m.editingMsg = refMessage(msgs[idx])
	m.input.SetValue(msgs[idx].Content)
	m.resetDraftHistory(msgs[idx].Content)
	m.input.Placeholder = "edit message..."
	return m.input.Focus()
}

// lastOwnMessageIdx is the index of our last message, -1 if none.
func lastOwnMessageIdx(msgs []Message) int {
	for i := range slices.Backward(msgs) {
		if msgs[i].IsMe {
			return i
		}
	}
	return -1
}

func (m *Model) actionReplyMessage() tea.Cmd {
	if m.currentChatIndex() < 0 {
		return nil
	}
	if len(m.currentMessages()) == 0 {
		return m.showNotification("no message to reply to")
	}
	msgs := m.currentMessages()
	if m.selectedMsg < 0 || m.selectedMsg >= len(msgs) {
		return nil
	}
	if target := refMessage(msgs[m.selectedMsg]); m.replyTo == target {
		m.replyTo = msgRef{} // pressed/clicked again on the same message: clear reply
	} else {
		m.replyTo = target
	}
	m.updateSizes()
	m.refreshViewport()
	return m.input.Focus()
}

// actionToggleExpandMessage expands the selected message if it's a long
// one shown collapsed, or collapses it back - what clicking its expand
// button does.
func (m *Model) actionToggleExpandMessage() tea.Cmd {
	idx := m.selectedMsg
	expanded, ok := m.messageCollapsible(idx)
	if !ok {
		return m.showNotification("message is shown in full")
	}
	m.expandedMsgs[msgKey(m.currentMessages()[idx], idx)] = !expanded
	m.refreshViewportFullScrollTo(idx)
	return nil
}

// messageCollapsible reports whether message idx is long enough to be
// shown collapsed (ok), and whether it's currently expanded. Long is a
// matter of how it wraps at the chat's width, so it's asked of the
// renderer: the message drawn expanded and collapsed, compared.
func (m Model) messageCollapsible(idx int) (expanded, ok bool) {
	msgs := m.currentMessages()
	if idx < 0 || idx >= len(msgs) {
		return false, false
	}
	key := msgKey(msgs[idx], idx)
	nameWidth := maxSenderNameWidth(msgs)
	probe := m
	probe.expandedMsgs = map[string]bool{key: true}
	full := strings.Count(probe.renderMessage(msgs[idx], idx, m.chatAreaWidth(), msgs, nameWidth), "\n")
	probe.expandedMsgs = map[string]bool{}
	short := strings.Count(probe.renderMessage(msgs[idx], idx, m.chatAreaWidth(), msgs, nameWidth), "\n")
	return m.expandedMsgs[key], full != short
}

func (m *Model) actionInfoMessage() tea.Cmd {
	msgs := m.currentMessages()
	if m.selectedMsg < 0 || m.selectedMsg >= len(msgs) {
		return m.showNotification("no message selected")
	}

	// Attachment sizes are fetched here, on asking for the info, not on
	// render: each is a HEAD request (see fetchAttachmentSizeCmd).
	msg := msgs[m.selectedMsg]
	chat, _ := m.currentChat()
	var cmds []tea.Cmd
	for _, a := range msg.Attachments {
		if _, ok := attachmentLocalSize(a, chat.Address); ok {
			continue
		}
		if _, ok := m.attachmentSizes[a]; ok || m.attachmentSizeFetching[a] || m.attachmentSizeFailed[a] {
			continue
		}
		if m.attachmentSizeFetching == nil {
			m.attachmentSizeFetching = make(map[string]bool)
		}
		m.attachmentSizeFetching[a] = true
		cmds = append(cmds, fetchAttachmentSizeCmd(a))
	}
	text := modal.NewText(m.infoLines()...)
	text.ActLabel = "copy URL"
	cmds = append(cmds, m.openModal(&modal.Modal[*Model]{
		ID:    infoModalID,
		Title: "Message info",
		Body:  text,
		OnAct: func(m *Model, v any) tea.Cmd {
			if err := clipboard.WriteAll(v.(string)); err != nil {
				return m.showNotification("copy failed")
			}
			return m.showNotification("URL copied")
		},
	}))
	return tea.Batch(cmds...)
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
		return m.openItemMenu("Open which?", items, len(msgs[m.selectedMsg].Attachments), func(m *Model, target string, isAttachment bool) tea.Cmd {
			return m.startOpen(target, isAttachment)
		})
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
		return m.openItemMenu("Save which?", items, len(msgs[m.selectedMsg].Attachments), func(m *Model, target string, _ bool) tea.Cmd {
			return m.startSave(target)
		})
	}
}

// actionSaveMessageAs is actionSaveMessage asking where to save, after
// picking which item if there are several.
func (m *Model) actionSaveMessageAs() tea.Cmd {
	msgs := m.currentMessages()
	if m.selectedMsg < 0 || m.selectedMsg >= len(msgs) {
		return m.showNotification("no message selected")
	}
	items := openableItems(msgs[m.selectedMsg])
	switch len(items) {
	case 0:
		return m.showNotification("nothing to save")
	case 1:
		return m.openSaveAsPrompt(items[0])
	default:
		return m.openItemMenu("Save which as?", items, len(msgs[m.selectedMsg].Attachments), func(m *Model, target string, _ bool) tea.Cmd {
			return m.openSaveAsPrompt(target)
		})
	}
}

// openSaveAsPrompt asks where to save target, prefilled with where a plain
// save would.
func (m *Model) openSaveAsPrompt(target string) tea.Cmd {
	dest := target
	if dir, err := downloadsDir(); err == nil {
		dest = uniqueDestPath(dir, attachmentDisplayName(target))
	}

	return m.openModal(&modal.Modal[*Model]{
		Title: "Save as",
		Body:  m.newInput(dest, "", "Path: ", "save"),
		OnSubmit: func(m *Model, v any) tea.Cmd {
			// An empty path cancels instead of saving nowhere.
			if dest := strings.TrimSpace(v.(string)); dest != "" {
				return m.startSaveAs(target, dest)
			}
			return nil
		},
	})
}

// actionReactMessage opens the emoji picker on the selected message.
func (m *Model) actionReactMessage() tea.Cmd {
	msgs := m.currentMessages()
	if m.selectedMsg < 0 || m.selectedMsg >= len(msgs) {
		return m.showNotification("no message selected")
	}
	idx := m.selectedMsg
	picker := emojipicker.New(rankReactionUsage(m.reactionEmojiUsage))
	picker.SetPicked(myReactionsEmoji(msgs[idx].Reactions))
	picker.Compact = true // the modal titles it and lists its keys
	applyEmojiPickerStyles(&picker, m.styles.colors)
	return tea.Batch(picker.Init(), m.openModal(&modal.Modal[*Model]{
		Title:    "React to: " + previewText(MessagePreviewContent(msgs[idx]), previewLen),
		Body:     &emojiPickerBody{picker: &picker},
		OnSubmit: func(m *Model, v any) tea.Cmd { return m.confirmEmojiPick(idx, v.([]string)) },
	}))
}

// openPendingChat opens pendingOpenChatAddress once the current account's
// chats are loaded.
func (m *Model) openPendingChat() tea.Cmd {
	if m.pendingOpenChatAddress == "" {
		return nil
	}
	addr := m.pendingOpenChatAddress
	for i, item := range m.chats.Items() {
		if chat, ok := item.(Chat); ok && chat.Address == addr {
			m.pendingOpenChatAddress = ""
			m.landedOnce = true
			m.chats.Select(i)
			model, cmd := m.openCurrentChat()
			*m = model.(Model)
			return cmd
		}
	}
	// Kept: Init runs this before any account is loaded.
	return nil
}

// landedInChat does what opening it would to the chat the TUI starts in,
// which nothing opened: shows the newest message and clears its unread
// count. Called by whichever message first brings the chats in, it acts
// once (landedOnce); the snapshot re-arms it.
func (m *Model) landedInChat() tea.Cmd {
	if m.landedOnce || m.currentChatIndex() < 0 {
		return nil
	}
	m.landedOnce = true
	// Not found: a later arrival mustn't jump to it mid-session.
	m.pendingOpenChatAddress = ""
	if m.selectedView != viewChat {
		return nil
	}
	if msgs := m.currentMessages(); len(msgs) > 0 {
		m.selectedMsg = len(msgs) - 1
	}
	m.refreshViewport()
	m.viewport.GotoBottom()
	return m.resetChatUnread(m.currentAccount, m.currentChatIndex())
}

// readOpenChat marks the chat on screen read, when focus or input returns:
// messages arriving meanwhile counted as unread (see isChatFocused).
func (m *Model) readOpenChat() tea.Cmd {
	if m.selectedView != viewChat {
		return nil
	}
	return m.resetChatUnread(m.currentAccount, m.currentChatIndex())
}

func (m Model) openCurrentChat() (tea.Model, tea.Cmd) {
	chatIdx := m.currentChatIndex()
	if chatIdx < 0 {
		return m, nil
	}
	m.setSelectedView(viewChat)
	// Left mid-history, load the latest window: messages arriving meanwhile
	// weren't appended (see IncomingMessageMsg).
	var jumpCmd tea.Cmd
	if m.currentAccount >= 0 && m.currentAccount < len(m.accounts) &&
		m.accounts[m.currentAccount].HistoryNewer[chatIdx] {
		jumpCmd = m.jumpToLatestMessage()
	}
	if msgs := m.currentMessages(); len(msgs) > 0 {
		m.selectedMsg = len(msgs) - 1
	}
	if m.currentAccount >= 0 && m.currentAccount < len(m.accounts) {
		if chat, ok := m.currentChat(); ok {
			_ = m.daemon.SaveState(LastChat{AccountJID: m.accounts[m.currentAccount].Name, ChatAddress: chat.Address})
		}
	}
	draftCmd := m.swapComposeDraft(m.currentAccount, m.currentChatIndex())
	m.updateSizes()
	// After updateSizes: the draft changes the viewport's height.
	m.refreshViewport()
	m.viewport.GotoBottom()
	unreadCmd := m.resetChatUnread(m.currentAccount, m.currentChatIndex())
	return m, tea.Batch(draftCmd, unreadCmd, m.input.Focus(), jumpCmd)
}

// canEdit returns true only when selectedMsg is an "IsMe" message and isn't
// an attachment (its body is the upload URL, not editable text).
func (m Model) canEdit(msgs []Message) bool {
	if m.selectedMsg < 0 || m.selectedMsg >= len(msgs) {
		return false
	}
	return msgs[m.selectedMsg].IsMe && len(msgs[m.selectedMsg].Attachments) == 0
}

// retractSelectedMsg flags the selected message deleted; its content is kept.
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
		cmd = m.setChatLastMessage(m.currentAccount, chatIdx, retractedPreview)
	}
	return cmd
}

// purgeSelectedMsg erases a retracted message for good, locally - the second
// delete (see Daemon.PurgeMessage).
func (m *Model) purgeSelectedMsg() tea.Cmd {
	chatIdx := m.currentChatIndex()
	if chatIdx < 0 {
		return nil
	}
	msgs := m.currentMessages()
	if m.selectedMsg < 0 || m.selectedMsg >= len(msgs) {
		return nil
	}
	idx := m.selectedMsg
	wasLast := idx == len(msgs)-1
	target := msgs[idx]

	var cmds []tea.Cmd
	// A message with no stanza ID was never persisted under one (so there's
	// nothing for the daemon to look up) - it only has to leave this view.
	if target.ID != "" {
		if chat, ok := m.currentChat(); ok && chat.Address != "" {
			if err := m.daemon.PurgeMessage(m.currentAccount, chat.Address, target.ID); err != nil {
				cmds = append(cmds, m.showNotification("delete not saved: "+err.Error()))
			}
		}
	}

	newMsgs, selectedMsg := removeMessageAt(msgs, idx, idx)
	m.setCurrentMessages(newMsgs)
	m.selectedMsg = selectedMsg
	if wasLast {
		preview := ""
		if len(newMsgs) > 0 {
			preview = MessagePreviewContent(newMsgs[len(newMsgs)-1])
		}
		cmds = append(cmds, m.setChatLastMessage(m.currentAccount, chatIdx, preview))
	}
	return tea.Batch(cmds...)
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

// hideSelectedChat takes the selected chat out of the list, deleting nothing.
// It comes back from the contact manager, or when they write unless
// auto_unhide_disabled.
func (m *Model) hideSelectedChat() tea.Cmd {
	return m.hideChat(m.currentAccount, m.currentChatIndex())
}

// sendReaction sets our reactions on the message at idx to newMine
// (XEP-0444).
func (m *Model) sendReaction(idx int, newMine []string) tea.Cmd {
	msgs := m.currentMessages()
	if idx < 0 || idx >= len(msgs) {
		return nil
	}
	if msgs[idx].ID == "" {
		return m.showNotification("message has no id; can't react")
	}
	chat, ok := m.currentChat()
	if !ok || chat.Address == "" {
		return nil
	}

	msgs[idx].Reactions = setMyReactions(msgs[idx].Reactions, newMine)
	var cmd tea.Cmd
	if idx == len(msgs)-1 {
		if chatIdx := m.currentChatIndex(); chatIdx >= 0 {
			preview := MessagePreviewContent(msgs[idx])
			if len(msgs[idx].Reactions) > 0 {
				preview = "reacted " + renderReactions(msgs[idx].Reactions)
			}
			cmd = m.setChatLastMessage(m.currentAccount, chatIdx, preview)
		}
	}
	if _, err := m.daemon.Send(m.currentAccount, chat.Address, "", SendOptions{
		ReactionTargetID: msgs[idx].ID,
		Reactions:        newMine,
	}); err != nil {
		return tea.Batch(cmd, m.showNotification("reaction not delivered: "+err.Error()))
	}
	return cmd
}
