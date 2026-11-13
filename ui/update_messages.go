package ui

import (
	"maps"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jim-ww/kage/ui/modal"
)

// trimMessagesFront drops the oldest of msgs past limit (<= 0: none),
// returning how many, for indices into them to shift.
func trimMessagesFront(msgs []Message, limit int) ([]Message, int) {
	if limit <= 0 || len(msgs) <= limit {
		return msgs, 0
	}
	drop := len(msgs) - limit
	trimmed := make([]Message, limit)
	copy(trimmed, msgs[drop:])
	return trimmed, drop
}

// appendAndTrim appends newMsgs to a chat's loaded messages and trims them
// to maxMessagesPerChat, setting HistoryMore if any were dropped. The
// selection stays on its message (see followTail).
func (m *Model) appendAndTrim(accountIdx, chatIdx int, newMsgs ...Message) {
	old := m.accounts[accountIdx].Messages[chatIdx]
	isOpenChat := accountIdx == m.currentAccount && chatIdx == m.currentChatIndex()
	msgs := append(old, newMsgs...)
	trimmed, drop := trimMessagesFront(msgs, m.maxMessagesPerChat)
	if drop > 0 {
		if m.accounts[accountIdx].HistoryMore == nil {
			m.accounts[accountIdx].HistoryMore = make(map[int]bool)
		}
		m.accounts[accountIdx].HistoryMore[chatIdx] = true
	}
	m.accounts[accountIdx].Messages[chatIdx] = trimmed
	if isOpenChat {
		m.selectedMsg = max(0, m.selectedMsg-drop)
	}
}

// followTail repaints the open chat after appendAndTrim: at the bottom if it
// was, else on the selected message. Before any other refresh, as AtBottom
// must see the old content.
func (m *Model) followTail() {
	if m.viewport.AtBottom() {
		m.refreshViewport()
		m.viewport.GotoBottom()
		return
	}
	m.refreshViewportFullScrollTo(m.selectedMsg)
}

// mergeLiveTail appends to fresh, a just-loaded tail window, the messages of
// old newer than fresh's last: ones that arrived while it loaded.
func mergeLiveTail(fresh, old []Message) []Message {
	if len(old) == 0 {
		return fresh
	}
	seen := make(map[string]bool, len(fresh))
	for _, m := range fresh {
		if m.ID != "" {
			seen[m.ID] = true
		}
	}
	var cutoff time.Time
	if len(fresh) > 0 {
		cutoff = fresh[len(fresh)-1].SentAt
	}
	for _, m := range old {
		if m.ID != "" && seen[m.ID] {
			continue
		}
		if m.SentAt.After(cutoff) {
			fresh = append(fresh, m)
		}
	}
	return fresh
}

// handleEventMsg handles every message but keys; handled is false for
// those.
func (m Model) handleEventMsg(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.termHeight = msg.Height
		m.updateSizes()
		m.refreshViewport()
		m.viewport.GotoBottom()
		return m, nil, true

	case tea.FocusMsg:
		m.focused = true
		return m, nil, true

	case tea.BlurMsg:
		m.focused = false
		return m, nil, true

	case tea.MouseClickMsg:
		if !m.mouseEnabled {
			return m, nil, true
		}
		model, cmd := m.handleMouseClick(msg)
		return model.(Model), cmd, true

	case tea.MouseWheelMsg:
		if !m.mouseEnabled {
			return m, nil, true
		}
		model, cmd := m.handleMouseWheel(msg)
		return model.(Model), cmd, true

	case tea.MouseMotionMsg:
		if !m.mouseEnabled {
			return m, nil, true
		}
		model, cmd := m.handleMouseMotion(msg)
		return model.(Model), cmd, true

	case tea.MouseReleaseMsg:
		if !m.mouseEnabled {
			return m, nil, true
		}
		model, cmd := m.handleMouseRelease(msg)
		return model.(Model), cmd, true

	case openPendingChatMsg:
		return m, m.openPendingChat(), true

	case noticeClearMsg:
		if msg.id == m.noticeID {
			m.noticeText = ""
		}
		return m, nil, true

	case transferProgressChanMsg:
		m.setTransferProgress(msg.FileTransferProgressMsg)
		return m, listenForTransferChan(msg.ch), true

	case openResultMsg:
		m.clearTransfer(msg.target)
		delete(m.downloadsInFlight, msg.target)
		label := msg.target
		if msg.isAttachment {
			label = attachmentDisplayName(msg.target)
		}
		if msg.err != nil {
			return m, m.showNotification("failed to open " + label), true
		}
		return m, m.showNotification("opened " + label), true

	case attachmentSizeMsg:
		delete(m.attachmentSizeFetching, msg.target)
		if msg.ok {
			if m.attachmentSizes == nil {
				m.attachmentSizes = make(map[string]int64)
			}
			m.attachmentSizes[msg.target] = msg.size
		} else {
			if m.attachmentSizeFailed == nil {
				m.attachmentSizeFailed = make(map[string]bool)
			}
			m.attachmentSizeFailed[msg.target] = true
		}
		if info := m.modals.Find(infoModalID); info != nil {
			info.Body.(*modal.Text).SetLines(m.infoLines()...)
		}
		return m, nil, true

	case saveResultMsg:
		m.clearTransfer(msg.target)
		delete(m.downloadsInFlight, msg.target)
		if msg.err != nil {
			return m, m.showNotification("save failed: " + msg.err.Error()), true
		}
		return m, m.showNotification("saved " + msg.path), true

	case FileTransferProgressMsg:
		m.setTransferProgress(msg)
		return m, nil, true

	case FileTransferDoneMsg:
		m.clearTransfer(msg.ID)
		return m, nil, true

	case clipboardImageResultMsg:
		if msg.err != nil {
			return m, m.showNotification("paste image: " + msg.err.Error()), true
		}
		m.stageAttachment(msg.path)
		return m, nil, true

	case ComposedSendResultMsg:
		for _, path := range msg.Paths {
			m.clearTransfer(path)
		}
		if len(msg.Messages) == 0 {
			if msg.Err != nil {
				notification := "send failed: " + msg.Err.Error()
				if msg.Err.Error() == uploadCanceledMsg {
					notification = uploadCanceledMsg
				}
				cmd, ok := m.showFailedAttachmentPlaceholder(msg)
				if !ok {
					return m, m.showNotification(notification), true
				}
				return m, tea.Batch(m.showNotification(notification), cmd), true
			}
			if msg.Queued {
				// A pending placeholder, as a queued text gets.
				chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.To)
				cmd := m.showNotification("offline — attachment(s) queued, will send on reconnect")
				if chatIdx < 0 || msg.QueuedLocalID == "" {
					return m, cmd, true
				}
				placeholder := Message{
					LocalID: msg.QueuedLocalID,
					Author:  "me",
					Content: "[queued: " + filepath.Base(msg.QueuedPath) + "]",
					SentAt:  time.Now(),
					IsMe:    true,
					Pending: true,
				}
				m.appendAndTrim(msg.AccountIdx, chatIdx, placeholder)
				// Asked before setChatLastMessage: the activity bump it
				// applies re-sorts the list, after which chatIdx names a
				// different chat (see IncomingMessageMsg).
				isOpenChat := msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex()
				cmd = tea.Batch(cmd, m.setChatLastMessage(msg.AccountIdx, chatIdx, MessagePreviewContent(placeholder)))
				if isOpenChat {
					m.followNew(true)
				}
				return m, cmd, true
			}
			return m, nil, true
		}
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.To)
		if chatIdx < 0 {
			return m, m.showNotification("message sent"), true
		}
		if m.accounts[msg.AccountIdx].Messages == nil {
			m.accounts[msg.AccountIdx].Messages = make(map[int][]Message)
		}
		existing := m.accounts[msg.AccountIdx].Messages[chatIdx]
		var lastContent string
		newMsgs := make([]Message, 0, len(msg.Messages))
		for _, sent := range msg.Messages {
			newMsg := Message{
				ID:          sent.ID,
				LocalID:     sent.LocalID,
				Author:      "me",
				Content:     sent.Content,
				SentAt:      time.Now(),
				IsMe:        true,
				Attachments: sent.Attachments,
			}
			newMsg.ReplyToID = msg.ReplyToID
			lastContent = MessagePreviewContent(newMsg)
			// Each message's broadcast usually arrived before this batch
			// result, so is shown already. Not a retry's placeholder, which
			// is patched in place below.
			isRetriedPlaceholder := msg.RetryOfLocalID != "" && newMsg.LocalID == msg.RetryOfLocalID
			if !isRetriedPlaceholder &&
				(messageIndexByID(existing, newMsg.ID) >= 0 || messageIndexByLocalID(existing, newMsg.LocalID) >= 0) {
				continue
			}
			newMsgs = append(newMsgs, newMsg)
		}
		// A retry's first outcome patches the existing Failed placeholder
		// (by LocalID) in place instead of appending a duplicate row — any
		// further files in the batch still append as usual.
		if msg.RetryOfLocalID != "" && len(newMsgs) > 0 {
			if idx := messageIndexByLocalID(existing, msg.RetryOfLocalID); idx >= 0 {
				newMsgs[0].LocalID = msg.RetryOfLocalID
				existing[idx] = newMsgs[0]
				m.accounts[msg.AccountIdx].Messages[chatIdx] = existing
				newMsgs = newMsgs[1:]
			}
		}
		m.appendAndTrim(msg.AccountIdx, chatIdx, newMsgs...)
		var cmds []tea.Cmd
		cmds = append(cmds, m.setChatLastMessage(msg.AccountIdx, chatIdx, lastContent))
		// Re-resolved after the sort setChatLastMessage just applied - see
		// IncomingMessageMsg. Guarded rather than returned on, so a send
		// failure is still reported even if the chat went away meanwhile.
		chatIdx = m.chatIndexByAddress(msg.AccountIdx, msg.To)
		if msg.Err != nil {
			notification := "send failed: " + msg.Err.Error()
			if msg.Err.Error() == uploadCanceledMsg {
				notification = uploadCanceledMsg
			}
			cmds = append(cmds, m.showNotification(notification))
			if placeholder, ok := failedAttachmentPlaceholder(msg); ok && chatIdx >= 0 {
				m.appendAndTrim(msg.AccountIdx, chatIdx, placeholder)
			}
		}
		if chatIdx >= 0 && msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
			m.followNew(true)
		}
		return m, tea.Batch(cmds...), true

	case FileSendResultMsg:
		m.clearTransfer(msg.Path)
		if msg.Err != nil {
			if msg.Err.Error() == uploadCanceledMsg {
				return m, m.showNotification(uploadCanceledMsg), true
			}
			return m, m.showNotification("file send failed: " + msg.Err.Error()), true
		}
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.To)
		if chatIdx < 0 {
			return m, m.showNotification("file sent: " + filepath.Base(msg.Path)), true
		}
		if m.accounts[msg.AccountIdx].Messages == nil {
			m.accounts[msg.AccountIdx].Messages = make(map[int][]Message)
		}
		newMsg := Message{
			ID:          msg.ID,
			Author:      "me",
			Content:     msg.URL,
			SentAt:      time.Now(),
			IsMe:        true,
			Attachments: []string{msg.URL},
		}
		newMsg.ReplyToID = msg.ReplyToID
		lastMsgCmd := m.setChatLastMessage(msg.AccountIdx, chatIdx, MessagePreviewContent(newMsg))
		notice := m.showNotification("file sent: " + filepath.Base(msg.Path))
		// Re-resolved after the sort setChatLastMessage just applied - see
		// IncomingMessageMsg.
		if chatIdx = m.chatIndexByAddress(msg.AccountIdx, msg.To); chatIdx < 0 {
			return m, tea.Batch(lastMsgCmd, notice), true
		}
		// Same race as ComposedSendResultMsg's batch: the upload runs async,
		// so the daemon's broadcast of this very message can land before the
		// result does and already be in the chat.
		if messageIndexByID(m.accounts[msg.AccountIdx].Messages[chatIdx], newMsg.ID) >= 0 {
			return m, tea.Batch(lastMsgCmd, notice), true
		}
		m.appendAndTrim(msg.AccountIdx, chatIdx, newMsg)
		if msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
			m.followNew(true)
		}
		return m, tea.Batch(lastMsgCmd, notice), true

	case IncomingMessageMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			// A hidden chat comes back when they write, unless
			// auto_unhide_disabled.
			if m.autoUnhideDisabled || !m.isChatHidden(msg.AccountIdx, msg.From) {
				return m, nil, true
			}
			unhideCmd := m.unhideChat(msg.AccountIdx, msg.From)
			if chatIdx = m.chatIndexByAddress(msg.AccountIdx, msg.From); chatIdx < 0 {
				return m, unhideCmd, true
			}
			next, cmd, handled := m.handleEventMsg(msg)
			return next, tea.Batch(unhideCmd, cmd), handled
		}
		if messageIndexByID(m.accounts[msg.AccountIdx].Messages[chatIdx], msg.Message.ID) >= 0 {
			// Our own message, shown already when we sent it.
			return m, nil, true
		}
		if messageIndexByLocalID(m.accounts[msg.AccountIdx].Messages[chatIdx], msg.Message.LocalID) >= 0 {
			// A replayed queued send, whose placeholder has no ID yet.
			return m, nil, true
		}
		if m.accounts[msg.AccountIdx].HistoryNewer[chatIdx] {
			// A mid-history window isn't the tail to append to; the message
			// shows once the latest window loads.
			return m, nil, true
		}
		if m.accounts[msg.AccountIdx].Messages == nil {
			m.accounts[msg.AccountIdx].Messages = make(map[int][]Message)
		}
		// Their message ends their typing; not every client says "active".
		var typingCmd tea.Cmd
		if !msg.Message.IsMe {
			typingCmd = m.setPeerTyping(msg.AccountIdx, msg.From, false)
		}
		newMsg := msg.Message
		newMsg.ReplyToID = msg.ReplyToID
		m.appendAndTrim(msg.AccountIdx, chatIdx, newMsg)
		cmd := m.setChatLastMessage(msg.AccountIdx, chatIdx, MessagePreviewContent(newMsg))
		// Looked up again: the new activity re-sorted the chats.
		if chatIdx = m.chatIndexByAddress(msg.AccountIdx, msg.From); chatIdx < 0 {
			return m, tea.Batch(typingCmd, cmd), true
		}
		if m.isChatOnScreen(msg.AccountIdx, chatIdx) {
			m.followNew(newMsg.IsMe)
		}
		return m, tea.Batch(typingCmd, cmd), true

	case HistoryWindowMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil, true
		}
		delete(m.loadingHistoryWindow, chatIdx)
		anchorID := m.pendingWindowAnchor[chatIdx]
		delete(m.pendingWindowAnchor, chatIdx)
		if msg.Err != nil {
			// Only the in-flight mark is cleared, so paging can retry.
			return m, m.showNotification("loading history: " + msg.Err.Error()), true
		}
		if m.accounts[msg.AccountIdx].HistoryMore == nil {
			m.accounts[msg.AccountIdx].HistoryMore = make(map[int]bool)
		}
		if m.accounts[msg.AccountIdx].HistoryNewer == nil {
			m.accounts[msg.AccountIdx].HistoryNewer = make(map[int]bool)
		}
		m.accounts[msg.AccountIdx].HistoryMore[chatIdx] = msg.HasOlder
		m.accounts[msg.AccountIdx].HistoryNewer[chatIdx] = msg.HasNewer
		if m.accounts[msg.AccountIdx].Messages == nil {
			m.accounts[msg.AccountIdx].Messages = make(map[int][]Message)
		}
		// The window replaces what was loaded; a tail window keeps messages
		// that arrived while it loaded (see mergeLiveTail).
		newMessages := msg.Messages
		if !msg.HasNewer {
			newMessages = mergeLiveTail(newMessages, m.accounts[msg.AccountIdx].Messages[chatIdx])
		}
		m.accounts[msg.AccountIdx].Messages[chatIdx] = newMessages
		if msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
			// The selection stays on its message, by ID; else the last one.
			if idx := messageIndexByID(newMessages, anchorID); idx >= 0 {
				m.selectedMsg = idx
			} else {
				m.selectedMsg = len(newMessages) - 1
			}
			if j := m.pendingDateJump; j != nil && j.accountIdx == msg.AccountIdx && j.chatAddress == msg.From {
				m.pendingDateJump = nil
				m.landOnDate(newMessages, j.day)
				return m, nil, true
			}
			m.refreshViewportFullScrollToCentered(m.selectedMsg)
		}
		return m, nil, true

	case HistorySearchResultMsg:
		md := m.modals.Find(searchResultsModalID)
		if md == nil {
			return m, nil, true
		}
		body := md.Body.(*searchResultsBody)
		if body.accountIdx != msg.AccountIdx || body.chatAddress != msg.From || body.query != msg.Query {
			// A later search (or the results being closed) superseded this
			// result — discard it rather than overwrite unrelated state.
			return m, nil, true
		}
		body.status = ""
		if msg.Err != nil {
			body.err = msg.Err.Error()
			return m, nil, true
		}
		body.messages, body.matches = msg.Messages, msg.Matches
		body.relist()
		return m, nil, true

	case MessageCorrectedMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil, true
		}
		msgs := m.accounts[msg.AccountIdx].Messages[chatIdx]
		idx := messageIndexByID(msgs, msg.ReplaceID)
		if idx < 0 {
			return m, nil, true
		}
		msgs[idx].Content = msg.NewContent
		msgs[idx].Encrypted = msg.Encrypted
		msgs[idx].EncMethod = msg.EncMethod
		msgs[idx].Edited = true
		// Asked before setChatLastMessage: the activity bump it applies
		// re-sorts the list, after which chatIdx names a different chat (see
		// IncomingMessageMsg).
		isOpenChat := msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex()
		var cmd tea.Cmd
		if idx == len(msgs)-1 {
			cmd = m.setChatLastMessage(msg.AccountIdx, chatIdx, MessagePreviewContent(msgs[idx]))
		}
		if isOpenChat {
			m.refreshViewport()
		}
		return m, cmd, true

	case MessageRetractedMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil, true
		}
		msgs := m.accounts[msg.AccountIdx].Messages[chatIdx]
		idx := messageIndexByID(msgs, msg.RetractID)
		if idx < 0 {
			return m, nil, true
		}
		msgs[idx].Retracted = true
		// Asked before setChatLastMessage: the activity bump it applies
		// re-sorts the list, after which chatIdx names a different chat (see
		// IncomingMessageMsg).
		isOpenChat := msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex()
		var cmd tea.Cmd
		if idx == len(msgs)-1 {
			cmd = m.setChatLastMessage(msg.AccountIdx, chatIdx, retractedPreview)
		}
		if isOpenChat {
			m.refreshViewport()
		}
		return m, cmd, true

	case MessageDeliveredMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil, true
		}
		msgs := m.accounts[msg.AccountIdx].Messages[chatIdx]
		idx := messageIndexByID(msgs, msg.MessageID)
		if idx < 0 {
			return m, nil, true
		}
		msgs[idx].Delivered = true
		if msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
			m.refreshViewport()
		}
		return m, nil, true

	case MessageServerAckedMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.To)
		if chatIdx < 0 {
			return m, nil, true
		}
		msgs := m.accounts[msg.AccountIdx].Messages[chatIdx]
		idx := messageIndexByID(msgs, msg.MessageID)
		if idx < 0 {
			return m, nil, true
		}
		msgs[idx].ServerAcked = true
		if msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
			m.refreshViewport()
		}
		return m, nil, true

	case MessageSendFailedMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.To)
		if chatIdx < 0 {
			return m, nil, true
		}
		msgs := m.accounts[msg.AccountIdx].Messages[chatIdx]
		idx := messageIndexByID(msgs, msg.MessageID)
		if idx < 0 {
			return m, nil, true
		}
		if msg.Reason != "" {
			// Bounced by the recipient's side: not delivered, whatever our
			// own server's ack said - and worth saying even with the chat
			// open, since a ✗ alone doesn't say why.
			msgs[idx].Failed, msgs[idx].ServerAcked = true, false
			if msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
				m.refreshViewport()
			}
			return m, m.showNotification("not delivered to " + msg.To + ": " + msg.Reason), true
		}
		if msgs[idx].ServerAcked {
			// Lost the race against a real ack that arrived just before the
			// connection was declared dead - trust the ack, not the timeout.
			return m, nil, true
		}
		msgs[idx].Failed = true
		var cmd tea.Cmd
		if msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
			m.refreshViewport()
		} else {
			cmd = m.showNotification("send failed: connection dropped")
		}
		return m, cmd, true

	case MessageSendResolvedMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.To)
		if chatIdx < 0 {
			return m, nil, true
		}
		msgs := m.accounts[msg.AccountIdx].Messages[chatIdx]
		idx := messageIndexByLocalID(msgs, msg.LocalID)
		if idx < 0 {
			return m, nil, true
		}
		msgs[idx].Pending = false
		var cmd tea.Cmd
		if msg.Err != "" {
			msgs[idx].Failed = true
			cmd = m.showNotification("send failed: " + msg.Err)
		} else {
			msgs[idx].ID = msg.ID
			msgs[idx].Encrypted = msg.Encrypted
			msgs[idx].EncMethod = msg.EncMethod
			if msg.Attachments != nil {
				msgs[idx].Content = msg.Content
				msgs[idx].Attachments = msg.Attachments
			}
		}
		isOpenChat := msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex()
		lastMsgCmd := m.setChatLastMessage(msg.AccountIdx, chatIdx, MessagePreviewContent(msgs[idx]))
		if isOpenChat {
			m.refreshViewport()
		}
		return m, tea.Batch(cmd, lastMsgCmd), true

	case OutboxDeletedMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.To)
		if chatIdx < 0 {
			return m, nil, true
		}
		msgs := m.accounts[msg.AccountIdx].Messages[chatIdx]
		idx := messageIndexByLocalID(msgs, msg.LocalID)
		if idx < 0 {
			return m, nil, true
		}
		selectedMsg := m.selectedMsg
		if msg.AccountIdx != m.currentAccount || chatIdx != m.currentChatIndex() {
			selectedMsg = idx // not the open chat - removeMessageAt's adjustment is meaningless, just needs to not go negative below
		}
		msgs, selectedMsg = removeMessageAt(msgs, idx, selectedMsg)
		m.accounts[msg.AccountIdx].Messages[chatIdx] = msgs
		if msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
			m.selectedMsg = selectedMsg
			m.refreshViewport()
		}
		return m, nil, true

	case MessagePurgedMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.Peer)
		if chatIdx < 0 {
			return m, nil, true
		}
		msgs := m.accounts[msg.AccountIdx].Messages[chatIdx]
		idx := messageIndexByID(msgs, msg.MessageID)
		if idx < 0 {
			return m, nil, true
		}
		isOpenChat := msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex()
		selectedMsg := m.selectedMsg
		if !isOpenChat {
			selectedMsg = idx // same as OutboxDeletedMsg: no live selection to adjust, just keep it in range
		}
		wasLast := idx == len(msgs)-1
		msgs, selectedMsg = removeMessageAt(msgs, idx, selectedMsg)
		m.accounts[msg.AccountIdx].Messages[chatIdx] = msgs
		var cmd tea.Cmd
		if wasLast {
			preview := ""
			if len(msgs) > 0 {
				preview = MessagePreviewContent(msgs[len(msgs)-1])
			}
			cmd = m.setChatLastMessage(msg.AccountIdx, chatIdx, preview)
		}
		if isOpenChat {
			m.selectedMsg = selectedMsg
			m.refreshViewport()
		}
		return m, cmd, true

	case MessageReactionsMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil, true
		}
		msgs := m.accounts[msg.AccountIdx].Messages[chatIdx]
		idx := messageIndexByID(msgs, msg.MessageID)
		if idx < 0 {
			return m, nil, true
		}
		msgs[idx].Reactions = msg.Reactions
		// Asked before setChatLastMessage: the activity bump it applies
		// re-sorts the list, after which chatIdx names a different chat (see
		// IncomingMessageMsg).
		isOpenChat := msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex()
		var cmd tea.Cmd
		if idx == len(msgs)-1 {
			preview := MessagePreviewContent(msgs[idx])
			if len(msg.Reactions) > 0 {
				preview = "reacted " + renderReactions(msg.Reactions)
			}
			cmd = m.setChatLastMessage(msg.AccountIdx, chatIdx, preview)
		}
		if isOpenChat {
			m.refreshViewport()
		}
		return m, cmd, true

	case AccountAddedMsg:
		m.modals.Remove(addAccountModalID)
		if m.accountIndexByJID(msg.Account.Name) >= 0 {
			// Its AccountAppearedMsg came first and added it.
			return m, nil, true
		}
		m.accounts = append(m.accounts, msg.Account)
		return m, m.showNotification("account added: " + msg.Account.Name), true

	case AccountAppearedMsg:
		if m.accountIndexByJID(msg.Account.Name) >= 0 {
			return m, nil, true // already known: our own add, or a repeat
		}
		if msg.Index != len(m.accounts) {
			// Our list is out of line with the daemon's, so it can't be
			// placed; the next start resyncs.
			return m, nil, true
		}
		m.accounts = append(m.accounts, msg.Account)
		return m, m.showNotification("account added elsewhere: " + msg.Account.Name), true

	case AccountAddErrorMsg:
		if md := m.modals.Find(addAccountModalID); md != nil {
			body := md.Body.(*addAccountBody)
			body.SetBusy("")
			body.SetError(msg.Err.Error())
		}
		return m, nil, true

	case OmemoDeviceListMsg:
		m.deviceListResult(msg.AccountIdx, msg.Local, msg.Devices, msg.Err)
		return m, nil, true

	case OmemoDevicePurgedMsg:
		if !m.deviceListResult(msg.AccountIdx, msg.Local, msg.Devices, msg.Err) {
			return m, nil, true
		}
		return m, m.showNotification("omemo device list updated"), true

	case ChatAddedMsg:
		if msg.AccountIdx < 0 || msg.AccountIdx >= len(m.accounts) {
			return m, nil, true
		}
		// An upsert, so a push that merely renames a contact (another of our
		// clients editing the roster) updates the row in place instead of
		// appending a second one for the same address.
		if idx := m.chatIndexByAddress(msg.AccountIdx, msg.Chat.Address); idx >= 0 {
			chat, ok := m.accounts[msg.AccountIdx].Chats[idx].(Chat)
			if !ok {
				return m, nil, true
			}
			renamed := msg.Chat.Name != "" && chat.Name != msg.Chat.Name
			resubscribed := chat.Subscription != msg.Chat.Subscription || chat.SubscriptionPending != msg.Chat.SubscriptionPending
			if !renamed && !resubscribed {
				return m, nil, true
			}
			if renamed {
				chat.Name = msg.Chat.Name
			}
			chat.Subscription, chat.SubscriptionPending = msg.Chat.Subscription, msg.Chat.SubscriptionPending
			m.accounts[msg.AccountIdx].Chats[idx] = chat
			var cmd tea.Cmd
			if msg.AccountIdx == m.currentAccount {
				cmd = m.chats.SetItems(m.accounts[msg.AccountIdx].Chats)
			}
			return m, cmd, true
		}
		// A chat the user hid stays hidden: it goes to the stash, where
		// unhiding (or the auto-unhide an incoming message triggers) picks
		// it up, rather than reappearing in the list on its own.
		if m.isChatHidden(msg.AccountIdx, msg.Chat.Address) {
			return m, nil, true
		}
		chat := msg.Chat
		chat.Hidden = false
		if len(msg.Messages) > 0 {
			last := msg.Messages[len(msg.Messages)-1]
			chat.LastMessage = MessagePreviewContent(last)
			chat.LastActivity = last.SentAt
		}
		chatIdx := len(m.accounts[msg.AccountIdx].Chats)
		m.accounts[msg.AccountIdx].Chats = append(m.accounts[msg.AccountIdx].Chats, chat)
		if len(msg.Messages) > 0 {
			if m.accounts[msg.AccountIdx].Messages == nil {
				m.accounts[msg.AccountIdx].Messages = make(map[int][]Message)
			}
			m.accounts[msg.AccountIdx].Messages[chatIdx] = msg.Messages
		}
		var cmd tea.Cmd
		if msg.AccountIdx == m.currentAccount {
			cmd = m.chats.SetItems(m.accounts[msg.AccountIdx].Chats)
		}
		return m, cmd, true

	case ChatRemovedMsg:
		if msg.AccountIdx < 0 || msg.AccountIdx >= len(m.accounts) {
			return m, nil, true
		}
		idx := m.chatIndexByAddress(msg.AccountIdx, msg.Address)
		if !m.removeChatAt(msg.AccountIdx, idx) {
			return m, nil, true
		}
		var removeCmd tea.Cmd
		if msg.AccountIdx == m.currentAccount {
			removeCmd = m.chats.SetItems(m.accounts[msg.AccountIdx].Chats)
		}
		return m, removeCmd, true

	case ContactAddedMsg:
		body := m.contactsOpen()
		md := m.modals.Find(addContactModalID)
		if body == nil || md == nil || body.accountIdx != msg.AccountIdx {
			return m, nil, true
		}
		form := md.Body.(*modal.Form)
		form.SetBusy("")
		if msg.Err != nil {
			form.SetError(msg.Err.Error())
			return m, nil, true
		}
		m.modals.Remove(addContactModalID)
		var cmd tea.Cmd
		if m.chatIndexByAddress(msg.AccountIdx, msg.Address) < 0 {
			chat := Chat{Name: msg.Address, Address: msg.Address}
			m.accounts[msg.AccountIdx].Chats = append(m.accounts[msg.AccountIdx].Chats, chat)
			if msg.AccountIdx == m.currentAccount {
				cmd = m.chats.SetItems(m.accounts[msg.AccountIdx].Chats)
			}
		}
		m.refreshContacts()
		return m, tea.Batch(cmd, m.showNotification("contact added: "+msg.Address)), true

	case ContactRemovedMsg:
		if !m.contactResult(msg.AccountIdx, msg.Err) {
			return m, nil, true
		}
		var cmd tea.Cmd
		if idx := m.chatIndexByAddress(msg.AccountIdx, msg.Address); m.removeChatAt(msg.AccountIdx, idx) && msg.AccountIdx == m.currentAccount {
			cmd = m.chats.SetItems(m.accounts[msg.AccountIdx].Chats)
		}
		m.refreshContacts()
		return m, tea.Batch(cmd, m.showNotification("contact removed: "+msg.Address)), true

	case ContactResubscribedMsg:
		if !m.contactResult(msg.AccountIdx, msg.Err) {
			return m, nil, true
		}
		return m, m.showNotification("resubscribe sent: " + msg.Address), true

	case AccountConnectedMsg:
		if msg.Index < 0 || msg.Index >= len(m.accounts) {
			return m, nil, true
		}
		m.accounts[msg.Index] = msg.Account
		m.stripHiddenChats(msg.Index)
		cmd := m.sortChatsByActivity(msg.Index)
		if msg.Index == m.currentAccount {
			m.refreshViewport()
			cmd = tea.Batch(cmd, m.openPendingChat(), m.landedInChat())
		}
		return m, cmd, true

	case NoticeMsg:
		return m, m.showNotification(msg.Text), true

	case AccountsSnapshotMsg:
		if msg.Err != "" {
			return m, m.showNotification("loading accounts: " + msg.Err), true
		}
		cmd := m.installAccounts(msg.Accounts, msg.StartAccount)
		if msg.CallState != nil {
			m, _ = m.handleCallStateMsg(*msg.CallState)
		}
		m.refreshViewport()
		// Re-armed: an AccountConnectedMsg may have landed on the chats this
		// replaced.
		m.landedOnce = false
		return m, tea.Batch(cmd, m.openPendingChat(), m.landedInChat()), true

	case AccountLiveMsg:
		if msg.Index < 0 || msg.Index >= len(m.accounts) {
			return m, nil, true
		}
		m.accounts[msg.Index].Connecting = false
		m.accounts[msg.Index].ConnectError = ""
		m.accounts[msg.Index].SupportsInvisible = msg.SupportsInvisible
		var cmd tea.Cmd
		if len(msg.NewChats) > 0 {
			m.accounts[msg.Index].Chats = append(m.accounts[msg.Index].Chats, msg.NewChats...)
			if m.accounts[msg.Index].Messages == nil {
				m.accounts[msg.Index].Messages = make(map[int][]Message)
			}
			maps.Copy(m.accounts[msg.Index].Messages, msg.NewMessages)
			if m.accounts[msg.Index].HistoryMore == nil {
				m.accounts[msg.Index].HistoryMore = make(map[int]bool)
			}
			maps.Copy(m.accounts[msg.Index].HistoryMore, msg.NewHistoryMore)
			m.stripHiddenChats(msg.Index)
			cmd = m.sortChatsByActivity(msg.Index)
		}
		if msg.Index == m.currentAccount {
			m.refreshViewport()
			cmd = tea.Batch(cmd, m.openPendingChat(), m.landedInChat())
		}
		return m, cmd, true

	case AccountConnectErrorMsg:
		if msg.Index < 0 || msg.Index >= len(m.accounts) {
			return m, nil, true
		}
		m.accounts[msg.Index].Connecting = false
		m.accounts[msg.Index].ConnectError = msg.Err.Error()
		if msg.Index == m.currentAccount {
			m.refreshViewport()
		}
		return m, m.showNotification("account " + m.accounts[msg.Index].Name + " failed to connect: " + msg.Err.Error()), true

	case AccountStatusSetMsg:
		if msg.Index < 0 || msg.Index >= len(m.accounts) {
			return m, nil, true
		}
		if msg.Err != nil {
			return m, m.showNotification("setting status: " + msg.Err.Error()), true
		}
		return m, m.showNotification("status: " + presenceLabel(msg.Status)), true

	case AccountStatusChangedMsg:
		if msg.Index < 0 || msg.Index >= len(m.accounts) {
			return m, nil, true
		}
		return m, m.applyAccountStatus(msg), true

	case ChatSettingMsg:
		return m, m.applyChatSetting(msg), true

	case AccountRemovedMsg:
		if msg.Index < 0 || msg.Index >= len(m.accounts) {
			return m, nil, true
		}
		if m.accounts[msg.Index].Removed {
			// Already applied - the removing client gets both the RPC result
			// and the broadcast that tells every other client.
			return m, nil, true
		}
		name := m.accounts[msg.Index].DisplayName()
		m.accounts[msg.Index].Removed = true
		m.accounts[msg.Index].Connecting = false
		m.accounts[msg.Index].ConnectError = ""
		if msg.Index == m.currentAccount {
			m.refreshViewport()
		}
		return m, m.showNotification("removed account " + name), true

	case AccountRemoveErrorMsg:
		if msg.Index < 0 || msg.Index >= len(m.accounts) {
			return m, nil, true
		}
		return m, m.showNotification("removing account: " + msg.Err.Error()), true

	case HistorySyncStartedMsg:
		if msg.AccountIdx >= 0 && msg.AccountIdx < len(m.accounts) {
			m.accounts[msg.AccountIdx].SyncingHistory = true
		}
		return m, nil, true

	case HistorySyncFinishedMsg:
		if msg.AccountIdx >= 0 && msg.AccountIdx < len(m.accounts) {
			m.accounts[msg.AccountIdx].SyncingHistory = false
		}
		return m, nil, true

	case HistorySyncedMsg:
		if len(msg.Messages) == 0 {
			return m, nil, true
		}
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil, true
		}
		if m.accounts[msg.AccountIdx].Messages == nil {
			m.accounts[msg.AccountIdx].Messages = make(map[int][]Message)
		}
		m.appendAndTrim(msg.AccountIdx, chatIdx, msg.Messages...)
		lastMsgCmd := m.setChatLastMessage(msg.AccountIdx, chatIdx, MessagePreviewContent(msg.Messages[len(msg.Messages)-1]))
		// Re-resolved after the sort, same as IncomingMessageMsg above.
		if chatIdx = m.chatIndexByAddress(msg.AccountIdx, msg.From); chatIdx < 0 {
			return m, lastMsgCmd, true
		}
		// Same repaint as IncomingMessageMsg above.
		if m.isChatOnScreen(msg.AccountIdx, chatIdx) {
			m.followTail()
		}
		return m, lastMsgCmd, true

	case ChatUnreadMsg:
		accountIdx := m.accountIndexByJID(msg.AccountJID)
		chatIdx := m.chatIndexByAddress(accountIdx, msg.ChatAddress)
		if chatIdx < 0 {
			return m, nil, true
		}
		if msg.Count > 0 && m.isChatFocused(accountIdx, chatIdx) {
			// Counted before the daemon heard this client had the chat open
			// (focus is reported asynchronously); it's being read.
			return m, m.resetChatUnread(accountIdx, chatIdx), true
		}
		return m, m.setChatUnread(accountIdx, chatIdx, msg.Count), true

	case chatSwitchSettledMsg:
		if msg.gen != m.chatSwitchGen {
			return m, nil, true
		}
		chatIdx := m.currentChatIndex()
		if chatIdx < 0 {
			m.selectedMsg = 0
			m.refreshViewport()
			return m, nil, true
		}
		if msgs := m.currentMessages(); len(msgs) > 0 {
			m.selectedMsg = len(msgs) - 1
		} else {
			m.selectedMsg = 0
		}
		m.refreshViewport()
		m.viewport.GotoBottom()
		return m, nil, true

	case flashClearMsg:
		if msg.gen == m.flashGen {
			m.flashMsgIdx = -1
			m.refreshViewport()
		}
		return m, nil, true

	case typingPauseMsg:
		if m.typingActiveTo == msg.addr && m.typingGen == msg.gen {
			if err := m.daemon.SetTyping(m.currentAccount, msg.addr, false); err == nil {
				m.typingActiveTo = ""
			}
		}
		return m, nil, true

	case StoragePasswordChangedMsg:
		md := m.modals.Find(storagePasswordModalID)
		if md == nil {
			return m, nil, true
		}
		form := md.Body.(*modal.Form)
		form.SetBusy("")
		if msg.Err != nil {
			form.SetError(msg.Err.Error())
			return m, nil, true
		}
		m.modals.Remove(storagePasswordModalID)
		return m, m.showNotification("storage password changed — kage will restart shortly, please relaunch it"), true

	case draftSaveMsg:
		if m.draftSaveGen != msg.gen || m.currentAccount != msg.accountIdx {
			return m, nil, true
		}
		if chat, ok := m.currentChat(); ok && chat.Address == msg.addr {
			if chatIdx := m.currentChatIndex(); chatIdx >= 0 {
				return m, m.saveChatDraft(msg.accountIdx, chatIdx, m.input.Value()), true
			}
		}
		return m, nil, true

	case TypingMsg:
		return m, m.setPeerTyping(msg.AccountIdx, msg.From, msg.Typing), true

	case peerTypingExpiredMsg:
		if m.peerTypingGen[peerTypingKey(msg.accountIdx, msg.from)] != msg.gen {
			// A newer "composing" rearmed the timer, or something already
			// cleared the indicator - this one is stale.
			return m, nil, true
		}
		return m, m.setPeerTyping(msg.accountIdx, msg.from, false), true

	case IncomingCallMsg:
		model, cmd := m.handleIncomingCallMsg(msg)
		return model, cmd, true

	case CallStateMsg:
		model, cmd := m.handleCallStateMsg(msg)
		return model, cmd, true

	case callClearMsg:
		if m.call != nil && m.call.accountIdx == msg.accountIdx && m.call.gen == msg.gen {
			m.call = nil
			m.updateSizes()
			m.refreshContextMenu()
			m.refreshDesktopVolume()
		}
		return m, nil, true

	case CallActionResultMsg:
		if msg.Err != nil {
			return m, m.showNotification("call " + msg.Action.callAction() + " failed: " + msg.Err.Error()), true
		}
		return m, nil, true

	case AudioDevicesMsg:
		return m, m.handleAudioDevicesMsg(msg), true

	case MissedCallMsg:
		return m, m.showNotification("missed call from " + msg.From + " (busy)"), true

	case hoverDevicesRevealMsg:
		if msg.gen == m.hoverGen && m.hover != nil && m.hover.id == msg.id {
			m.hover.devicesID = msg.id
		}
		return m, nil, true

	case DeviceNameMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil, true
		}
		chat, ok := m.accounts[msg.AccountIdx].Chats[chatIdx].(Chat)
		if !ok {
			return m, nil, true
		}
		chat = chat.withResourceName(msg.Resource, msg.Name)
		m.accounts[msg.AccountIdx].Chats[chatIdx] = chat
		if msg.AccountIdx == m.currentAccount {
			cmd := m.chats.SetItem(chatIdx, chat)
			return m, cmd, true
		}
		return m, nil, true

	case avatarPreviewLoadedMsg:
		m.applyAvatarPreviewLoaded(msg)
		return m, nil, true
	case AvatarPublishedMsg:
		what := "avatar published"
		if msg.Removed {
			what = "avatar removed"
		}
		if msg.Err != nil {
			return m, m.showNotification(what + ": " + msg.Err.Error()), true
		}
		return m, m.showNotification(what), true
	case AvatarUpdatedMsg:
		// Decoded already; this only redraws.
		return m, nil, true
	case PresenceMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil, true
		}
		chat, ok := m.accounts[msg.AccountIdx].Chats[chatIdx].(Chat)
		if !ok {
			return m, nil, true
		}
		chat.Resources = WithResource(chat.Resources, msg.Resource, msg.Presence)
		if msg.Resource == "" {
			// No resource part to track individually - this stanza is the
			// whole story, same as before resource-aware aggregation existed.
			chat.Presence = msg.Presence
		} else {
			chat.Presence = AggregatePresence(chat.Resources)
		}
		m.accounts[msg.AccountIdx].Chats[chatIdx] = chat
		var cmd tea.Cmd
		if msg.AccountIdx == m.currentAccount {
			cmd = m.chats.SetItem(chatIdx, chat)
		}
		if chat.Presence == PresenceOffline {
			// They can't still be typing from a resource that just went
			// away, and the "active" that would have said so never comes.
			cmd = tea.Batch(cmd, m.setPeerTyping(msg.AccountIdx, msg.From, false))
		}
		return m, cmd, true
	}

	return m, nil, false
}
