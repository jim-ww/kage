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

// trimTailAndStashFront trims msgs the same way trimMessagesFront does, but
// additionally retains whatever got dropped in accounts[accountIdx].TrimmedFront[chatIdx]
// (oldest-first, appended after anything already stashed there since the
// last older-history fetch) so a later OlderHistoryMsg prepend can fold it
// back in instead of silently skipping it. Use this instead of calling
// trimMessagesFront directly for any tail-growth path (live incoming
// messages, sent messages, MAM catch-up).
func (m *Model) trimTailAndStashFront(accountIdx, chatIdx int, msgs []Message) []Message {
	trimmed, drop := trimMessagesFront(msgs, m.maxMessagesPerChat)
	if drop == 0 {
		return trimmed
	}
	dropped := msgs[:drop]
	if m.accounts[accountIdx].TrimmedFront == nil {
		m.accounts[accountIdx].TrimmedFront = make(map[int][]Message)
	}
	m.accounts[accountIdx].TrimmedFront[chatIdx] = append(m.accounts[accountIdx].TrimmedFront[chatIdx], dropped...)
	return trimmed
}

// trimMessagesBack drops the newest messages from msgs so at most limit
// remain, used when an older-history page is prepended (see OlderHistoryMsg
// handling) and the chat has grown past the configured cap — the mirror
// image of trimMessagesFront: scrolling up to see older content means the
// oldest end (what was just fetched) is what the caller wants to keep, and
// the newest end (now furthest from where the user's looking) is what's
// safe to give up. Returns the trimmed slice and how many messages were
// dropped so the caller can adjust any index (selectedMsg) that pointed
// into the old slice. limit <= 0 disables trimming.
func trimMessagesBack(msgs []Message, limit int) ([]Message, int) {
	if limit <= 0 || len(msgs) <= limit {
		return msgs, 0
	}
	drop := len(msgs) - limit
	trimmed := make([]Message, limit)
	copy(trimmed, msgs[:limit])
	for i := range trimmed {
		if trimmed[i].ReplyTo == nil {
			continue
		}
		if *trimmed[i].ReplyTo >= limit {
			trimmed[i].ReplyTo = nil
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
		msgs = m.trimTailAndStashFront(msg.AccountIdx, chatIdx, msgs)
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
		msgs = m.trimTailAndStashFront(msg.AccountIdx, chatIdx, msgs)
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
		if _, pinned := m.accounts[msg.AccountIdx].PinnedHistory[chatIdx]; pinned {
			// Currently browsing an old part of this chat via a search-result
			// jump (see loadSearchResult) — the loaded window isn't anchored
			// to the live tail, so splicing a just-arrived message onto its
			// end would show it next to unrelated old messages instead of
			// where it belongs. Just count it unread rather than force the
			// pinned browse closed out from under the user; it'll show up
			// normally once growPinnedWindow reaches the true tail again.
			var cmd tea.Cmd
			if !msg.Message.IsMe && !msg.Message.DecryptFailed {
				cmd = m.incrementChatUnread(msg.AccountIdx, chatIdx, 1)
			}
			return m, cmd, true
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
		msgs = m.trimTailAndStashFront(msg.AccountIdx, chatIdx, msgs)
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
		// If an earlier page already trimmed this chat's newest end into
		// PinnedHistory (see below), that full retained history — not just
		// the currently windowed slice — is the real baseline to prepend
		// onto; otherwise a second older-history fetch would merge against
		// only the visible window and silently forget everything already
		// trimmed off.
		existing, pinned := m.accounts[msg.AccountIdx].PinnedHistory[chatIdx]
		if !pinned {
			existing = m.accounts[msg.AccountIdx].Messages[chatIdx]
		}
		// Anything trimmed off the live tail's front since the last fetch
		// (see TrimmedFront's doc comment) sits chronologically between the
		// freshly fetched older page and existing — fold it back in here so
		// it isn't silently skipped.
		trimmedFront := m.accounts[msg.AccountIdx].TrimmedFront[chatIdx]
		delete(m.accounts[msg.AccountIdx].TrimmedFront, chatIdx)
		prefixLen := len(msg.Messages) + len(trimmedFront)
		// Prepending shifts every already-loaded message's position by
		// prefixLen; their ReplyTo indices (set when that page was built)
		// point within the slice as it existed before this shift, so they
		// need to move with it. trimmedFront's own ReplyTo indices only need
		// to move past msg.Messages, since they were already self-consistent
		// relative to each other when trimMessagesFront dropped them.
		for i := range trimmedFront {
			if trimmedFront[i].ReplyTo != nil {
				shifted := *trimmedFront[i].ReplyTo + len(msg.Messages)
				trimmedFront[i].ReplyTo = &shifted
			}
		}
		for i := range existing {
			if existing[i].ReplyTo != nil {
				shifted := *existing[i].ReplyTo + prefixLen
				existing[i].ReplyTo = &shifted
			}
		}
		combined := append(append(append([]Message{}, msg.Messages...), trimmedFront...), existing...)
		// Trimmed off the *newest* end (trimMessagesBack), not the oldest —
		// the whole point of scrolling up was to see this older content, so
		// keeping it and dropping what's now furthest from view is the
		// right end to give up. Unlike a plain trim, the dropped tail isn't
		// lost: it's kept as PinnedHistory/PinnedWindow (the same mechanism
		// a search jump uses), so scrolling back down still works —
		// maybeLoadNewerHistory/growPinnedWindow slides the window across
		// it instead of needing a "load newer" fetch that doesn't exist.
		windowed, drop := trimMessagesBack(combined, m.maxMessagesPerChat)
		if drop > 0 {
			if m.accounts[msg.AccountIdx].PinnedHistory == nil {
				m.accounts[msg.AccountIdx].PinnedHistory = make(map[int][]Message)
			}
			if m.accounts[msg.AccountIdx].PinnedWindow == nil {
				m.accounts[msg.AccountIdx].PinnedWindow = make(map[int][2]int)
			}
			m.accounts[msg.AccountIdx].PinnedHistory[chatIdx] = combined
			m.accounts[msg.AccountIdx].PinnedWindow[chatIdx] = [2]int{0, len(windowed)}
			// combined is only what's been paged in from storage so far, not
			// the whole chat — see PinnedHistoryComplete's doc comment.
			delete(m.accounts[msg.AccountIdx].PinnedHistoryComplete, chatIdx)
		} else if pinned {
			// Shrank back to fit in one window (e.g. maxMessagesPerChat was
			// raised) — nothing left to page across.
			delete(m.accounts[msg.AccountIdx].PinnedHistory, chatIdx)
			delete(m.accounts[msg.AccountIdx].PinnedWindow, chatIdx)
			delete(m.accounts[msg.AccountIdx].PinnedHistoryComplete, chatIdx)
		}
		m.accounts[msg.AccountIdx].Messages[chatIdx] = windowed
		if msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
			// Prepended messages shift every existing index up by
			// prefixLen — keep the selection on the same message rather than
			// letting it silently jump, clamped in case it (or the message
			// it pointed at) was itself trimmed off the end.
			m.selectedMsg = min(m.selectedMsg+prefixLen, len(windowed)-1)
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
		}
		if msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex() {
			m.refreshViewport()
		}
		return m, cmd, true

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
		msgs := append(m.accounts[msg.AccountIdx].Messages[chatIdx], msg.Messages...)
		msgs = m.trimTailAndStashFront(msg.AccountIdx, chatIdx, msgs)
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
		chat.Presence = msg.Presence
		chat = chat.withResource(msg.Resource, msg.Presence)
		m.accounts[msg.AccountIdx].Chats[chatIdx] = chat
		if msg.AccountIdx == m.currentAccount {
			cmd := m.chats.SetItem(chatIdx, chat)
			return m, cmd, true
		}
		return m, nil, true
	}

	return m, nil, false
}
