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
	accountIdx := m.currentAccount
	if accountIdx < 0 || accountIdx >= len(m.accounts) {
		return m.showNotification("no account selected")
	}
	body := m.openContextMenu("Avatar", []contextMenuItem{
		{label: "Set from file…", run: func(m *Model) tea.Cmd {
			// The file picker already used for attachments, rather than
			// a second one.
			return m.openFilePicker(true)
		}},
		{label: "Remove avatar", run: func(m *Model) tea.Cmd {
			return m.removeOwnAvatarCmd(accountIdx)
		}},
	})
	body.header = &ownAvatarHeader{jid: m.accounts[accountIdx].Name, accountIdx: accountIdx, checking: true, styles: m.styles, gen: ^uint64(0)}
	// What's cached may be stale or missing - set from another client.
	daemon := m.daemon
	return func() tea.Msg { return daemon.SyncOwnAvatar(accountIdx) }
}

// avatarMenuPictureRows is the current avatar's height in the avatar menu;
// it's twice as many columns wide.
const avatarMenuPictureRows = 6

// ownAvatarHeader is the avatar menu's header: the account's current
// avatar, or that it has none - once checking, the fetch from the server
// asked for on opening the menu, is done (see OwnAvatarSyncedMsg).
// Rendered again only once an avatar changes.
type ownAvatarHeader struct {
	jid        string
	accountIdx int
	checking   bool
	styles     *uiStyles

	rendered string
	gen      uint64 // avatar generation rendered is for
}

func (h *ownAvatarHeader) View() string {
	g := avatarGeneration()
	if g == h.gen && h.rendered != "" {
		return h.rendered
	}
	// Shown with avatars turned off too: it's what's being changed.
	if img, ok := avatarPicture(h.jid); ok && img != nil {
		h.gen, h.rendered = g, renderImageBlocks(img, 2*avatarMenuPictureRows, avatarMenuPictureRows)
		return h.rendered
	}
	if h.checking {
		return h.styles.messageMuted.Render("checking…")
	}
	h.gen, h.rendered = g, h.styles.messageMuted.Render("no avatar set")
	return h.rendered
}

// ownAvatarSynced ends the open avatar menu's check for accountIdx.
func (m *Model) ownAvatarSynced(accountIdx int) {
	md := m.modals.Find(menuModalID)
	if md == nil {
		return
	}
	if h, ok := md.Body.(*contextMenuBody).header.(*ownAvatarHeader); ok && h.accountIdx == accountIdx {
		h.checking = false
	}
}

// setOwnAvatarCmd and removeOwnAvatarCmd run as commands, not inline: both
// are IPC round trips that publish to the network, which would block the
// UI for as long as the server takes (see CLAUDE.md on slow ops).
func (m Model) setOwnAvatarCmd(accountIdx int, path string) tea.Cmd {
	publisher := m.daemon
	return func() tea.Msg {
		return AvatarPublishedMsg{AccountIdx: accountIdx, Removed: false, Err: publisher.SetOwnAvatar(accountIdx, path)}
	}
}

func (m Model) removeOwnAvatarCmd(accountIdx int) tea.Cmd {
	publisher := m.daemon
	return func() tea.Msg {
		return AvatarPublishedMsg{AccountIdx: accountIdx, Removed: true, Err: publisher.RemoveOwnAvatar(accountIdx)}
	}
}
