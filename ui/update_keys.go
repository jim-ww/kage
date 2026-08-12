package ui

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// updateKeyMsg handles every tea.KeyMsg. handled is false only when the key
// didn't match anything and should fall through to Update's focused-component
// routing (the compose input, chat list, etc).
func (m Model) updateKeyMsg(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	var cmds []tea.Cmd

	// ── Chat list filter input intercepts all input until dismissed ────
	// Otherwise keys typed into the filter (e.g. "l") get matched against
	// global bindings like ChatOpen first and never reach the list.
	if m.selectedView == viewChats && m.chats.FilterState() == list.Filtering {
		var cmd tea.Cmd
		*m.chats, cmd = m.chats.Update(msg)
		return m, cmd, true
	}

	// ── Context menu intercepts all input until dismissed ───────────────
	// It's mouse-only otherwise (every action it lists already has its
	// own keybinding), so the keyboard's only job here is closing it.
	if m.contextMenu != nil {
		switch {
		case matchesKey(msg, m.keys.Back), matchesKey(msg, m.keys.ConfirmNo):
			m.closeContextMenu()
		}
		return m, nil, true
	}

	// ── Delete confirmation popup intercepts all input ─────────────────
	if m.confirmTarget != confirmNone {
		switch {
		case matchesKey(msg, m.keys.ConfirmYes):
			switch m.confirmTarget {
			case confirmQuit:
				return m, tea.Quit, true
			case confirmDeleteMessage:
				// Messages are never actually deleted — only flagged as
				// retracted. Own messages also get a XEP-0424 retraction
				// sent over the network; someone else's message can only
				// be flagged locally, since we can't retract their stanza.
				if msgs := m.currentMessages(); m.selectedMsg >= 0 && m.selectedMsg < len(msgs) {
					target := msgs[m.selectedMsg]
					if target.ID != "" {
						if chat, ok := m.currentChat(); ok && chat.Address != "" && m.sender != nil {
							if target.IsMe {
								if _, err := m.sender.Send(m.currentAccount, chat.Address, "", SendOptions{RetractID: target.ID}); err != nil {
									cmds = append(cmds, m.showNotification("retract not delivered: "+err.Error()))
								}
							} else if err := m.sender.MarkRetracted(m.currentAccount, chat.Address, target.ID); err != nil {
								cmds = append(cmds, m.showNotification("delete not saved: "+err.Error()))
							}
						}
					}
				}
				cmds = append(cmds, m.retractSelectedMsg())
			case confirmDeleteChat:
				cmds = append(cmds, m.deleteSelectedChat())
			case confirmRemoveAccount:
				cmds = append(cmds, m.removeCurrentAccount())
			}
			m.confirmTarget = confirmNone
			m.refreshViewport()
			return m, tea.Batch(cmds...), true
		case matchesKey(msg, m.keys.ConfirmNo):
			m.confirmTarget = confirmNone
		}
		return m, nil, true
	}

	// ── Message-info popup intercepts all input until dismissed ────────
	if m.showMsgInfo {
		switch {
		case matchesKey(msg, m.keys.InfoMsg), matchesKey(msg, m.keys.Back), matchesKey(msg, m.keys.ConfirmNo):
			m.showMsgInfo = false
		}
		return m, nil, true
	}

	// ── Help popup intercepts all input until dismissed ─────────────────
	if m.showHelp {
		switch {
		case matchesKey(msg, m.keys.Help), matchesKey(msg, m.keys.Back), matchesKey(msg, m.keys.ConfirmNo):
			m.showHelp = false
		}
		return m, nil, true
	}

	// ── OMEMO device-list popup intercepts all input until dismissed ───
	if m.deviceList != nil {
		return m.updateDeviceListKey(msg)
	}

	// ── Contact-manager popup intercepts all input until dismissed ─────
	if m.contactManagerState != nil {
		return m.updateContactManagerKey(msg)
	}

	// ── Change-storage-password popup intercepts all input until submitted/canceled ──
	if m.changePasswordState != nil {
		model, cmd := m.updateChangePasswordForm(msg)
		return model.(Model), cmd, true
	}

	// ── Add-account form intercepts all input until submitted/canceled ──
	if m.addingAccount {
		model, cmd := m.updateAddAccountForm(msg)
		return model.(Model), cmd, true
	}

	// ── Rename-chat prompt intercepts all input until submitted/canceled ──
	if m.renamingChat {
		model, cmd := m.updateRenameChatForm(msg)
		return model.(Model), cmd, true
	}

	// ── Search-in-chat prompt intercepts all input until submitted/canceled ──
	if m.searchingChat {
		model, cmd := m.updateSearchChatForm(msg)
		return model.(Model), cmd, true
	}

	// ── Search-results popup intercepts all input until closed ──────────
	if m.searchResults != nil {
		return m.updateSearchResultsKey(msg)
	}

	// ── Save-as prompt intercepts all input until submitted/canceled ──
	if m.savingAs {
		model, cmd := m.updateSaveAsForm(msg)
		return model.(Model), cmd, true
	}

	// ── File picker intercepts all input until selected or canceled ─────
	if m.pickingFile {
		if matchesKey(msg, m.keys.AttachFile) || matchesKey(msg, m.keys.Back) || matchesKey(msg, m.keys.ConfirmNo) {
			m.pickingFile = false
			return m, nil, true
		}
		if matchesKey(msg, m.keys.SortFilePicker) {
			var sortCmd tea.Cmd
			*m.filePicker, sortCmd = m.filePicker.CycleSort()
			if m.filePickerSortSetter != nil {
				if err := m.filePickerSortSetter.SetFilePickerSort(m.filePicker.SortField.String(), m.filePicker.SortAscending); err != nil {
					return m, tea.Batch(sortCmd, m.showNotification("saving file picker sort: "+err.Error())), true
				}
			}
			return m, sortCmd, true
		}
		var pickerCmd tea.Cmd
		*m.filePicker, pickerCmd = m.filePicker.Update(msg)
		if selected, path := m.filePicker.DidSelectFile(msg); selected {
			// Stage the file instead of uploading it immediately — nothing
			// touches the network until the message is actually sent (see
			// sendCurrentInput/startAttachedSend), and the picker stays open
			// so it can be used again to attach more files first.
			hadAttachments := len(m.pendingAttachments) > 0
			m.stageAttachment(path)
			if !hadAttachments {
				// The staged-attachments row above the compose box just
				// appeared for the first time, adding a row to
				// inputAreaHeight() — the picker's own height was sized
				// without it (see the AttachFile case below), so without
				// this it now overflows the terminal by one row, pushing
				// that new row (the only feedback that anything was
				// attached) off screen until the picker closes.
				m.filePicker.SetHeight(max(1, m.height-m.inputAreaHeight()-6))
			}
			return m, tea.Batch(cmds...), true
		}
		if disabled, _ := m.filePicker.DidSelectDisabledFile(msg); disabled {
			cmds = append(cmds, m.showNotification("that file type cannot be selected"))
		}
		return m, tea.Batch(append(cmds, pickerCmd)...), true
	}

	// ── Open-item picker intercepts all input until a choice is made ───
	if len(m.openItems) > 0 {
		start, end := openPageBounds(len(m.openItems), m.openPage)
		if i, ok := digitKey(msg); ok && i >= 1 && i <= end-start {
			idx := start + i - 1
			target := m.openItems[idx]
			isAttachment := idx < m.openItemsAttachCount
			m.openItems = nil
			m.openItemsAttachCount = 0
			m.openPage = 0
			switch m.openMode {
			case pickerModeSave:
				return m, m.startSave(target), true
			case pickerModeSaveAs:
				return m, m.openSaveAsPrompt(target), true
			}
			return m, m.startOpen(target, isAttachment), true
		}
		switch msg.String() {
		case "left", "h":
			m.openPage = max(0, m.openPage-1)
		case "right", "l":
			if m.openPage < openPageCount(len(m.openItems))-1 {
				m.openPage++
			}
		default:
			switch {
			case matchesKey(msg, m.keys.Back), matchesKey(msg, m.keys.ConfirmNo):
				m.openItems = nil
				m.openPage = 0
			}
		}
		return m, nil, true
	}

	// ── Ringing-local call banner intercepts y/n (answer/reject) ───────
	// Checked ahead of the global switch since y/n aren't otherwise bound
	// outside a confirm popup (already handled above) — a call can ring in
	// on any view, not just while its matching chat happens to be open.
	if m.call != nil && m.call.state == "ringing-local" {
		switch {
		case matchesKey(msg, m.keys.ConfirmYes):
			return m, m.answerRingingCall(), true
		case matchesKey(msg, m.keys.ConfirmNo):
			return m, m.rejectRingingCall(), true
		}
	}

	// ── "camera or screen?" prompt (VideoToggle on a connected, not yet
	// sharing call) intercepts c/s/esc while open, swallowing everything
	// else so a stray keystroke can't fall through to e.g. toggleScreenShare
	// below with the prompt still (confusingly) up ─────────────────────
	if m.videoSourcePrompt {
		switch {
		case matchesLetter(msg, 'c'):
			var cmd tea.Cmd
			m, cmd = m.startVideo(true)
			return m, cmd, true
		case matchesLetter(msg, 's'):
			var cmd tea.Cmd
			m, cmd = m.startVideo(false)
			return m, cmd, true
		case matchesKey(msg, m.keys.Back), matchesKey(msg, m.keys.ConfirmNo):
			m.videoSourcePrompt = false
			return m, nil, true
		}
		return m, nil, true
	}

	// ── "camera or screen?" pre-dial prompt (VideoCallToggle with no call
	// in progress) intercepts c/s/esc while open, same shape as
	// videoSourcePrompt above but answered before a call exists at all ──
	if m.videoDialPrompt {
		switch {
		case matchesLetter(msg, 'c'):
			var cmd tea.Cmd
			m, cmd = m.startVideoCall(true)
			return m, cmd, true
		case matchesLetter(msg, 's'):
			var cmd tea.Cmd
			m, cmd = m.startVideoCall(false)
			return m, cmd, true
		case matchesKey(msg, m.keys.Back), matchesKey(msg, m.keys.ConfirmNo):
			m.videoDialPrompt = false
			return m, nil, true
		}
		return m, nil, true
	}

	// ── Call bar intercepts h (hang up) / m (mute) / v (video) / s (screen
	// share) while a call is in progress but not ringing-local (that's y/n
	// above) ────────────────────────────────────────────────────────────
	if m.call != nil && m.callInProgress() && m.call.state != "ringing-local" {
		switch {
		case matchesLetter(msg, 'h'):
			return m, m.hangupCurrentCall(), true
		case matchesLetter(msg, 'm') && m.call.state == "connected":
			return m, m.toggleMuteCall(), true
		case matchesLetter(msg, 'v') && m.call.state == "connected" && !m.call.sharing:
			return m.startVideoPrompt(), nil, true
		case matchesLetter(msg, 's') && m.call.state == "connected":
			return m, m.toggleScreenShare(), true
		case matchesLetter(msg, 'r') && m.call.state == "connected":
			return m, m.reopenRemoteVideo(), true
		}
	}

	switch {

	// ── Reaction-composition suggestion nav (must precede ChatOpen,
	// which also binds "right", and Switch, which binds "tab") ────────
	case m.reactingMsgIdx >= 0 && len(m.emojiSuggestions) > 0 && (msg.String() == "left" || msg.String() == "right"):
		n := len(m.emojiSuggestions)
		if msg.String() == "left" {
			m.emojiSuggestIdx = (m.emojiSuggestIdx - 1 + n) % n
		} else {
			m.emojiSuggestIdx = (m.emojiSuggestIdx + 1) % n
		}
		return m, nil, true

	case (msg.String() == "tab" || matchesKey(msg, m.keys.SelectSend)) && m.reactingMsgIdx >= 0 && len(m.emojiSuggestions) > 0:
		// While a suggestion is showing, enter accepts it (like tab)
		// instead of falling through to SelectSend and sending the
		// reaction early — matches the emoji picker, not a message send.
		m.acceptEmojiSuggestion(m.emojiSuggestIdx)
		return m, nil, true

	// ── Global ────────────────────────────────────────────────────────
	case matchesKey(msg, m.keys.Help):
		m.showHelp = true
		return m, nil, true

	case matchesKey(msg, m.keys.Quit):
		if msg.String() == "ctrl+c" {
			return m, tea.Quit, true
		}
		if m.selectedView != viewChat {
			m.confirmTarget = confirmQuit
			return m, nil, true
		}

	case matchesKey(msg, m.keys.Back):
		if m.selectedView != viewAccounts && m.selectedView != viewChats {
			m.notifyTypingStopped()
			m.cancelPending()
			m.setSelectedView(viewChats)
			m.input.Blur()
			return m, nil, true
		}

	case matchesKey(msg, m.keys.FocusChats):
		m.notifyTypingStopped()
		m.setSelectedView(viewChats)
		m.input.Blur()
		return m, nil, true

	case matchesKey(msg, m.keys.ToggleSidebar):
		model, cmd := m.toggleSidebar()
		return model, cmd, true

	case matchesKey(msg, m.keys.ChatOpen):
		if m.selectedView == viewChats {
			model, cmd := m.openCurrentChat()
			return model.(Model), cmd, true
		}

	case matchesKey(msg, m.keys.SelectSend):
		switch m.selectedView {
		case viewAccounts:
			m.setSelectedView(viewChats)
			return m, nil, true
		case viewChats:
			model, cmd := m.openCurrentChat()
			return model.(Model), cmd, true
		case viewChat:
			return m, m.sendCurrentInput(), true
		}

	case matchesKey(msg, m.keys.AddAccount):
		if m.selectedView == viewAccounts {
			m.addingAccount = true
			m.addAccountRegister = false
			m.addAccountFocus = 0
			m.addAccountErr = ""
			m.addAccountInputs = m.newAddAccountForm()
			return m, textinput.Blink, true
		}

	case matchesKey(msg, m.keys.DeviceList):
		if m.selectedView == viewAccounts {
			model, cmd := m.openDeviceList()
			return model, cmd, true
		}

	case matchesKey(msg, m.keys.ContactManager):
		if m.selectedView == viewAccounts {
			model, cmd := m.openContactManager()
			return model, cmd, true
		}

	case matchesKey(msg, m.keys.CallToggle):
		if m.selectedView == viewChat || m.callInProgress() {
			return m, m.startCallToCurrentChat(), true
		}

	case matchesKey(msg, m.keys.VideoCallToggle):
		if m.selectedView == viewChat || m.callInProgress() {
			var cmd tea.Cmd
			m, cmd = m.startVideoCallToCurrentChat()
			return m, cmd, true
		}

	case matchesKey(msg, m.keys.ChangeStoragePassword):
		if m.selectedView == viewAccounts {
			cmd := m.openChangePasswordPopup()
			return m, cmd, true
		}

	case matchesKey(msg, m.keys.RenameChat):
		if m.selectedView == viewChats {
			return m, m.actionRenameChat(), true
		}

	case matchesKey(msg, m.keys.AttachFile):
		if m.selectedView == viewChat {
			if m.currentChatIndex() < 0 {
				cmds = append(cmds, m.showNotification("no chat selected"))
				return m, tea.Batch(cmds...), true
			}
			m.pickingFile = true
			m.filePicker.SetHeight(max(1, m.height-m.inputAreaHeight()-6))
			return m, m.filePicker.Init(), true
		}

	case matchesKey(msg, m.keys.PasteImage):
		if m.selectedView == viewChat {
			if m.currentChatIndex() < 0 {
				cmds = append(cmds, m.showNotification("no chat selected"))
				return m, tea.Batch(cmds...), true
			}
			return m, pasteClipboardImage, true
		}

	// Tab cycles which staged attachment is highlighted (shown "[in
	// brackets]" above the compose box when more than one is staged) —
	// checked ahead of Switch, which also binds tab but only acts on
	// viewAccounts/viewChats.
	case msg.String() == "tab" && m.selectedView == viewChat && len(m.pendingAttachments) > 0:
		m.cycleSelectedAttachment()
		return m, nil, true

	// Backspacing an empty compose box drops the highlighted attachment —
	// mirrors the chat apps this pattern is borrowed from, and gives
	// keyboard-only users a way to remove one without the mouse.
	case matchesKey(msg, m.keys.RemoveAttachment):
		if m.selectedView == viewChat && m.input.Value() == "" && len(m.pendingAttachments) > 0 {
			m.removeAttachment(m.selectedAttachment)
			m.updateSizes()
			return m, nil, true
		}

	case matchesKey(msg, m.keys.ToggleComposeExpand):
		if m.selectedView == viewChat {
			return m.toggleComposeExpand(), nil, true
		}

	case matchesKey(msg, m.keys.ClearDraft):
		if m.selectedView == viewChat && m.input.Value() != "" {
			m.input.SetValue("")
			m.pushDraftSnapshot("")
			m.notifyTypingStopped()
			var cmd tea.Cmd
			if chatIdx := m.currentChatIndex(); chatIdx >= 0 {
				cmd = m.saveChatDraft(m.currentAccount, chatIdx, "")
			}
			m.updateSizes()
			return m, cmd, true
		}

	case matchesKey(msg, m.keys.RedoDraft):
		if m.selectedView == viewChat && m.redoDraft() {
			m.updateSizes()
			return m, nil, true
		}

	case matchesKey(msg, m.keys.UndoDraft):
		if m.selectedView == viewChat && m.undoDraft() {
			m.updateSizes()
			return m, nil, true
		}

	case matchesKey(msg, m.keys.Switch):
		switch m.selectedView {
		case viewAccounts:
			m.setSelectedView(viewChats)
			return m, nil, true
		case viewChats:
			m.setSelectedView(viewAccounts)
			return m, nil, true
		}

	// ── Viewport paging (chat view) ───────────────────────────────────
	case isViewportPagingKey(msg):
		if m.selectedView == viewChat {
			oldOffset := m.viewport.YOffset()
			var viewportCmd tea.Cmd
			m.viewport, viewportCmd = m.viewport.Update(msg)
			cmds = append(cmds, viewportCmd)
			// Keep the message selection in sync with whatever paging
			// just scrolled into view, instead of leaving it pointing
			// at a message that's no longer on screen. Only do this if
			// paging actually moved the viewport — e.g. short chats that
			// fit on screen have nothing to page, and pgup/pgdown must
			// leave the current selection untouched in that case.
			if len(m.msgOffsets) > 0 && m.viewport.YOffset() != oldOffset {
				atTrueTop, atTrueBottom := m.scrollBoundaryStatus()
				m.recenterRenderWindowForScroll()
				if atTrueTop {
					cmds = append(cmds, m.maybeLoadOlderHistory())
				}
				if atTrueBottom {
					cmds = append(cmds, m.maybeLoadNewerHistory())
				}
			}
			return m, tea.Batch(cmds...), true
		}

	// ── Message navigation ─────────────────────────────────────────────
	// The plain up/down arrows are shared with the compose box: while it
	// holds more than one line, up/down move the cursor between those lines
	// instead of the selected message — falls through (handled=false) so
	// Update's normal routing reaches m.input.Update. ctrl+k/ctrl+j (MsgUp/
	// MsgDown's other bound keys) always navigate messages regardless, since
	// they're not keys the textarea itself binds to anything.
	case matchesKey(msg, m.keys.MsgUp):
		if msg.String() == "up" && m.composeMultiline() {
			break
		}
		if m.selectedView == viewAccounts && m.currentAccount > 0 {
			cmds = append(cmds, m.switchAccount(m.currentAccount-1))
			return m, tea.Batch(cmds...), true
		}
		if m.selectedView == viewChat && m.selectedMsg > 0 {
			old := m.selectedMsg
			m.selectedMsg--
			m.lastClickedMsgIdx = -1
			m.lastClickTime = time.Time{}
			m.refreshViewportScrollTo(old, m.selectedMsg)
			return m, nil, true
		}
		if m.selectedView == viewChat && m.selectedMsg == 0 {
			return m, m.maybeLoadOlderHistory(), true
		}

	case matchesKey(msg, m.keys.MsgDown):
		if msg.String() == "down" && m.composeMultiline() {
			break
		}
		if m.selectedView == viewAccounts && m.currentAccount < len(m.accounts)-1 {
			cmds = append(cmds, m.switchAccount(m.currentAccount+1))
			return m, tea.Batch(cmds...), true
		}
		if m.selectedView == viewChat {
			chatIdx := m.currentChatIndex()
			if chatIdx < 0 {
				return m, nil, true
			}
			if m.selectedMsg < len(m.currentMessages())-1 {
				old := m.selectedMsg
				m.selectedMsg++
				m.lastClickedMsgIdx = -1
				m.lastClickTime = time.Time{}
				m.refreshViewportScrollTo(old, m.selectedMsg)
				return m, nil, true
			}
			return m, m.maybeLoadNewerHistory(), true
		}

	// HalfPageUp/HalfPageDown jump by half the currently visible message
	// count (not raw viewport lines — see visibleMessageCount) so the leap
	// feels consistent regardless of how many lines each message wraps to.
	case matchesKey(msg, m.keys.HalfPageUp):
		if m.selectedView == viewChat && m.currentChatIndex() >= 0 {
			step := max(1, m.visibleMessageCount()/2)
			if m.selectedMsg > 0 {
				old := m.selectedMsg
				m.selectedMsg = max(0, m.selectedMsg-step)
				m.lastClickedMsgIdx = -1
				m.lastClickTime = time.Time{}
				m.refreshViewportScrollTo(old, m.selectedMsg)
				return m, nil, true
			}
			return m, m.maybeLoadOlderHistory(), true
		}

	case matchesKey(msg, m.keys.HalfPageDown):
		if m.selectedView == viewChat && m.currentChatIndex() >= 0 {
			step := max(1, m.visibleMessageCount()/2)
			if last := len(m.currentMessages()) - 1; m.selectedMsg < last {
				old := m.selectedMsg
				m.selectedMsg = min(last, m.selectedMsg+step)
				m.lastClickedMsgIdx = -1
				m.lastClickTime = time.Time{}
				m.refreshViewportScrollTo(old, m.selectedMsg)
				return m, nil, true
			}
			return m, m.maybeLoadNewerHistory(), true
		}

	// ── Message actions ────────────────────────────────────────────────
	case matchesKey(msg, m.keys.DeleteMsg):
		switch m.selectedView {
		case viewChat:
			return m, m.actionDeleteMessage(), true
		case viewChats:
			return m, m.actionLeaveChat(), true
		}

	case matchesKey(msg, m.keys.YankMsg):
		if m.selectedView == viewChat {
			return m, m.actionYankMessage(), true
		}

	case matchesKey(msg, m.keys.YankDraft):
		if m.selectedView == viewChat {
			return m, m.actionYankDraft(), true
		}

	case matchesKey(msg, m.keys.EditMsg):
		if m.selectedView == viewChat {
			return m, m.actionEditMessage(), true
		}

	case matchesKey(msg, m.keys.ReplyMsg):
		if m.selectedView == viewChat {
			return m, m.actionReplyMessage(), true
		}

	case matchesKey(msg, m.keys.InfoMsg):
		if m.selectedView == viewChat {
			return m, m.actionInfoMessage(), true
		}

	case matchesKey(msg, m.keys.OpenMsg):
		if m.selectedView == viewChat {
			// A staged-but-unsent attachment takes priority over whatever
			// message is selected in the history above — it's what's
			// actively being composed, and there's no other way to preview
			// it before sending (it isn't a Message yet).
			if m.selectedAttachment >= 0 && m.selectedAttachment < len(m.pendingAttachments) {
				return m, m.startOpen(m.pendingAttachments[m.selectedAttachment].path, true), true
			}
			return m, m.actionOpenMessage(), true
		}

	case matchesKey(msg, m.keys.SaveMsg):
		if m.selectedView == viewChat {
			return m, m.actionSaveMessage(), true
		}

	case matchesKey(msg, m.keys.SaveMsgAs):
		if m.selectedView == viewChat {
			return m, m.actionSaveMessageAs(), true
		}

	case matchesKey(msg, m.keys.ReactMsg):
		if m.selectedView == viewChat {
			return m, m.actionReactMessage(), true
		}

	case matchesKey(msg, m.keys.SearchChat):
		if m.selectedView == viewChat {
			return m, m.actionSearchChat(), true
		}
	}

	return m, nil, false
}

// updateRenameChatForm handles all key input while the rename-chat prompt is
// open: enter submits (see submitRenameChat), esc cancels. Only esc cancels —
// not the full Back/ConfirmNo bindings, since those also bind plain letters
// ("n") that must reach the focused text field while typing a name.
func (m Model) updateRenameChatForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.String() == "esc":
		m.renamingChat = false
		return m, nil

	case matchesKey(msg, m.keys.SelectSend):
		return m, m.submitRenameChat()

	default:
		var cmd tea.Cmd
		*m.renameInput, cmd = m.renameInput.Update(msg)
		return m, cmd
	}
}

// updateSearchChatForm handles all key input while the search-in-chat query
// prompt is open: enter submits it (see submitSearchChat), esc cancels.
func (m Model) updateSearchChatForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.String() == "esc":
		m.searchingChat = false
		return m, nil

	case matchesKey(msg, m.keys.SelectSend):
		return m, m.submitSearchChat()

	default:
		var cmd tea.Cmd
		*m.searchInput, cmd = m.searchInput.Update(msg)
		return m, cmd
	}
}

// updateSaveAsForm handles all key input while the save-as popup is open:
// enter submits the typed path and starts the download, esc cancels.
func (m Model) updateSaveAsForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.String() == "esc":
		m.savingAs = false
		m.saveAsTarget = ""
		return m, nil

	case matchesKey(msg, m.keys.SelectSend):
		return m, m.submitSaveAs()

	default:
		var cmd tea.Cmd
		*m.saveAsInput, cmd = m.saveAsInput.Update(msg)
		return m, cmd
	}
}

// addAccountFieldCount returns how many of addAccountInputs are active for
// the current mode — the confirm-password field (index 2) only applies to
// register mode.
func (m Model) addAccountFieldCount() int {
	if m.addAccountRegister {
		return len(m.addAccountInputs)
	}
	return len(m.addAccountInputs) - 1
}

// addAccountFieldIndex maps a logical position (0..addAccountFieldCount-1)
// to its slot in addAccountInputs, skipping the confirm-password field in
// login mode.
func (m Model) addAccountFieldIndex(pos int) int {
	if !m.addAccountRegister && pos >= 2 {
		return pos + 1
	}
	return pos
}

// updateAddAccountForm handles all key input while the add-account popup is
// open: ctrl+r toggles login/register mode, tab/shift+tab cycles the active
// fields, enter on any field submits, esc cancels.
func (m Model) updateAddAccountForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	// Only esc cancels — not the full Back/ConfirmNo bindings, since those
	// also bind plain letters ("n") that must reach the focused text field
	// while typing instead of being swallowed as a cancel key here.
	case msg.String() == "esc":
		if !m.addAccountBusy {
			m.addingAccount = false
			return m, nil
		}
		return m, nil

	case msg.String() == "ctrl+r":
		if m.addAccountBusy {
			return m, nil
		}
		m.addAccountRegister = !m.addAccountRegister
		m.addAccountErr = ""
		// A field that only exists in register mode may have been focused;
		// clamp back onto the JID field rather than landing somewhere hidden.
		if !m.addAccountRegister && m.addAccountFocus == 2 {
			m.addAccountInputs[m.addAccountFocus].Blur()
			m.addAccountFocus = 0
			m.addAccountInputs[m.addAccountFocus].Focus()
		}
		return m, nil

	case msg.String() == "tab", msg.String() == "down":
		count := m.addAccountFieldCount()
		pos := 0
		for i := 0; i < count; i++ {
			if m.addAccountFieldIndex(i) == m.addAccountFocus {
				pos = i
				break
			}
		}
		m.addAccountInputs[m.addAccountFocus].Blur()
		m.addAccountFocus = m.addAccountFieldIndex((pos + 1) % count)
		m.addAccountInputs[m.addAccountFocus].Focus()
		return m, textinput.Blink

	case msg.String() == "shift+tab", msg.String() == "up":
		count := m.addAccountFieldCount()
		pos := 0
		for i := 0; i < count; i++ {
			if m.addAccountFieldIndex(i) == m.addAccountFocus {
				pos = i
				break
			}
		}
		m.addAccountInputs[m.addAccountFocus].Blur()
		m.addAccountFocus = m.addAccountFieldIndex((pos - 1 + count) % count)
		m.addAccountInputs[m.addAccountFocus].Focus()
		return m, textinput.Blink

	case matchesKey(msg, m.keys.SelectSend):
		if m.addAccountBusy {
			return m, nil
		}
		jid := strings.TrimSpace(m.addAccountInputs[0].Value())
		if jid == "" {
			m.addAccountErr = "JID is required"
			return m, nil
		}
		if m.accountAdder == nil {
			m.addAccountErr = "no account adder configured"
			return m, nil
		}
		password := m.addAccountInputs[1].Value()
		if m.addAccountRegister {
			if password == "" {
				m.addAccountErr = "password is required to register"
				return m, nil
			}
			if password != m.addAccountInputs[2].Value() {
				m.addAccountErr = "passwords don't match"
				return m, nil
			}
		}
		gpgKeyID := strings.TrimSpace(m.addAccountInputs[3].Value())
		m.addAccountBusy = true
		m.addAccountErr = ""
		adder := m.accountAdder
		register := m.addAccountRegister
		return m, func() tea.Msg { return adder.AddAccount(jid, password, gpgKeyID, register) }

	default:
		var cmd tea.Cmd
		m.addAccountInputs[m.addAccountFocus], cmd = m.addAccountInputs[m.addAccountFocus].Update(msg)
		return m, cmd
	}
}
