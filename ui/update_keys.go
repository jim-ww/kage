package ui

import (
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// startFileUpload kicks off an asynchronous upload+send of the local file at
// path to the given chat address, shared by both the file-picker's
// DidSelectFile path and drag-and-drop.
func (m Model) startFileUpload(to, path string) (Model, tea.Cmd) {
	if m.fileSender == nil {
		return m, m.showNotification("file sending unavailable")
	}
	if to == "" {
		return m, m.showNotification("no chat selected")
	}
	accountIdx := m.currentAccount

	// An attachment can be sent in reply, same as a text message: carry
	// whatever m.replyToIdx points at and clear it, mirroring the text-send
	// path in message_actions.go.
	var opts SendOptions
	if m.replyToIdx >= 0 {
		if msgs := m.currentMessages(); m.replyToIdx < len(msgs) && msgs[m.replyToIdx].ID != "" {
			opts = SendOptions{
				ReplyToID:    msgs[m.replyToIdx].ID,
				QuotedAuthor: msgs[m.replyToIdx].Author,
				QuotedBody:   messagePreviewContent(msgs[m.replyToIdx]),
			}
		}
		m.replyToIdx = -1
	}

	// Uploading can take a while and runs asynchronously; make the
	// accepted selection visible immediately instead of leaving the
	// user looking at an unchanged chat.
	m.noticeID++
	m.noticeText = "uploading " + filepath.Base(path) + "..."
	return m, func() tea.Msg { return m.fileSender.SendFile(accountIdx, to, path, opts) }
}

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
		m.chats, cmd = m.chats.Update(msg)
		return m, cmd, true
	}

	// ── Context menu intercepts all input until dismissed ───────────────
	// It's mouse-only otherwise (every action it lists already has its
	// own keybinding), so the keyboard's only job here is closing it.
	if m.contextMenu != nil {
		switch {
		case key.Matches(msg, m.keys.Back), key.Matches(msg, m.keys.ConfirmNo):
			m.closeContextMenu()
		}
		return m, nil, true
	}

	// ── Delete confirmation popup intercepts all input ─────────────────
	if m.confirmTarget != confirmNone {
		switch {
		case key.Matches(msg, m.keys.ConfirmYes):
			switch m.confirmTarget {
			case confirmDeleteMessage:
				// Only our own messages can meaningfully be retracted on
				// the network (XEP-0424); deleting someone else's
				// message is always local-only, same as before.
				if msgs := m.currentMessages(); m.selectedMsg >= 0 && m.selectedMsg < len(msgs) {
					target := msgs[m.selectedMsg]
					if target.IsMe && target.ID != "" {
						if chat, ok := m.currentChat(); ok && chat.Address != "" && m.sender != nil {
							if _, err := m.sender.Send(m.currentAccount, chat.Address, "", SendOptions{RetractID: target.ID}); err != nil {
								cmds = append(cmds, m.showNotification("retract not delivered: "+err.Error()))
							}
						}
					}
				}
				m.deleteSelectedMsg()
			case confirmDeleteChat:
				cmds = append(cmds, m.deleteSelectedChat())
			}
			m.confirmTarget = confirmNone
			m.refreshViewport()
			return m, tea.Batch(cmds...), true
		case key.Matches(msg, m.keys.ConfirmNo):
			m.confirmTarget = confirmNone
		}
		return m, nil, true
	}

	// ── Message-info popup intercepts all input until dismissed ────────
	if m.showMsgInfo {
		switch {
		case key.Matches(msg, m.keys.InfoMsg), key.Matches(msg, m.keys.Back), key.Matches(msg, m.keys.ConfirmNo):
			m.showMsgInfo = false
		}
		return m, nil, true
	}

	// ── OMEMO device-list popup intercepts all input until dismissed ───
	if m.deviceList != nil {
		return m.updateDeviceListKey(msg)
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

	// ── File picker intercepts all input until selected or canceled ─────
	if m.pickingFile {
		if key.Matches(msg, m.keys.AttachFile) || key.Matches(msg, m.keys.Back) || key.Matches(msg, m.keys.ConfirmNo) {
			m.pickingFile = false
			return m, nil, true
		}
		var pickerCmd tea.Cmd
		m.filePicker, pickerCmd = m.filePicker.Update(msg)
		if selected, path := m.filePicker.DidSelectFile(msg); selected {
			m.pickingFile = false
			chat, _ := m.currentChat()
			model, cmd := m.startFileUpload(chat.Address, path)
			return model, cmd, true
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
			target := m.openItems[start+i-1]
			m.openItems = nil
			m.openPage = 0
			if m.openMode == pickerModeSave {
				return m, saveURLToDownloads(target), true
			}
			return m, openWithXDGOpen(target), true
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
			case key.Matches(msg, m.keys.Back), key.Matches(msg, m.keys.ConfirmNo):
				m.openItems = nil
				m.openPage = 0
			}
		}
		return m, nil, true
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

	case (msg.String() == "tab" || key.Matches(msg, m.keys.SelectSend)) && m.reactingMsgIdx >= 0 && len(m.emojiSuggestions) > 0:
		// While a suggestion is showing, enter accepts it (like tab)
		// instead of falling through to SelectSend and sending the
		// reaction early — matches the emoji picker, not a message send.
		m.acceptEmojiSuggestion(m.emojiSuggestIdx)
		return m, nil, true

	// ── Global ────────────────────────────────────────────────────────
	case key.Matches(msg, m.keys.Quit):
		if msg.String() == "ctrl+c" || m.selectedView != viewChat {
			return m, tea.Quit, true
		}

	case key.Matches(msg, m.keys.Back):
		if m.selectedView != viewAccounts && m.selectedView != viewChats {
			m.notifyTypingStopped()
			m.cancelPending()
			m.setSelectedView(viewChats)
			m.input.Blur()
			return m, nil, true
		}

	case key.Matches(msg, m.keys.FocusChats):
		m.notifyTypingStopped()
		m.setSelectedView(viewChats)
		m.input.Blur()
		return m, nil, true

	case key.Matches(msg, m.keys.ToggleSidebar):
		model, cmd := m.toggleSidebar()
		return model, cmd, true

	case key.Matches(msg, m.keys.ChatOpen):
		if m.selectedView == viewChats {
			model, cmd := m.openCurrentChat()
			return model.(Model), cmd, true
		}

	case key.Matches(msg, m.keys.SelectSend):
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

	case key.Matches(msg, m.keys.AddAccount):
		if m.selectedView == viewAccounts {
			m.addingAccount = true
			m.addAccountFocus = 0
			m.addAccountErr = ""
			m.addAccountInputs = m.newAddAccountForm()
			return m, textinput.Blink, true
		}

	case key.Matches(msg, m.keys.RenameChat):
		if m.selectedView == viewChats {
			return m, m.actionRenameChat(), true
		}

	case key.Matches(msg, m.keys.AttachFile):
		if m.selectedView == viewChat {
			if m.currentChatIndex() < 0 {
				cmds = append(cmds, m.showNotification("no chat selected"))
				return m, tea.Batch(cmds...), true
			}
			m.pickingFile = true
			m.filePicker.SetHeight(max(1, m.height-m.inputAreaHeight()-6))
			return m, m.filePicker.Init(), true
		}

	case key.Matches(msg, m.keys.Switch):
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
			newOffset := m.viewport.YOffset()
			if len(m.msgOffsets) > 0 && newOffset != oldOffset {
				m.selectedMsg = m.msgIndexAtOffset(newOffset)
				m.refreshViewport()
				m.viewport.SetYOffset(newOffset)
			}
			if newOffset == 0 {
				cmds = append(cmds, m.maybeLoadOlderHistory())
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
	case key.Matches(msg, m.keys.MsgUp):
		if msg.String() == "up" && m.composeMultiline() {
			break
		}
		if m.selectedView == viewAccounts && m.currentAccount > 0 {
			cmds = append(cmds, m.switchAccount(m.currentAccount-1))
			return m, tea.Batch(cmds...), true
		}
		if m.selectedView == viewChat && m.selectedMsg > 0 {
			m.selectedMsg--
			m.lastClickedMsgIdx = -1
			m.lastClickTime = time.Time{}
			m.refreshViewportScrollTo(m.selectedMsg)
			return m, nil, true
		}
		if m.selectedView == viewChat && m.selectedMsg == 0 {
			return m, m.maybeLoadOlderHistory(), true
		}

	case key.Matches(msg, m.keys.MsgDown):
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
				m.selectedMsg++
				m.lastClickedMsgIdx = -1
				m.lastClickTime = time.Time{}
				m.refreshViewportScrollTo(m.selectedMsg)
				return m, nil, true
			}
		}

	// ── Message actions ────────────────────────────────────────────────
	case key.Matches(msg, m.keys.DeleteMsg):
		switch m.selectedView {
		case viewChat:
			return m, m.actionDeleteMessage(), true
		case viewChats:
			return m, m.actionLeaveChat(), true
		}

	case key.Matches(msg, m.keys.YankMsg):
		if m.selectedView == viewChat {
			return m, m.actionYankMessage(), true
		}

	case key.Matches(msg, m.keys.EditMsg):
		if m.selectedView == viewChat {
			return m, m.actionEditMessage(), true
		}

	case key.Matches(msg, m.keys.ReplyMsg):
		if m.selectedView == viewChat {
			return m, m.actionReplyMessage(), true
		}

	case key.Matches(msg, m.keys.InfoMsg):
		if m.selectedView == viewChat {
			return m, m.actionInfoMessage(), true
		}

	case key.Matches(msg, m.keys.OpenMsg):
		if m.selectedView == viewChat {
			return m, m.actionOpenMessage(), true
		}

	case key.Matches(msg, m.keys.SaveMsg):
		if m.selectedView == viewChat {
			return m, m.actionSaveMessage(), true
		}

	case key.Matches(msg, m.keys.ReactMsg):
		if m.selectedView == viewChat {
			return m, m.actionReactMessage(), true
		}

	case key.Matches(msg, m.keys.DeviceList):
		model, cmd := m.openDeviceList()
		return model, cmd, true
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

	case key.Matches(msg, m.keys.SelectSend):
		return m, m.submitRenameChat()

	default:
		var cmd tea.Cmd
		m.renameInput, cmd = m.renameInput.Update(msg)
		return m, cmd
	}
}

// updateAddAccountForm handles all key input while the add-account popup is
// open: tab/shift+tab cycles the three fields, enter on any field submits
// (password/gpg key are optional), esc cancels.
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

	case msg.String() == "tab", msg.String() == "down":
		m.addAccountInputs[m.addAccountFocus].Blur()
		m.addAccountFocus = (m.addAccountFocus + 1) % len(m.addAccountInputs)
		m.addAccountInputs[m.addAccountFocus].Focus()
		return m, textinput.Blink

	case msg.String() == "shift+tab", msg.String() == "up":
		m.addAccountInputs[m.addAccountFocus].Blur()
		m.addAccountFocus = (m.addAccountFocus - 1 + len(m.addAccountInputs)) % len(m.addAccountInputs)
		m.addAccountInputs[m.addAccountFocus].Focus()
		return m, textinput.Blink

	case key.Matches(msg, m.keys.SelectSend):
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
		gpgKeyID := strings.TrimSpace(m.addAccountInputs[2].Value())
		m.addAccountBusy = true
		m.addAccountErr = ""
		adder := m.accountAdder
		return m, func() tea.Msg { return adder.AddAccount(jid, password, gpgKeyID) }

	default:
		var cmd tea.Cmd
		m.addAccountInputs[m.addAccountFocus], cmd = m.addAccountInputs[m.addAccountFocus].Update(msg)
		return m, cmd
	}
}
