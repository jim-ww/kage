package ui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
)

func (m *Model) switchAccount(index int) tea.Cmd {
	if index < 0 || index >= len(m.accounts) || index == m.currentAccount || m.accounts[index].Removed {
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

// scrolledPastFirstPage reports whether the chat viewport has scrolled up
// more than one full screen's worth of content away from the bottom — the
// "jump to latest" button's show/hide condition. A plain !AtBottom() would
// show it the instant the bottom line scrolls out of view even by one row,
// which is still the first screenful the user would see if they scrolled
// the rest of the way down themselves; the button is for the case where
// they've paged/navigated far enough that "latest" is no longer one
// scroll away.
//
// If the currently loaded window isn't the live tail (HistoryNewer set —
// paged up far enough to fall off the tail window, or landed on a search
// result), "distance from the bottom of this window" is meaningless as a
// proxy for "distance from the actual latest message": the window can be
// short (little history left in that direction, or a narrow search-result
// window) and scrolling around inside it would cross the local YOffset/
// TotalLineCount threshold back and forth, flickering the button even
// though the real latest message is nowhere close. In that case the button
// always shows — jumpToLatestMessage does a real fetch back to the tail
// regardless of where in this window the viewport happens to sit.
func (m Model) scrolledPastFirstPage() bool {
	chatIdx := m.currentChatIndex()
	if chatIdx < 0 || m.currentAccount < 0 || m.currentAccount >= len(m.accounts) {
		return false
	}
	if m.accounts[m.currentAccount].HistoryNewer[chatIdx] {
		return true
	}
	height := m.viewport.Height()
	if height <= 0 {
		return false
	}
	return m.viewport.YOffset() < m.viewport.TotalLineCount()-2*height
}

// jumpToLatestMessage scrolls the chat viewport to the bottom and moves the
// selection to the newest loaded message — used by the floating "jump to
// latest" button shown whenever the viewport has scrolled away from the
// bottom (via message navigation, paging, or landing on a search result).
// If the currently loaded window isn't the live tail (HistoryNewer set —
// paged up, or landed on a search result), that's a real fetch anchored on
// nil (see HistoryAnchor's doc comment: nil means "the true latest window"),
// same as opening the chat fresh. Otherwise it's already showing the tail,
// so this is just a local selection/scroll move — no round trip needed.
func (m *Model) jumpToLatestMessage() tea.Cmd {
	chatIdx := m.currentChatIndex()
	if chatIdx < 0 || m.currentAccount < 0 || m.currentAccount >= len(m.accounts) {
		return nil
	}
	if m.accounts[m.currentAccount].HistoryNewer[chatIdx] && m.historyLoader != nil && !m.loadingHistoryWindow[chatIdx] {
		if chat, ok := m.currentChat(); ok {
			m.loadingHistoryWindow[chatIdx] = true
			return m.historyLoader.LoadHistoryWindow(m.currentAccount, chat.Address, nil)
		}
	}

	msgs := m.currentMessages()
	if len(msgs) == 0 {
		m.viewport.GotoBottom()
		return nil
	}
	old := m.selectedMsg
	m.selectedMsg = len(msgs) - 1
	m.refreshViewportSelection(old, m.selectedMsg)
	m.viewport.GotoBottom()
	return nil
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

// encryptionModeOrDefault returns mode, or m.defaultEncryptionMode
// (config's default_encryption_mode, "omemo-v1" if unset) if mode is unset.
func (m *Model) encryptionModeOrDefault(mode string) string {
	if mode == "" {
		if m.defaultEncryptionMode == "" {
			return "omemo-v1"
		}
		return m.defaultEncryptionMode
	}
	return mode
}

// encryptionModes lists every selectable outgoing-encryption mode, in the
// order they're offered in the encryption picker. "omemo-v1"/"omemo-v2"
// force a specific OMEMO protocol version for this chat.
var encryptionModes = []string{"omemo-v1", "omemo-v2", "gpg", "none"}

// actionOpenEncryptionMenu opens a picker submenu (chat-item context menu's
// "Encryption") listing every mode with the current one marked, so picking
// one sets it directly instead of cycling through them one keypress at a
// time.
func (m *Model) actionOpenEncryptionMenu() tea.Cmd {
	idx := m.currentChatIndex()
	items := m.chats.Items()
	if idx < 0 || idx >= len(items) || m.chatEncryptionSetter == nil {
		return m.showNotification("no chat selected")
	}
	chat, ok := items[idx].(Chat)
	if !ok {
		return nil
	}

	current := m.encryptionModeOrDefault(chat.EncryptionMode)
	menuItems := make([]contextMenuItem, 0, len(encryptionModes))
	for _, mode := range encryptionModes {
		if mode == "gpg" && !m.useGPG {
			continue
		}
		label := mode
		if mode == current {
			label = "✓ " + mode
		}
		menuItems = append(menuItems, contextMenuItem{
			label: label,
			run:   func(m *Model) tea.Cmd { return m.actionSetChatEncryption(mode) },
		})
	}
	m.openContextMenu(menuItems)
	return nil
}

// actionSetChatEncryption sets the selected chat's outgoing message
// encryption to mode directly (the encryption picker's per-mode entries).
func (m *Model) actionSetChatEncryption(mode string) tea.Cmd {
	idx := m.currentChatIndex()
	items := m.chats.Items()
	if idx < 0 || idx >= len(items) || m.chatEncryptionSetter == nil {
		return m.showNotification("no chat selected")
	}
	chat, ok := items[idx].(Chat)
	if !ok {
		return nil
	}

	if err := m.chatEncryptionSetter.SetChatEncryption(m.currentAccount, chat.Address, mode); err != nil {
		return m.showNotification("setting encryption: " + err.Error())
	}
	chat.EncryptionMode = mode
	m.accounts[m.currentAccount].Chats[idx] = chat
	cmd := m.chats.SetItem(idx, chat)
	return tea.Batch(cmd, m.showNotification("Encryption: "+mode))
}

func (m *Model) showNotification(text string) tea.Cmd {
	m.noticeID++
	m.noticeText = text
	id := m.noticeID
	return tea.Tick(m.noticeDuration, func(time.Time) tea.Msg {
		return noticeClearMsg{id: id}
	})
}

// cancelPending clears any in-progress edit, reply, reaction, or emoji-picker
// composition.
func (m *Model) cancelPending() {
	wasComposing := m.editingMsgIdx >= 0
	m.editingMsgIdx = -1
	m.replyToIdx = -1
	m.reactingMsgIdx = -1
	m.emojiPicker = nil
	m.lastClickedMsgIdx = -1
	if wasComposing {
		m.restoreStashedDraft()
	}
	m.input.Placeholder = "message..."
	m.updateSizes()
}
