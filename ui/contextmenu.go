package ui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
)

// contextMenuItem is one entry in a right-click popup menu: a label plus
// the action it runs when clicked. run mutates m directly (pointer
// receiver) and returns whatever tea.Cmd the equivalent keybinding would.
type contextMenuItem struct {
	label string
	run   func(m *Model) tea.Cmd
}

// contextMenu is the right-click popup state: a fixed list of actions
// applicable to whatever component was right-clicked, rendered as a small
// centered popup (renderContextMenuPopup in view.go) until an item is
// picked or the menu is dismissed. Keyboard-only users don't need this —
// every action here already has its own keybinding — so the menu only
// responds to mouse clicks (see handleContextMenuClick) plus Back/ConfirmNo
// to close it.
type contextMenu struct {
	items []contextMenuItem
}

func zoneContextMenuItem(i int) string { return fmt.Sprintf("ctxmenu-item-%d", i) }

func (m *Model) openContextMenu(items []contextMenuItem) {
	if len(items) == 0 {
		return
	}
	m.contextMenu = &contextMenu{items: items}
}

func (m *Model) closeContextMenu() {
	m.contextMenu = nil
}

// messageContextMenuItems builds the right-click menu for the message at
// idx (already selected by the caller — see handleRightClick). Order:
// content actions first (copy/open/save), then compose actions
// (reply/edit/react), then info, then the destructive delete last.
func (m *Model) messageContextMenuItems(idx int) []contextMenuItem {
	msgs := m.currentMessages()
	if idx < 0 || idx >= len(msgs) {
		return nil
	}

	items := []contextMenuItem{
		{label: "Copy", run: (*Model).actionYankMessage},
	}
	if len(openableItems(msgs[idx])) > 0 {
		items = append(items,
			contextMenuItem{label: "Open", run: (*Model).actionOpenMessage},
			contextMenuItem{label: "Save as", run: (*Model).actionSaveMessage},
		)
	}
	items = append(items, contextMenuItem{label: "Reply", run: (*Model).actionReplyMessage})
	if m.canEdit(msgs) {
		items = append(items, contextMenuItem{label: "Edit", run: (*Model).actionEditMessage})
	}
	items = append(items,
		contextMenuItem{label: "React", run: (*Model).actionReactMessage},
		contextMenuItem{label: "Info", run: (*Model).actionInfoMessage},
		contextMenuItem{label: "Delete", run: (*Model).actionDeleteMessage},
	)
	return items
}

// chatItemContextMenuItems builds the right-click menu for the chat at idx
// (already selected by the caller).
func (m *Model) chatItemContextMenuItems(idx int) []contextMenuItem {
	if idx < 0 || idx >= len(m.chats.Items()) {
		return nil
	}
	return []contextMenuItem{
		{label: "Open", run: func(m *Model) tea.Cmd {
			model, cmd := m.openCurrentChat()
			*m = model.(Model)
			return cmd
		}},
		{label: "Rename", run: (*Model).actionRenameChat},
		{label: "Encryption", run: (*Model).actionOpenEncryptionMenu},
		{label: "Leave chat", run: (*Model).actionLeaveChat},
	}
}

// accountRowContextMenuItems builds the right-click menu for the account at
// idx (already selected/switched to by the caller).
func (m *Model) accountRowContextMenuItems(idx int) []contextMenuItem {
	if idx < 0 || idx >= len(m.accounts) {
		return nil
	}
	return []contextMenuItem{
		{label: "Switch to", run: func(m *Model) tea.Cmd { return m.switchAccount(idx) }},
		{label: "Make default", run: func(m *Model) tea.Cmd { return m.actionMakeDefaultAccount(idx) }},
		{label: "Status", run: func(m *Model) tea.Cmd { return m.actionOpenAccountStatusMenu(idx) }},
	}
}

// accountStatuses lists every selectable account status, in the order
// offered by actionOpenAccountStatusMenu.
var accountStatuses = []Presence{PresenceOnline, PresenceChat, PresenceAway, PresenceXA, PresenceDND, PresenceOffline}

// actionOpenAccountStatusMenu opens a picker submenu (account row context
// menu's "Status") listing every status with the current one marked, so
// picking one sets it directly — mirrors actionOpenEncryptionMenu.
func (m *Model) actionOpenAccountStatusMenu(idx int) tea.Cmd {
	if idx < 0 || idx >= len(m.accounts) || m.accountStatusSetter == nil {
		return nil
	}
	current := m.accounts[idx].Status
	items := make([]contextMenuItem, 0, len(accountStatuses))
	for _, status := range accountStatuses {
		label := presenceLabel(status)
		if status == current {
			label = "✓ " + label
		}
		items = append(items, contextMenuItem{
			label: label,
			run:   func(m *Model) tea.Cmd { return m.actionSetAccountStatus(idx, status) },
		})
	}
	m.openContextMenu(items)
	return nil
}

// actionSetAccountStatus sets account idx's status directly (the status
// picker's per-status entries) — runs as a tea.Cmd since it may dial or
// disconnect the account over the network.
func (m *Model) actionSetAccountStatus(idx int, status Presence) tea.Cmd {
	if idx < 0 || idx >= len(m.accounts) || m.accountStatusSetter == nil {
		return nil
	}
	return func() tea.Msg { return m.accountStatusSetter.SetAccountStatus(idx, status) }
}
