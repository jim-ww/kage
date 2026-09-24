package ui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// The avatar menu: a two-item popup for publishing or removing this
// account's own avatar (XEP-0084, see the daemon's avatars.go).
//
// Reached from the accounts panel for now, next to its siblings (add
// account, OMEMO devices, contacts), since an avatar belongs to the account
// rather than to a chat. That panel is a poor home for all of them — it has
// to be found before any of its actions can be — and they belong in an
// account modal reachable by both click and keybind; until that exists this
// at least sits where the others do, rather than on a keybinding of its own
// that no terminal reports reliably.
type avatarMenuState struct {
	index int
	busy  bool
	err   string
}

const (
	avatarMenuSet = iota
	avatarMenuRemove
	avatarMenuItems
)

var avatarMenuLabels = [avatarMenuItems]string{
	avatarMenuSet:    "Set from file…",
	avatarMenuRemove: "Remove avatar",
}

// openAvatarMenu opens the popup, refusing when there's no account to
// publish for or nothing wired up to publish with.
func (m *Model) openAvatarMenu() tea.Cmd {
	if m.avatarPublisher == nil {
		return m.showNotification("publishing avatars is not available")
	}
	if m.currentAccount < 0 || m.currentAccount >= len(m.accounts) {
		return m.showNotification("no account selected")
	}
	m.avatarMenu = &avatarMenuState{}
	return nil
}

// updateAvatarMenuKey handles input while the avatar menu is open: up/down
// moves, enter picks, esc closes. Reports whether it consumed the key.
func (m Model) updateAvatarMenuKey(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	s := m.avatarMenu
	if s == nil {
		return m, nil, false
	}
	if s.busy {
		// A publish is a network round trip; let esc through so the popup
		// can't trap the user, but don't start a second one.
		if matchesKey(msg, m.keys.Back) {
			m.avatarMenu = nil
		}
		return m, nil, true
	}
	switch {
	case matchesKey(msg, m.keys.Back), matchesKey(msg, m.keys.ConfirmNo):
		m.avatarMenu = nil
	case matchesKey(msg, m.keys.MsgUp):
		s.index = (s.index - 1 + avatarMenuItems) % avatarMenuItems
	case matchesKey(msg, m.keys.MsgDown):
		s.index = (s.index + 1) % avatarMenuItems
	case matchesKey(msg, m.keys.SelectSend):
		return m.chooseAvatarMenuItem()
	}
	return m, nil, true
}

func (m Model) chooseAvatarMenuItem() (Model, tea.Cmd, bool) {
	switch m.avatarMenu.index {
	case avatarMenuSet:
		// Hand off to the file picker already used for attachments rather
		// than growing a second one; pickingAvatar is what tells its
		// selection handler where the path is going.
		m.avatarMenu = nil
		m.pickingAvatar = true
		m.pickingFile = true
		m.filePicker.SetHeight(max(1, m.height-m.inputAreaHeight()-6))
		return m, m.filePicker.Init(), true
	case avatarMenuRemove:
		m.avatarMenu.busy = true
		return m, m.removeOwnAvatarCmd(m.currentAccount), true
	}
	return m, nil, true
}

// setOwnAvatarCmd and removeOwnAvatarCmd run as commands, not inline: both
// are IPC round trips that publish to the network, which would block the
// UI for as long as the server takes (see CLAUDE.md on slow ops).
func (m Model) setOwnAvatarCmd(accountIdx int, path string) tea.Cmd {
	publisher := m.avatarPublisher
	return func() tea.Msg {
		return AvatarPublishedMsg{AccountIdx: accountIdx, Removed: false, Err: publisher.SetOwnAvatar(accountIdx, path)}
	}
}

func (m Model) removeOwnAvatarCmd(accountIdx int) tea.Cmd {
	publisher := m.avatarPublisher
	return func() tea.Msg {
		return AvatarPublishedMsg{AccountIdx: accountIdx, Removed: true, Err: publisher.RemoveOwnAvatar(accountIdx)}
	}
}

// renderAvatarMenuPopup draws the menu centered in the chat pane's popup
// slot, the same place every other popup uses.
func (m Model) renderAvatarMenuPopup() string {
	popup := m.styles.popupDialog(m.styles.colors.borderA, m.avatarMenuPrompt())
	return lipgloss.Place(m.chatAreaWidth(), m.height-m.inputAreaHeight(), lipgloss.Center, lipgloss.Center, popup)
}

func (m Model) avatarMenuPrompt() string {
	s := m.avatarMenu
	if s == nil {
		return ""
	}
	rows := make([]string, 0, avatarMenuItems+2)
	for i, label := range avatarMenuLabels {
		prefix := "  "
		row := label
		if i == s.index {
			prefix = "> "
			row = m.styles.messageNickMe.Render(label)
		}
		rows = append(rows, prefix+row)
	}
	footer := "enter select · esc close"
	if s.busy {
		rows = append(rows, "", "publishing…")
		footer = "esc close"
	} else if s.err != "" {
		rows = append(rows, "", m.styles.popupDanger.Render(s.err))
	}
	return m.styles.listPopup("Avatar", rows, footer)
}
