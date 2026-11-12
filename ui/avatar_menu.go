package ui

import (
	tea "charm.land/bubbletea/v2"
)

// The avatar menu: publishing or removing this account's own avatar
// (XEP-0084, see the daemon's avatars.go).
//
// Built as an ordinary context menu rather than a popup of its own, so it
// is clickable and keyboard-navigable for free and looks like every other
// menu in the app. It had a bespoke implementation first, which meant a
// second set of key handling, a second rendering, and no mouse support at
// all.
//
// Reached from the account menu, since an avatar belongs to the account
// rather than to a chat.
func (m *Model) openAvatarMenu() tea.Cmd {
	if m.avatarPublisher == nil {
		return m.showNotification("publishing avatars is not available")
	}
	accountIdx := m.currentAccount
	if accountIdx < 0 || accountIdx >= len(m.accounts) {
		return m.showNotification("no account selected")
	}
	m.openContextMenu("Avatar", []contextMenuItem{
		{label: "Set from file…", run: func(m *Model) tea.Cmd {
			// The file picker already used for attachments, rather than
			// a second one.
			return m.openFilePicker(true)
		}},
		{label: "Remove avatar", run: func(m *Model) tea.Cmd {
			return m.removeOwnAvatarCmd(accountIdx)
		}},
	})
	return nil
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
