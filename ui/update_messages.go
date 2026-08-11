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

// trimMessagesAround keeps at most limit messages from msgs, centered on
// target — unlike trimMessagesFront (which always keeps the newest tail),
// used when target itself is what matters (a search-result jump into
// possibly very old history) rather than "whatever's most recent". Without
// this, jumping to a match loads the chat's entire persisted history into
// the in-memory window regardless of how far back it is, which is what
// makes the message list sluggish (every render/scroll walks/wraps however
// many thousand messages that chat has, not the same bounded window every
// other load path already respects via trimMessagesFront). Returns the
// trimmed slice, target's new index within it, and how many messages were
// dropped off the front (so the caller knows older history still exists
// beyond the window). limit <= 0 or len(msgs) <= limit disables trimming.
func trimMessagesAround(msgs []Message, target, limit int) (trimmed []Message, newTarget, front int) {
	if limit <= 0 || len(msgs) <= limit {
		return msgs, target, 0
	}
	start := target - limit/2
	start = max(0, min(start, len(msgs)-limit))
	end := start + limit

	trimmed = make([]Message, limit)
	copy(trimmed, msgs[start:end])
	for i := range trimmed {
		if trimmed[i].ReplyTo == nil {
			continue
		}
		rt := *trimmed[i].ReplyTo
		if rt < start || rt >= end {
			trimmed[i].ReplyTo = nil
		} else {
			shifted := rt - start
			trimmed[i].ReplyTo = &shifted
		}
	}
	return trimmed, target - start, start
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
				return m, m.showNotification("offline — attachment(s) queued, will send on reconnect"), true
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
		msgs := m.accounts[msg.AccountIdx].Messages[chatIdx]
		var lastContent string
		for _, sent := range msg.Messages {
			newMsg := Message{
				ID:          sent.ID,
				Author:      "me",
				Content:     sent.Content,
				SentAt:      time.Now(),
				IsMe:        true,
				Attachments: sent.Attachments,
			}
			if replyIdx := messageIndexByID(msgs, msg.ReplyToID); replyIdx >= 0 {
				newMsg.ReplyTo = &replyIdx
			}
			msgs = append(msgs, newMsg)
			lastContent = MessagePreviewContent(newMsg)
		}
		msgs, _ = trimMessagesFront(msgs, m.maxMessagesPerChat)
		m.accounts[msg.AccountIdx].Messages[chatIdx] = msgs
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
		msgs := m.accounts[msg.AccountIdx].Messages[chatIdx]
		newMsg := Message{
			ID:          msg.ID,
			Author:      "me",
			Content:     msg.URL,
			SentAt:      time.Now(),
			IsMe:        true,
			Attachments: []string{msg.URL},
		}
		if replyIdx := messageIndexByID(msgs, msg.ReplyToID); replyIdx >= 0 {
			newMsg.ReplyTo = &replyIdx
		}
		msgs = append(msgs, newMsg)
		msgs, _ = trimMessagesFront(msgs, m.maxMessagesPerChat)
		m.accounts[msg.AccountIdx].Messages[chatIdx] = msgs
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
		if m.accounts[msg.AccountIdx].Messages == nil {
			m.accounts[msg.AccountIdx].Messages = make(map[int][]Message)
		}
		msgs := m.accounts[msg.AccountIdx].Messages[chatIdx]
		newMsg := msg.Message
		if replyIdx := messageIndexByID(msgs, msg.ReplyToID); replyIdx >= 0 {
			newMsg.ReplyTo = &replyIdx
		}
		msgs = append(msgs, newMsg)
		msgs, _ = trimMessagesFront(msgs, m.maxMessagesPerChat)
		m.accounts[msg.AccountIdx].Messages[chatIdx] = msgs
		cmd := m.setChatLastMessage(msg.AccountIdx, chatIdx, MessagePreviewContent(newMsg))
		if m.isChatFocused(msg.AccountIdx, chatIdx) {
			m.selectedMsg = len(msgs) - 1
			m.refreshViewport()
			m.viewport.GotoBottom()
		} else if !newMsg.IsMe && !newMsg.DecryptFailed {
			cmd = tea.Batch(cmd, m.incrementChatUnread(msg.AccountIdx, chatIdx, 1))
		}
		return m, cmd, true

	case OlderHistoryMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil, true
		}
		delete(m.loadingOlderHistory, chatIdx)
		if m.accounts[msg.AccountIdx].HistoryMore == nil {
			m.accounts[msg.AccountIdx].HistoryMore = make(map[int]bool)
		}
		m.accounts[msg.AccountIdx].HistoryMore[chatIdx] = msg.HasMore
		if len(msg.Messages) == 0 {
			return m, nil, true
		}
		if m.accounts[msg.AccountIdx].Messages == nil {
			m.accounts[msg.AccountIdx].Messages = make(map[int][]Message)
		}
		existing := m.accounts[msg.AccountIdx].Messages[chatIdx]
		// Prepending shifts every already-loaded message's position by
		// len(msg.Messages); their ReplyTo indices (set when that page was
		// built) point within the slice as it existed before this shift, so
		// they need to move with it.
		for i := range existing {
			if existing[i].ReplyTo != nil {
				shifted := *existing[i].ReplyTo + len(msg.Messages)
				existing[i].ReplyTo = &shifted
			}
		}
		// Unlike live/MAM-tail growth (trimMessagesFront), an older-history
		// page prepended here is never trimmed off the newest end - doing so
		// used to silently drop already-viewed messages with no way to
		// re-fetch them (no "load newer" path exists), leaving a permanent
		// gap the moment the user scrolled back down. The cap only bounds
		// unbounded growth from incoming traffic; deliberately scrolling up
		// through history is self-limiting (one page per user action).
		combined := append(msg.Messages, existing...)
		m.accounts[msg.AccountIdx].Messages[chatIdx] = combined
		if msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
			// Prepended messages shift every existing index up by
			// len(msg.Messages) — keep the selection on the same message
			// rather than letting it silently jump.
			m.selectedMsg += len(msg.Messages)
			if m.selectedMsg >= len(combined) {
				m.selectedMsg = len(combined) - 1
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
			cmd = m.setChatLastMessage(msg.AccountIdx, chatIdx, msg.NewContent)
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
		msgs := append(m.accounts[msg.AccountIdx].Messages[chatIdx], msg.Messages...)
		msgs, _ = trimMessagesFront(msgs, m.maxMessagesPerChat)
		m.accounts[msg.AccountIdx].Messages[chatIdx] = msgs
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

	case PresenceMsg:
		chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
		if chatIdx < 0 {
			return m, nil, true
		}
		chat, ok := m.accounts[msg.AccountIdx].Chats[chatIdx].(Chat)
		if !ok {
			return m, nil, true
		}
		chat.Presence = msg.Presence
		m.accounts[msg.AccountIdx].Chats[chatIdx] = chat
		if msg.AccountIdx == m.currentAccount {
			cmd := m.chats.SetItem(chatIdx, chat)
			return m, cmd, true
		}
		return m, nil, true
	}

	return m, nil, false
}
