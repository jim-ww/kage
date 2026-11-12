package ui

import (
	"errors"
	"fmt"
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

// localIDSeq backs newLocalID - only needs to be unique among messages
// currently held in memory (see Message.LocalID's doc comment), so a
// per-process counter is enough; no need for anything cryptographic or
// persisted across restarts.
var localIDSeq atomic.Uint64

// newLocalID returns a fresh Message.LocalID.
func newLocalID() string {
	return fmt.Sprintf("local-%d-%d", time.Now().UnixNano(), localIDSeq.Add(1))
}

// trimComposed drops the blank lines and trailing whitespace around what
// was typed, but keeps the first line's indentation: trimmed like the rest
// of the text, a pasted indented block would come out with its first line
// shifted left of the others. Text with nothing visible in it - only
// spaces, or invisible characters like a zero-width space - comes back
// empty, so it isn't sent.
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

// sendCurrentInput performs the SelectSend action for viewChat: sends the
// composed message (wired as an edit/reply/reaction as appropriate given
// the current compose state), clears the input, and refreshes the
// viewport. Shared by the SelectSend keybinding and a click on the mouse
// send button — both must produce identical behavior.
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
		// Uploading (and the send it feeds into) runs async, same as
		// SendFile — the compose box is cleared optimistically, but unlike
		// a plain text send there's no message to echo into the chat until
		// ComposedSendResultMsg reports the upload actually succeeded.
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
			// A caption typed alongside staged attachments always goes out as
			// its own independent message - never folded into an attachment's
			// body (see startAttachedSend's doc comment: other clients only
			// read the first XEP-0066 URL in a message body anyway, and
			// combining also entangled the caption's send/retry state with
			// the attachment's own upload/queue state).
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
		// Apply edit in-place, and wire it as a XEP-0308 correction so the
		// other party actually sees the update — a message can only be
		// corrected on the network if it was sent with an ID in the first
		// place (e.g. locally-seeded/demo data never was), so degrade to a
		// local-only edit otherwise.
		//
		// The target is looked up by identity every time rather than held as
		// an index: messages arriving while the edit is being typed can trim
		// the front of the chat (maxMessagesPerChat), which would slide a
		// remembered index onto a neighbouring message - one that may not
		// even be ours - and correct that instead.
		msgs := m.currentMessages()
		editIdx := m.editingMsg.index(msgs)
		switch {
		case editIdx < 0:
			// The message went out of the loaded window while it was being
			// edited. Nothing to apply the edit to; say so rather than
			// writing it onto whatever sits nearby now.
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
			// Nothing was ever handed to Send - showing a local echo here
			// would be a message the network has never heard of and never
			// will, indistinguishable on screen from one that actually went
			// out. Surface the problem and bail before the shared tail below
			// clears m.input via restoreStashedDraft - the typed text is
			// worth keeping around for a retry, not silently dropped along
			// with the failed send.
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
	// Re-read rather than reusing the chatIdx this function opened with: the
	// send bumped the chat's activity, which re-sorts the list (see
	// setChatLastMessage), and the old index now names a different chat -
	// whose draft this would otherwise overwrite with the cleared input.
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

// pendingReplyOptions turns the pending reply target (m.replyTo) into the reply
// half of a SendOptions. The target is located by identity, never by the
// index the reply was started at - see Model.replyTo.
//
// A target that has left the loaded window (trimmed off the front, or a
// history window loaded around something else) still threads the reply by
// its stanza ID: that is all XEP-0461 needs, and dropping the reply because
// the quote preview can no longer be built would silently ignore what the
// user asked for. Only the in-band quote fallback goes missing. A target
// with no stanza ID yet (a message still being sent) can't be replied to at
// all, and sends as a plain message.
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

// replySendOptions builds the reply half of a SendOptions for a message
// whose reply target is replyToID, filling the quoted author/body fallback
// from the target if it's still in the loaded window (it may not be - a
// reply older than the window it was loaded in still knows what it replies
// to, it just can't quote it). Zero value when replyToID is empty.
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
		// Send only succeeds without falling back to plaintext, so a
		// configured encryption mode here means the message really went out
		// encrypted - mirrors what the daemon persists, which this echo has
		// no way to read back before the next reload.
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

// The action* methods below each implement one message/chat action against
// the current selection (m.selectedMsg / m.currentChatIndex()). They exist
// so the DeleteMsg/YankMsg/EditMsg/etc. keybindings and the mouse
// context-menu (see ui/contextmenu.go) share one implementation instead of
// drifting apart.

// actionDeleteMessage opens the delete-confirmation popup for the selected
// message (viewChat's DeleteMsg / a message context-menu's "Delete").
// Deleting a message that's already flagged retracted - i.e. a second delete
// on content the chat view is already hiding - asks for the irreversible
// local purge instead (see purgeSelectedMsg).
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

// actionRetryMessage re-sends the selected message after a failed send
// (Message.Failed). Reuses the same LocalID so a later MessageSendResolvedMsg
// (if this retry itself only gets queued, e.g. the account just went offline
// again) still finds and patches this row.
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
		// A failed startAttachedSend batch (see failedAttachmentPlaceholder) -
		// re-upload rather than the plain-text retry below, since there's
		// nothing sent yet for sender.Send to correct/supersede.
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
		// Already made it into storage under a real ID before being flagged
		// Failed (an ack-timeout, not an offline/immediate failure) - see
		// SendOptions.SupersedesID's doc comment.
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

// renameChat names the chat at idx: pushes the name to the server as a
// roster set and mirrors it locally (see Daemon), then updates the
// in-memory chat name shown in the sidebar. An empty name clears the custom
// one — the chat falls back to displaying its JID, matching what a fresh
// roster fetch would show.
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

// actionSearchChat opens the search-in-chat query prompt (viewChat's
// SearchChat keybind, Ctrl+/). Submitting it (see submitSearchChat) searches
// the chat's entire persisted history, not just what's currently loaded.
func (m *Model) actionSearchChat() tea.Cmd {
	if m.currentChatIndex() < 0 {
		return m.showNotification("no chat selected")
	}

	date := modal.NewDate(m.newSearchDatePicker())
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

// searchChat kicks off
// historySearcher.SearchHistory as a tea.Cmd, opening the results busy so
// they show at once while the scan runs.
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

// lastOwnMessageIdx returns the index of the last message with IsMe set, or
// -1 if the chat has none — what ctrl+e edits when the selected message
// isn't ours (callers must separately reject it if it's an attachment).
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

	// Fetch a size for each attachment on this message that doesn't already
	// have one locally measurable and hasn't already been fetched/attempted
	// this session - deliberately only here (an explicit "tell me about this
	// message" action), not on every render, since it means an HTTP HEAD
	// request to whatever server hosts the file (see fetchAttachmentSizeCmd).
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

// actionSaveMessageAs is actionSaveMessage's counterpart for Ctrl+Shift+S:
// instead of downloading straight to the default downloads directory, it
// prompts for a destination path first (openSaveAsPrompt). With multiple
// links/attachments on the message, the item picker opens first (in
// "save as" mode) so the user chooses which one before being prompted for
// where to put it.
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

// openSaveAsPrompt opens the single-field "save as" popup for target,
// prefilled with the default downloads-directory destination so submitting
// unchanged behaves the same as a plain save.
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

// actionReactMessage opens the emoji picker popup (see ui/emojipicker) on
// the selected message. The compose box is untouched — reacting no longer
// goes through it at all, unlike edit/reply — so there's no stash/restore
// dance here the way actionEditMessage needs.
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

// openPendingChat opens pendingOpenChatAddress (set from config's
// open_last_chat/last_chat_address on startup) once the current account's
// chats have loaded and contain a matching chat, then clears it so it's
// only attempted once.
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
	// Kept for the next attempt rather than consumed here: Init fires this
	// before any account exists (the model is deliberately built with no
	// accounts so the first frame doesn't wait on the daemon - see runTUI),
	// so the chat list is empty on that first pass and the address has to
	// survive it for AccountsSnapshotMsg/AccountLiveMsg to honour. Clearing
	// it here dropped the last-opened chat on the floor, which also meant
	// the TUI came up sitting in a chat that nothing had actually *opened*:
	// no openCurrentChat, so its stored unread count was never reset and the
	// chat-list badge and tray dot stayed on a chat being read.
	return nil
}

// landedInChat covers the chat the TUI comes up *in* without anything
// having opened it: the model is constructed in viewChat with the chat
// list's cursor on the first row (and runTUI builds it with no accounts at
// all, so the chats only arrive with the snapshot), which means the user is
// reading that chat although openCurrentChat never ran for it. So do the two
// things opening it would have done - put the view on the newest message,
// scrolled to the bottom, instead of leaving it at the top of the loaded
// page, and clear the chat's stored unread count so its chat-list badge and
// the tray dot don't stay lit on a chat being read.
//
// Called from every message that can first bring the current account's chats
// in - the snapshot, AccountConnectedMsg, AccountLiveMsg - since which one
// wins the race depends on whether the daemon was already running. It only
// ever acts once (landedOnce): any later chat arrival (AccountLiveMsg fires
// on every reconnect) must leave the viewport where the user left it. The
// snapshot re-arms it, being the attach-time truth about every account and
// arriving before the user can have scrolled anywhere.
func (m *Model) landedInChat() tea.Cmd {
	if m.landedOnce || m.currentChatIndex() < 0 {
		return nil
	}
	m.landedOnce = true
	// The restore had its chance and the chat wasn't there (hidden, or gone
	// from the roster); dropping it keeps a much later chat arrival from
	// yanking the user into it mid-session.
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

// readOpenChat marks the chat on screen as read, if there is one, because
// the user is looking at it again: the terminal just regained focus, or the
// session came back from idle. Those are exactly the states in which an
// arriving message counts as unread even though its chat never stopped being
// open (see isChatFocused), so without this the chat-list badge and the tray
// dot would stay lit on a chat being read until the user navigated away and
// back.
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
	// If the loaded window isn't the live tail (left mid-history from a
	// previous page-up, e.g. by switching to another chat and back without
	// jumping to latest first), opening the chat must fetch back to the true
	// tail rather than just scrolling to the bottom of the stale window -
	// otherwise a message that arrived while HistoryNewer was set never got
	// appended (see IncomingMessageMsg's HistoryNewer branch) and stays
	// invisible until something else happens to trigger this same fetch.
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
	// Rendered and scrolled after updateSizes, not before: swapping in this
	// chat's draft changes the compose box's height and so the viewport's,
	// and a bottom computed against the old height isn't the bottom once the
	// pane has been resized under it.
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
		cmd = m.setChatLastMessage(m.currentAccount, chatIdx, retractedPreview)
	}
	return cmd
}

// purgeSelectedMsg erases the current message locally for good - the second
// step of the two-step delete, only reachable for a message already flagged
// Retracted. Unlike retractSelectedMsg nothing is kept: the storage row and
// its reactions go (adapter.PurgeMessage, which also tells every other
// attached client via MessagePurgedMsg) and the message leaves the chat.
// Local-only - there's no stanza that erases a message from the peer's copy.
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

// hideSelectedChat takes the selected chat out of the list. Nothing is
// deleted: the contact stays in the roster, message history stays in
// storage, and the chat comes back from the contact manager or — unless
// auto_unhide_disabled is set — the next time they write. This replaced a
// "leave chat" that removed the row locally and nothing else, which read
// as destructive while being purely cosmetic, and silently dropped that
// contact's incoming messages for the rest of the session.
func (m *Model) hideSelectedChat() tea.Cmd {
	return m.hideChat(m.currentAccount, m.currentChatIndex())
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
