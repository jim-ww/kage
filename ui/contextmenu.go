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

// contextMenu is the popup-menu state: a fixed list of actions applicable
// to whatever it was opened on, rendered as a small centered popup
// (renderContextMenuPopup in view.go) until an item is picked or the menu
// is dismissed. Driven by mouse (see handleContextMenuClick) and by
// keyboard (cursor plus SelectSend, see updateContextMenuKey) — it used to
// be mouse-only on the grounds that every action it listed had its own
// keybinding, which stopped being true once the account menu became the
// home for actions that have none.
type contextMenu struct {
	items []contextMenuItem
	// cursor is the keyboard-selected row. Mouse hover is tracked
	// separately (hoverState), so moving the pointer doesn't yank the
	// keyboard's place in the list.
	cursor int
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

// updateContextMenuKey drives the popup from the keyboard: cursor moves,
// SelectSend runs the highlighted item, Back/ConfirmNo closes.
func (m Model) updateContextMenuKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	menu := m.contextMenu
	switch {
	case matchesKey(msg, m.keys.Back), matchesKey(msg, m.keys.ConfirmNo):
		m.closeContextMenu()
	case matchesKey(msg, m.keys.MsgUp):
		menu.cursor = (menu.cursor - 1 + len(menu.items)) % len(menu.items)
	case matchesKey(msg, m.keys.MsgDown):
		menu.cursor = (menu.cursor + 1) % len(menu.items)
	case matchesKey(msg, m.keys.SelectSend):
		if menu.cursor < 0 || menu.cursor >= len(menu.items) {
			return m, nil
		}
		cmd := menu.items[menu.cursor].run(&m)
		// An action that opened a menu of its own must not have it closed
		// again from under it — the same guard handleContextMenuClick uses.
		if m.contextMenu == menu {
			m.closeContextMenu()
		}
		return m, cmd
	}
	return m, nil
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
	if msgs[idx].Failed {
		items = append(items, contextMenuItem{label: "Retry", run: (*Model).actionRetryMessage})
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

// contactRowContextMenuItems builds the options menu for the contact-manager
// row at address (already selected by the caller — see contacts.go),
// opened via Enter or a click on the row.
func (m *Model) contactRowContextMenuItems(address string) []contextMenuItem {
	return []contextMenuItem{
		{label: "Resubscribe", run: func(m *Model) tea.Cmd { return m.actionResubscribeContact(address) }},
		{label: "Remove", run: func(m *Model) tea.Cmd {
			if m.contactManagerState != nil {
				m.contactManagerState.pendingRemove = address
			}
			return nil
		}},
	}
}

// actionResubscribeContact re-sends a subscription request for address on
// the contact manager's current account, run as a tea.Cmd since it's
// network I/O (see ContactManager.ResubscribeContact).
func (m *Model) actionResubscribeContact(address string) tea.Cmd {
	if m.contactManager == nil || m.contactManagerState == nil {
		return nil
	}
	accountIdx := m.contactManagerState.accountIdx
	manager := m.contactManager
	m.contactManagerState.busy = true
	return func() tea.Msg { return manager.ResubscribeContact(accountIdx, address) }
}

// accountRowContextMenuItems builds the right-click menu for the account at
// idx (already selected/switched to by the caller).
func (m *Model) accountRowContextMenuItems(idx int) []contextMenuItem {
	if idx < 0 || idx >= len(m.accounts) || m.accounts[idx].Removed {
		return nil
	}
	items := []contextMenuItem{
		{label: "Status", run: func(m *Model) tea.Cmd { return m.actionOpenAccountStatusMenu(idx) }},
		{label: "Avatar", run: func(m *Model) tea.Cmd {
			m.switchAccount(idx)
			return m.openAvatarMenu()
		}},
		{label: "Contacts", run: func(m *Model) tea.Cmd {
			m.switchAccount(idx)
			model, cmd := m.openContactManager()
			*m = model
			return cmd
		}},
		{label: "OMEMO devices", run: func(m *Model) tea.Cmd {
			m.switchAccount(idx)
			model, cmd := m.openDeviceList()
			*m = model
			return cmd
		}},
	}
	if m.storagePasswordChanger != nil {
		items = append(items, contextMenuItem{label: "Storage password", run: func(m *Model) tea.Cmd {
			m.switchAccount(idx)
			return m.openChangePasswordPopup()
		}})
	}
	return append(items,
		contextMenuItem{label: "Make default", run: func(m *Model) tea.Cmd { return m.actionMakeDefaultAccount(idx) }},
		contextMenuItem{label: "Remove account", run: func(m *Model) tea.Cmd { return m.actionRemoveAccount(idx) }},
	)
}

// openAccountMenu opens the account actions menu for the current account —
// the one place every account-scoped action lives. Reached by the account
// bar's menu button, by right-clicking an account row, and by the
// AccountMenu keybinding, rather than only from inside the accounts panel.
func (m *Model) openAccountMenu() tea.Cmd {
	idx := m.currentAccount
	if idx < 0 || idx >= len(m.accounts) {
		return m.showNotification("no account selected")
	}
	items := m.accountRowContextMenuItems(idx)
	if len(items) == 0 {
		return m.showNotification("no actions for this account")
	}
	m.openContextMenu(items)
	return nil
}

// actionRemoveAccount opens the remove-account confirmation for the account
// at idx (already selected/switched to by the caller — see
// accountRowContextMenuItems). Confirming disconnects it and drops it from
// config.toml (see AccountRemover) without touching local storage.
func (m *Model) actionRemoveAccount(idx int) tea.Cmd {
	if idx < 0 || idx >= len(m.accounts) || m.accountRemover == nil {
		return m.showNotification("account removal unavailable")
	}
	if idx != m.currentAccount {
		return nil
	}
	m.confirmTarget = confirmRemoveAccount
	return nil
}

// removeCurrentAccount runs as a tea.Cmd since it purges the account's
// published OMEMO device list over the network before disconnecting (see
// AccountRemover.RemoveAccount).
func (m *Model) removeCurrentAccount() tea.Cmd {
	if m.accountRemover == nil || m.currentAccount < 0 || m.currentAccount >= len(m.accounts) {
		return nil
	}
	accountIdx := m.currentAccount
	return func() tea.Msg { return m.accountRemover.RemoveAccount(accountIdx) }
}

// accountStatuses lists every selectable account status, in the order
// offered by actionOpenAccountStatusMenu.
var accountStatuses = []Presence{PresenceOnline, PresenceChat, PresenceAway, PresenceXA, PresenceDND, PresenceInvisible, PresenceOffline}

// actionOpenAccountStatusMenu opens a picker submenu (account row context
// menu's "Status") listing every status with the current one marked, so
// picking one sets it directly — mirrors actionOpenEncryptionMenu.
func (m *Model) actionOpenAccountStatusMenu(idx int) tea.Cmd {
	if idx < 0 || idx >= len(m.accounts) || m.accountStatusSetter == nil {
		return nil
	}
	acct := m.accounts[idx]
	current := acct.Status
	items := make([]contextMenuItem, 0, len(accountStatuses))
	for _, status := range accountStatuses {
		// Invisible only makes sense (and only works) if the server
		// advertised XEP-0186 support via disco — omit it entirely rather
		// than offer a dead menu entry.
		if status == PresenceInvisible && !acct.SupportsInvisible {
			continue
		}
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
