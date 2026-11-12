package ui

import (
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jim-ww/kage/ui/modal"
)

// contextMenuItem is one entry in a right-click popup menu: a label plus
// the action it runs when clicked. run mutates m directly (pointer
// receiver) and returns whatever tea.Cmd the equivalent keybinding would.
type contextMenuItem struct {
	label string
	// key, when set, is a single-letter shortcut that runs the item straight
	// from the keyboard while the menu is open, shown right-aligned on its
	// row. It's checked before the menu's own navigation/close keys, so a
	// letter ConfirmNo uses ("n") only belongs on an item that itself means
	// "no".
	key rune
	run func(m *Model) tea.Cmd
}

// menuModalID finds the open menu, to rebuild it (see refreshContextMenu).
const menuModalID = "menu"

// contextMenuBody is a menu modal's body: a modal.Menu of items' labels and
// keys, plus the items themselves, whose run picking one calls.
type contextMenuBody struct {
	*modal.Menu
	items []contextMenuItem
	// refresh, when set, rebuilds title and items from the model's current
	// state - for a menu whose actions depend on something that can change
	// while it's open (the call menu: a call ringing, connecting, ending).
	// See refreshContextMenu.
	refresh func(m *Model) (string, []contextMenuItem)
}

func menuItems(items []contextMenuItem) []modal.MenuItem {
	out := make([]modal.MenuItem, len(items))
	for i, item := range items {
		out[i] = modal.MenuItem{Label: item.label, Key: item.key}
	}
	return out
}

// openContextMenu opens a menu of items, titled with what they act on. An
// item's run happens after the menu has closed, so it can open another
// (a submenu).
func (m *Model) openContextMenu(title string, items []contextMenuItem) *contextMenuBody {
	if len(items) == 0 {
		return nil
	}
	if title == "" {
		title = contextMenuDefaultTitle
	}
	menu := modal.NewMenu(menuItems(items)...)
	menu.MatchKey = matchesLetter
	menu.Styles.Cursor = onAccent(m.styles.colors.accentCyan)
	menu.Styles.Hover = lipgloss.NewStyle().Foreground(m.styles.colors.accentCyan)
	menu.Styles.Key = lipgloss.NewStyle().Foreground(m.styles.colors.time)
	body := &contextMenuBody{Menu: menu, items: items}
	m.openModal(&modal.Modal[*Model]{
		ID:    menuModalID,
		Title: title,
		Body:  body,
		OnSubmit: func(m *Model, v any) tea.Cmd {
			return body.items[v.(int)].run(m)
		},
	})
	return body
}

// refreshContextMenu rebuilds the open menu from current state if it was
// opened with a refresh func, and closes it if nothing applicable is left.
func (m *Model) refreshContextMenu() {
	md := m.modals.Find(menuModalID)
	if md == nil {
		return
	}
	body := md.Body.(*contextMenuBody)
	if body.refresh == nil {
		return
	}
	title, items := body.refresh(m)
	if len(items) == 0 {
		m.modals.Remove(menuModalID)
		return
	}
	md.Title, body.items = title, items
	body.SetItems(menuItems(items)...)
}

// messageContextMenuItems builds the right-click menu for the message at
// idx (already selected by the caller — see handleRightClick). Order:
// content actions first (copy/open/save/save as), then compose actions
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
			contextMenuItem{label: "Save", run: (*Model).actionSaveMessage},
			contextMenuItem{label: "Save as", run: (*Model).actionSaveMessageAs},
		)
	}
	items = append(items, contextMenuItem{label: "Reply", run: (*Model).actionReplyMessage})
	if m.canEdit(msgs) {
		items = append(items, contextMenuItem{label: "Edit", run: (*Model).actionEditMessage})
	}
	if msgs[idx].Failed {
		items = append(items, contextMenuItem{label: "Retry", run: (*Model).actionRetryMessage})
	}
	// Same entry either way - actionDeleteMessage picks retract vs. purge -
	// but the label says which of the two this press will ask about.
	deleteLabel := "Delete"
	if msgs[idx].Retracted {
		deleteLabel = "Erase for good"
	}
	if expanded, ok := m.messageCollapsible(idx); ok {
		label := "Expand"
		if expanded {
			label = "Collapse"
		}
		items = append(items, contextMenuItem{label: label, run: (*Model).actionToggleExpandMessage})
	}
	items = append(items,
		contextMenuItem{label: "React", run: (*Model).actionReactMessage},
		contextMenuItem{label: "Info", run: (*Model).actionInfoMessage},
		contextMenuItem{label: deleteLabel, run: (*Model).actionDeleteMessage},
	)
	return items
}

// chatItemContextMenuItems builds the right-click menu for the chat at idx
// (already selected by the caller). No "Open": opening is what a plain
// click or enter on the row already does, so the entry only ever restated
// the gesture that got you here.
func (m *Model) chatItemContextMenuItems(idx int) []contextMenuItem {
	if idx < 0 || idx >= len(m.chats.Items()) {
		return nil
	}
	pin, mute := "Pin chat", "Mute notifications"
	if chat, ok := m.chats.Items()[idx].(Chat); ok {
		if chat.Pinned {
			pin = "Unpin chat"
		}
		if chat.Muted {
			mute = "Unmute notifications"
		}
	}
	return []contextMenuItem{
		{label: "Rename", run: (*Model).actionRenameChat},
		{label: pin, run: (*Model).actionTogglePinChat},
		{label: mute, run: (*Model).actionToggleMuteChat},
		{label: "Encryption", run: (*Model).actionOpenEncryptionMenu},
		{label: "Hide chat", run: (*Model).actionHideChat},
	}
}

// contactRowContextMenuItems builds the options menu for the contact-manager
// row at address (already selected by the caller — see contacts.go),
// opened via Enter or a click on the row.
func (m *Model) contactRowContextMenuItems(address string) []contextMenuItem {
	items := []contextMenuItem{}
	// The contact manager is where a hidden chat is reachable again: it
	// lists the whole roster, hidden or not, so a contact you never hear
	// from isn't stranded off the chat list forever.
	if accountIdx := m.contactManagerAccount(); m.isChatHidden(accountIdx, address) {
		items = append(items, contextMenuItem{label: "Unhide chat", run: func(m *Model) tea.Cmd {
			cmd := m.unhideChat(accountIdx, address)
			m.refreshContacts()
			return cmd
		}})
	}
	return append(items,
		contextMenuItem{label: "Resubscribe", run: func(m *Model) tea.Cmd { return m.actionResubscribeContact(address) }},
		contextMenuItem{label: "Remove", run: func(m *Model) tea.Cmd {
			return m.confirm("Remove "+address+"?", "This also unsubscribes from their presence.",
				func(m *Model) tea.Cmd { return m.removeContact(address) })
		}},
	)
}

// contactManagerAccount is the account the contact manager is open for,
// or the current one when it isn't open.
func (m Model) contactManagerAccount() int {
	if body := m.contactsOpen(); body != nil {
		return body.accountIdx
	}
	return m.currentAccount
}

// actionResubscribeContact re-sends a subscription request for address on
// the contact manager's current account, run as a tea.Cmd since it's
// network I/O (see Daemon.ResubscribeContact).
func (m *Model) actionResubscribeContact(address string) tea.Cmd {
	body := m.contactsOpen()
	if body == nil {
		return nil
	}
	accountIdx := body.accountIdx
	manager := m.daemon
	body.setBusy("resubscribing to " + address + "…")
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
			return m.openContactManager()
		}},
		{label: "OMEMO devices", run: func(m *Model) tea.Cmd {
			m.switchAccount(idx)
			return m.openDeviceList()
		}},
	}
	items = append(items, contextMenuItem{label: "Storage password", run: func(m *Model) tea.Cmd {
		m.switchAccount(idx)
		return m.openChangePasswordPopup()
	}})
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
	m.openContextMenu(m.accounts[idx].NameWithJID(), items)
	return nil
}

// actionRemoveAccount opens the remove-account confirmation for the account
// at idx (already selected/switched to by the caller — see
// accountRowContextMenuItems). Confirming disconnects it and drops it from
// config.toml (see Daemon) without touching local storage.
func (m *Model) actionRemoveAccount(idx int) tea.Cmd {
	if idx < 0 || idx >= len(m.accounts) {
		return m.showNotification("account removal unavailable")
	}
	if idx != m.currentAccount {
		return nil
	}
	return m.confirm("Remove account?",
		m.accounts[idx].DisplayName()+" — disconnects and drops it from config; local history is kept",
		(*Model).removeCurrentAccount)
}

// removeCurrentAccount runs as a tea.Cmd since it purges the account's
// published OMEMO device list over the network before disconnecting (see
// Daemon.RemoveAccount).
func (m *Model) removeCurrentAccount() tea.Cmd {
	if m.currentAccount < 0 || m.currentAccount >= len(m.accounts) {
		return nil
	}
	accountIdx := m.currentAccount
	return func() tea.Msg { return m.daemon.RemoveAccount(accountIdx) }
}

// accountStatuses lists every selectable account status, in the order
// offered by actionOpenAccountStatusMenu.
var accountStatuses = []Presence{PresenceOnline, PresenceChat, PresenceAway, PresenceXA, PresenceDND, PresenceInvisible, PresenceOffline}

// actionOpenAccountStatusMenu opens a picker submenu (account row context
// menu's "Status") listing every status with the current one marked, so
// picking one sets it directly — mirrors actionOpenEncryptionMenu.
func (m *Model) actionOpenAccountStatusMenu(idx int) tea.Cmd {
	if idx < 0 || idx >= len(m.accounts) {
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
	m.openContextMenu("Status", items)
	return nil
}

// actionSetAccountStatus sets account idx's status directly (the status
// picker's per-status entries) — runs as a tea.Cmd since it may dial or
// disconnect the account over the network.
func (m *Model) actionSetAccountStatus(idx int, status Presence) tea.Cmd {
	if idx < 0 || idx >= len(m.accounts) {
		return nil
	}
	return func() tea.Msg { return m.daemon.SetAccountStatus(idx, status) }
}

// chatMenuTitle names the chat a right-click menu applies to, falling back
// to the generic title when the row isn't a Chat.
func chatMenuTitle(items []list.Item, idx int) string {
	if idx < 0 || idx >= len(items) {
		return contextMenuDefaultTitle
	}
	if chat, ok := items[idx].(Chat); ok && chat.Name != "" {
		return chat.Name
	}
	return contextMenuDefaultTitle
}

// contextMenuDefaultTitle is what a menu is headed when there's nothing
// more specific to name.
const contextMenuDefaultTitle = "Actions"
