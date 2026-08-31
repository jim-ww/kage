package ui

import (
	"maps"
	"path/filepath"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

// trimMessagesFront drops the oldest messages from msgs so at most limit
// remain, used when new messages are appended at the tail (live incoming
// traffic, MAM sync backfill) and the chat has grown past the configured
// cap. Returns the trimmed slice and how many messages were dropped so the
// caller can adjust any index (selectedMsg, ReplyTo) that pointed into the
// old slice. limit <= 0 disables trimming.
func trimMessagesFront(msgs []Message, limit int) ([]Message, int) {
	if limit <= 0 || len(msgs) <= limit {
		return msgs, 0
	}
	drop := len(msgs) - limit
	trimmed := make([]Message, limit)
	copy(trimmed, msgs[drop:])
	for i := range trimmed {
		if trimmed[i].ReplyTo == nil {
			continue
		}
		if *trimmed[i].ReplyTo < drop {
			trimmed[i].ReplyTo = nil
		} else {
			shifted := *trimmed[i].ReplyTo - drop
			trimmed[i].ReplyTo = &shifted
		}
	}
	return trimmed, drop
}

// appendAndTrim appends newMsgs to accounts[accountIdx].Messages[chatIdx]
// (the live tail: incoming messages, our own sent messages, MAM catch-up)
// and caps it back down to maxMessagesPerChat via trimMessagesFront. Unlike
// a HistoryLoader window load, this never touches HistoryNewer — the tail is
// still the tail — but does set HistoryMore whenever trimming actually
// dropped something, since storage now holds messages older than what's
// left in memory (recoverable at any time via an ordinary older-history
// fetch — nothing needs to be specially retained here, storage already has
// it).
func (m *Model) appendAndTrim(accountIdx, chatIdx int, newMsgs ...Message) []Message {
	msgs := append(m.accounts[accountIdx].Messages[chatIdx], newMsgs...)
	trimmed, drop := trimMessagesFront(msgs, m.maxMessagesPerChat)
	if drop > 0 {
		if m.accounts[accountIdx].HistoryMore == nil {
			m.accounts[accountIdx].HistoryMore = make(map[int]bool)
		}
		m.accounts[accountIdx].HistoryMore[chatIdx] = true
	}
	m.accounts[accountIdx].Messages[chatIdx] = trimmed
	return trimmed
}

// handleEventMsg handles every non-key message Update can receive (window
// resizes, mouse events, and all the async network/timer messages). handled
// is false only when msg isn't one of these — Update then falls through to
// key handling and focused-component routing.
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
				return m, m.showNotification("send failed: " + msg.Err.Error()), true
			}
			if msg.Queued {
				// Same local-echo treatment a plain-text queued send already
				// gets (see sendCurrentInput) — otherwise this message has no
				// on-screen representation at all until the upload+send
				// actually happens on reconnect, which could be an arbitrarily
				// long time away (see MessageSendResolvedMsg's Content/
				// Attachments fields, which patch this placeholder once
				// resolved).
				chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.To)
				cmd := m.showNotification("offline — attachment(s) queued, will send on reconnect")
				if chatIdx < 0 || msg.QueuedLocalID == "" {
					return m, cmd, true
				}
				content := msg.QueuedText
				name := "[queued: " + filepath.Base(msg.QueuedPath) + "]"
				if content != "" {
					content += "\n" + name
				} else {
					content = name
				}
				placeholder := Message{
					LocalID: msg.QueuedLocalID,
					Author:  "me",
					Content: content,
					SentAt:  time.Now(),
					IsMe:    true,
					Pending: true,
				}
				msgs := m.appendAndTrim(msg.AccountIdx, chatIdx, placeholder)
				cmd = tea.Batch(cmd, m.setChatLastMessage(msg.AccountIdx, chatIdx, MessagePreviewContent(placeholder)))
				if msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
					m.selectedMsg = len(msgs) - 1
					m.refreshViewport()
					m.viewport.GotoBottom()
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
				Author:      "me",
				Content:     sent.Content,
				SentAt:      time.Now(),
				IsMe:        true,
				Attachments: sent.Attachments,
			}
			if replyIdx := messageIndexByID(existing, msg.ReplyToID); replyIdx >= 0 {
				newMsg.ReplyTo = &replyIdx
			}
			newMsgs = append(newMsgs, newMsg)
			lastContent = MessagePreviewContent(newMsg)
		}
		msgs := m.appendAndTrim(msg.AccountIdx, chatIdx, newMsgs...)
		var cmds []tea.Cmd
		cmds = append(cmds, m.setChatLastMessage(msg.AccountIdx, chatIdx, lastContent))
		if msg.Err != nil {
			cmds = append(cmds, m.showNotification("send failed: "+msg.Err.Error()))
		}
		if msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
			m.selectedMsg = len(msgs) - 1
			m.refreshViewport()
			m.viewport.GotoBottom()
		}
		return m, tea.Batch(cmds...), true

	case FileSendResultMsg:
		m.clearTransfer(msg.Path)
		if msg.Err != nil {
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
		if replyIdx := messageIndexByID(m.accounts[msg.AccountIdx].Messages[chatIdx], msg.ReplyToID); replyIdx >= 0 {
			newMsg.ReplyTo = &replyIdx
		}
		msgs := m.appendAndTrim(msg.AccountIdx, chatIdx, newMsg)
		lastMsgCmd := m.setChatLastMessage(msg.AccountIdx, chatIdx, MessagePreviewContent(newMsg))
		if msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
			m.selectedMsg = len(msgs) - 1
			m.refreshViewport()
			m.viewport.GotoBottom()
		}
		return m, tea.Batch(lastMsgCmd, m.showNotification("file sent: "+filepath.Base(msg.Path))), true

	case IncomingMessageMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil, true
		}
		if messageIndexByID(m.accounts[msg.AccountIdx].Messages[chatIdx], msg.Message.ID) >= 0 {
			// Our own message, broadcast back to every attached client
			// (including the one that sent it - see adapter.go's send()) and
			// already rendered here as a local optimistic echo the instant
			// the Send RPC returned. Same message ID, so this is that
			// broadcast catching up with a client that didn't need it.
			return m, nil, true
		}
		if messageIndexByLocalID(m.accounts[msg.AccountIdx].Messages[chatIdx], msg.Message.LocalID) >= 0 {
			// Same case as above, but for a queued send flushOutbox just
			// replayed: the placeholder still has no ID at this point (that's
			// filled in by the MessageSendResolvedMsg broadcast that follows),
			// so it can only be matched by LocalID here - matching by ID would
			// miss it and append a duplicate row alongside the placeholder.
			return m, nil, true
		}
		if m.accounts[msg.AccountIdx].HistoryNewer[chatIdx] {
			// Currently viewing a mid-history window (paged up, or a
			// search-result jump) — Messages[chatIdx] isn't the live tail, so
			// splicing a just-arrived message onto its end would show it next
			// to unrelated older messages instead of where it belongs. Just
			// count it unread; it'll show up normally next time the window
			// loads the true tail (paging back down, or jump-to-latest).
			var cmd tea.Cmd
			if !msg.Message.IsMe && !msg.Message.DecryptFailed {
				cmd = m.incrementChatUnread(msg.AccountIdx, chatIdx, 1)
			}
			return m, cmd, true
		}
		if m.accounts[msg.AccountIdx].Messages == nil {
			m.accounts[msg.AccountIdx].Messages = make(map[int][]Message)
		}
		newMsg := msg.Message
		if replyIdx := messageIndexByID(m.accounts[msg.AccountIdx].Messages[chatIdx], msg.ReplyToID); replyIdx >= 0 {
			newMsg.ReplyTo = &replyIdx
		}
		msgs := m.appendAndTrim(msg.AccountIdx, chatIdx, newMsg)
		cmd := m.setChatLastMessage(msg.AccountIdx, chatIdx, MessagePreviewContent(newMsg))
		if m.isChatFocused(msg.AccountIdx, chatIdx) {
			m.selectedMsg = len(msgs) - 1
			m.refreshViewport()
			m.viewport.GotoBottom()
		} else if !newMsg.IsMe && !newMsg.DecryptFailed {
			cmd = tea.Batch(cmd, m.incrementChatUnread(msg.AccountIdx, chatIdx, 1))
		}
		return m, cmd, true

	case HistoryWindowMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil, true
		}
		delete(m.loadingHistoryWindow, chatIdx)
		anchorID := m.pendingWindowAnchor[chatIdx]
		delete(m.pendingWindowAnchor, chatIdx)
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
		// A HistoryWindowMsg is always a fresh, complete window — it fully
		// replaces whatever was loaded before, never merges with it. Nothing
		// needs to be preserved from the old window: storage still has
		// everything it ever had, so any point in this chat's history is
		// just another anchor away.
		m.accounts[msg.AccountIdx].Messages[chatIdx] = msg.Messages
		if msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
			// Keep the selection on the same message it was on before the
			// reload — found by ID rather than carried across as an index,
			// since a full replace makes any old index meaningless. Falls
			// back to the last message if the anchor somehow isn't in the
			// new window (shouldn't normally happen: the anchor is always
			// included in what was requested).
			if idx := messageIndexByID(msg.Messages, anchorID); idx >= 0 {
				m.selectedMsg = idx
			} else {
				m.selectedMsg = len(msg.Messages) - 1
			}
			m.refreshViewportFullScrollTo(m.selectedMsg)
		}
		return m, nil, true

	case HistorySearchResultMsg:
		sr := m.searchResults
		if sr == nil || sr.accountIdx != msg.AccountIdx || sr.chatAddress != msg.From || sr.query != msg.Query {
			// A later search (or the popup being closed) superseded this
			// result — discard it rather than overwrite unrelated state.
			return m, nil, true
		}
		sr.busy = false
		if msg.Err != nil {
			sr.err = msg.Err.Error()
			return m, nil, true
		}
		sr.messages = msg.Messages
		sr.matches = msg.Matches
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
		var cmd tea.Cmd
		if idx == len(msgs)-1 {
			cmd = m.setChatLastMessage(msg.AccountIdx, chatIdx, MessagePreviewContent(msgs[idx]))
		}
		if msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
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
		var cmd tea.Cmd
		if idx == len(msgs)-1 {
			cmd = m.setChatLastMessage(msg.AccountIdx, chatIdx, "message deleted")
		}
		if msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
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
		lastMsgCmd := m.setChatLastMessage(msg.AccountIdx, chatIdx, MessagePreviewContent(msgs[idx]))
		if msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
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
		var cmd tea.Cmd
		if idx == len(msgs)-1 {
			preview := MessagePreviewContent(msgs[idx])
			if len(msg.Reactions) > 0 {
				preview = "reacted " + renderReactions(msg.Reactions)
			}
			cmd = m.setChatLastMessage(msg.AccountIdx, chatIdx, preview)
		}
		if msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
			m.refreshViewport()
		}
		return m, cmd, true

	case AccountAddedMsg:
		m.addingAccount = false
		m.addAccountBusy = false
		m.accounts = append(m.accounts, msg.Account)
		return m, m.showNotification("account added: " + msg.Account.Name), true

	case AccountAddErrorMsg:
		m.addAccountBusy = false
		m.addAccountErr = msg.Err.Error()
		return m, nil, true

	case OmemoDeviceListMsg:
		if m.deviceList == nil || m.deviceList.accountIdx != msg.AccountIdx {
			return m, nil, true
		}
		m.deviceList.busy = false
		if msg.Err != nil {
			m.deviceList.err = msg.Err.Error()
			return m, nil, true
		}
		m.deviceList.local = msg.Local
		m.deviceList.devices = msg.Devices
		return m, nil, true

	case OmemoDevicePurgedMsg:
		if m.deviceList == nil || m.deviceList.accountIdx != msg.AccountIdx {
			return m, nil, true
		}
		m.deviceList.busy = false
		if msg.Err != nil {
			m.deviceList.err = msg.Err.Error()
			return m, nil, true
		}
		m.deviceList.local = msg.Local
		m.deviceList.devices = msg.Devices
		m.deviceList.selected = map[OmemoDevice]bool{}
		return m, m.showNotification("omemo device list updated"), true

	case ContactAddedMsg:
		cs := m.contactManagerState
		if cs == nil || cs.accountIdx != msg.AccountIdx {
			return m, nil, true
		}
		cs.busy = false
		if msg.Err != nil {
			cs.err = msg.Err.Error()
			return m, nil, true
		}
		cs.adding = false
		if m.chatIndexByAddress(msg.AccountIdx, msg.Address) < 0 {
			chat := Chat{Name: msg.Address, Address: msg.Address}
			m.accounts[msg.AccountIdx].Chats = append(m.accounts[msg.AccountIdx].Chats, chat)
			var cmd tea.Cmd
			if msg.AccountIdx == m.currentAccount {
				cmd = m.chats.SetItems(m.accounts[msg.AccountIdx].Chats)
			}
			return m, tea.Batch(cmd, m.showNotification("contact added: "+msg.Address)), true
		}
		return m, m.showNotification("contact added: " + msg.Address), true

	case ContactRemovedMsg:
		cs := m.contactManagerState
		if cs == nil || cs.accountIdx != msg.AccountIdx {
			return m, nil, true
		}
		cs.busy = false
		if msg.Err != nil {
			cs.err = msg.Err.Error()
			return m, nil, true
		}
		if idx := m.chatIndexByAddress(msg.AccountIdx, msg.Address); idx >= 0 {
			items := m.accounts[msg.AccountIdx].Chats
			newItems := make([]list.Item, 0, len(items)-1)
			newItems = append(newItems, items[:idx]...)
			newItems = append(newItems, items[idx+1:]...)
			m.accounts[msg.AccountIdx].Chats = newItems
			var cmd tea.Cmd
			if msg.AccountIdx == m.currentAccount {
				cmd = m.chats.SetItems(newItems)
			}
			return m, tea.Batch(cmd, m.showNotification("contact removed: "+msg.Address)), true
		}
		return m, m.showNotification("contact removed: " + msg.Address), true

	case ContactResubscribedMsg:
		cs := m.contactManagerState
		if cs == nil || cs.accountIdx != msg.AccountIdx {
			return m, nil, true
		}
		cs.busy = false
		if msg.Err != nil {
			cs.err = msg.Err.Error()
			return m, nil, true
		}
		return m, m.showNotification("resubscribe sent: " + msg.Address), true

	case AccountConnectedMsg:
		if msg.Index < 0 || msg.Index >= len(m.accounts) {
			return m, nil, true
		}
		m.accounts[msg.Index] = msg.Account
		var cmd tea.Cmd
		if msg.Index == m.currentAccount {
			cmd = m.chats.SetItems(msg.Account.Chats)
			m.refreshViewport()
			cmd = tea.Batch(cmd, m.openPendingChat())
		}
		return m, cmd, true

	case AccountLiveMsg:
		if msg.Index < 0 || msg.Index >= len(m.accounts) {
			return m, nil, true
		}
		m.accounts[msg.Index].Connecting = false
		m.accounts[msg.Index].ConnectError = ""
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
		}
		var cmd tea.Cmd
		if msg.Index == m.currentAccount {
			if len(msg.NewChats) > 0 {
				cmd = m.chats.SetItems(m.accounts[msg.Index].Chats)
			}
			m.refreshViewport()
			cmd = tea.Batch(cmd, m.openPendingChat())
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
		m.accounts[msg.Index].Status = msg.Status
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
		}
		var cmd tea.Cmd
		if msg.Index == m.currentAccount && len(msg.NewChats) > 0 {
			cmd = m.chats.SetItems(m.accounts[msg.Index].Chats)
		}
		return m, tea.Batch(cmd, m.showNotification("status: "+presenceLabel(msg.Status))), true

	case AccountRemovedMsg:
		if msg.Index < 0 || msg.Index >= len(m.accounts) {
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
		msgs := m.appendAndTrim(msg.AccountIdx, chatIdx, msg.Messages...)
		lastMsgCmd := m.setChatLastMessage(msg.AccountIdx, chatIdx, MessagePreviewContent(msg.Messages[len(msg.Messages)-1]))
		if m.isChatFocused(msg.AccountIdx, chatIdx) {
			m.selectedMsg = len(msgs) - 1
			m.refreshViewport()
			m.viewport.GotoBottom()
		} else {
			// MAM catch-up delivers messages that arrived while offline (see
			// syncArchive), not just historical replay of what was already
			// seen elsewhere — those genuinely-new, non-"me" messages count
			// toward unread the same as a live IncomingMessageMsg would.
			unread := 0
			for _, nm := range msg.Messages {
				if !nm.IsMe && !nm.DecryptFailed {
					unread++
				}
			}
			lastMsgCmd = tea.Batch(lastMsgCmd, m.incrementChatUnread(msg.AccountIdx, chatIdx, unread))
		}
		return m, lastMsgCmd, true

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
		if m.sender != nil && m.typingActiveTo == msg.addr && m.typingGen == msg.gen {
			if err := m.sender.SetTyping(m.currentAccount, msg.addr, false); err == nil {
				m.typingActiveTo = ""
			}
		}
		return m, nil, true

	case StoragePasswordChangedMsg:
		if m.changePasswordState == nil {
			return m, nil, true
		}
		m.changePasswordState.busy = false
		if msg.Err != nil {
			m.changePasswordState.err = msg.Err.Error()
			return m, nil, true
		}
		m.changePasswordState = nil
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
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil, true
		}
		chat, ok := m.accounts[msg.AccountIdx].Chats[chatIdx].(Chat)
		if !ok {
			return m, nil, true
		}
		chat.Typing = msg.Typing
		m.accounts[msg.AccountIdx].Chats[chatIdx] = chat
		if msg.AccountIdx == m.currentAccount {
			cmd := m.chats.SetItem(chatIdx, chat)
			return m, cmd, true
		}
		return m, nil, true

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
		}
		return m, nil, true

	case CallActionResultMsg:
		if msg.Err != nil {
			return m, m.showNotification("call " + msg.Action + " failed: " + msg.Err.Error()), true
		}
		return m, nil, true

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

	case PresenceMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil, true
		}
		chat, ok := m.accounts[msg.AccountIdx].Chats[chatIdx].(Chat)
		if !ok {
			return m, nil, true
		}
		chat = chat.withResource(msg.Resource, msg.Presence)
		if msg.Resource == "" {
			// No resource part to track individually - this stanza is the
			// whole story, same as before resource-aware aggregation existed.
			chat.Presence = msg.Presence
		} else {
			chat.Presence = aggregatePresence(chat.Resources)
		}
		m.accounts[msg.AccountIdx].Chats[chatIdx] = chat
		if msg.AccountIdx == m.currentAccount {
			cmd := m.chats.SetItem(chatIdx, chat)
			return m, cmd, true
		}
		return m, nil, true
	}

	return m, nil, false
}
