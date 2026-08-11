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
func (m Model) scrolledPastFirstPage() bool {
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
func (m *Model) jumpToLatestMessage() {
	msgs := m.currentMessages()
	if len(msgs) == 0 {
		m.viewport.GotoBottom()
		return
	}
	old := m.selectedMsg
	m.selectedMsg = len(msgs) - 1
	m.refreshViewportSelection(old, m.selectedMsg)
	m.viewport.GotoBottom()
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

// encryptionModeOrDefault returns mode, or "omemo-v1" (kage's default) if
// unset.
func encryptionModeOrDefault(mode string) string {
	if mode == "" {
		return "omemo-v1"
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

	current := encryptionModeOrDefault(chat.EncryptionMode)
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

// setEmojiSuggestions replaces the live suggestion list and resets which one
// is highlighted — always reset together so the highlight never points past
// the end of a freshly narrowed list.
func (m *Model) setEmojiSuggestions(sugs []emojiSuggestion) {
	m.emojiSuggestions = sugs
	m.emojiSuggestIdx = 0
}

// acceptEmojiSuggestion accepts emojiSuggestions[idx] into the input —
// shared by the tab keybinding (idx == emojiSuggestIdx) and a click on a
// suggestion in the react hint (see handleLeftClick).
func (m *Model) acceptEmojiSuggestion(idx int) {
	if idx < 0 || idx >= len(m.emojiSuggestions) {
		return
	}
	// Insert the resolved glyph, not the raw shortcode text — otherwise a
	// freshly-picked reaction shows as literal ":thing:" in the input while
	// the prefilled existing reactions (myReactionsText) already show as
	// real emoji, which looks inconsistent.
	chosen := m.emojiSuggestions[idx].Emoji
	m.input.SetValue(acceptEmojiSuggestion(m.input.Value(), chosen))
	m.input.CursorEnd()
	var next []emojiSuggestion
	if token, _, ok := currentWordToken(m.input.Value()); ok {
		next = emojiSuggestionsFor(token)
	}
	m.setEmojiSuggestions(next)
}

// cancelPending clears any in-progress edit, reply, or reaction composition.
func (m *Model) cancelPending() {
	m.editingMsgIdx = -1
	m.replyToIdx = -1
	m.reactingMsgIdx = -1
	m.lastClickedMsgIdx = -1
	m.setEmojiSuggestions(nil)
	m.restoreStashedDraft()
	m.input.Placeholder = "message..."
	m.updateSizes()
}
