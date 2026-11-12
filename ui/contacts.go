package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jim-ww/kage/ui/modal"
)

// Modal IDs of the contact manager and its add-contact form, to update them
// as the server answers.
const (
	contactsModalID   = "contacts"
	addContactModalID = "add-contact"
)

// contactsBody is the contact manager: the account's roster (drawn from its
// Chats, same data the sidebar uses, plus its hidden chats) as a list -
// picking a contact opens its options (see contactRowContextMenuItems), a
// adds one. While a change is with the server it shows what's happening and
// takes no input; a failure shows under the list.
type contactsBody struct {
	*modal.Menu
	accountIdx int
	addrs      []string
	status     string
	err        string
	styles     *uiStyles
}

// contactsAdd is what the contact manager acts with on a: add a contact.
type contactsAdd struct{}

func (b *contactsBody) Update(msg tea.Msg) (tea.Cmd, modal.Outcome) {
	if b.Busy() {
		return nil, modal.Continue
	}
	if k, ok := msg.(tea.KeyPressMsg); ok && matchesLetter(k, 'a') {
		return nil, modal.Act(contactsAdd{})
	}
	return b.Menu.Update(msg)
}

func (b *contactsBody) View() string {
	view := b.Menu.View()
	switch {
	case b.status != "":
		view += "\n\n" + b.styles.messageMuted.Render(b.status)
	case b.err != "":
		view += "\n\n" + b.styles.popupDanger.Render(b.err)
	}
	return view
}

func (b *contactsBody) SetSize(width, height int) { b.Menu.SetSize(width, max(1, height-2)) }

// Busy keeps the manager open while a change is with the server.
func (b *contactsBody) Busy() bool { return b.status != "" }

func (b *contactsBody) setBusy(status string) {
	b.status = status
	if status != "" {
		b.err = ""
	}
}

func (b *contactsBody) Hints() []modal.Hint {
	if b.Busy() {
		return nil
	}
	return []modal.Hint{
		modal.KeyHint(tea.KeyPressMsg{Code: tea.KeyEnter}, "options"),
		modal.KeyHint(tea.KeyPressMsg{Code: 'a', Text: "a"}, "add"),
	}
}

// openContactManager opens the contact manager for the current account.
func (m *Model) openContactManager() tea.Cmd {
	if m.contactManager == nil {
		return m.showNotification("contact management unavailable")
	}
	accountIdx := m.currentAccount
	if accountIdx < 0 || accountIdx >= len(m.accounts) {
		return nil
	}
	if m.accounts[accountIdx].Connecting {
		return m.showNotification("account is still connecting, try again shortly")
	}
	menu := modal.NewMenu()
	menu.ActOnPick = true
	menu.Empty = "no contacts"
	menu.Styles.Cursor = onAccent(m.styles.colors.accentCyan)
	menu.Styles.Hover = lipgloss.NewStyle().Foreground(m.styles.colors.accentCyan)
	body := &contactsBody{Menu: menu, accountIdx: accountIdx, styles: m.styles}
	cmd := m.openModal(&modal.Modal[*Model]{
		ID:    contactsModalID,
		Title: "Contacts",
		Body:  body,
		OnAct: func(m *Model, v any) tea.Cmd {
			switch v := v.(type) {
			case int:
				addr := body.addrs[v]
				m.openContextMenu(addr, m.contactRowContextMenuItems(addr))
			case contactsAdd:
				return m.openAddContact()
			}
			return nil
		},
	})
	m.refreshContacts()
	return cmd
}

// contactsOpen is the open contact manager, or nil.
func (m Model) contactsOpen() *contactsBody {
	if md := m.modals.Find(contactsModalID); md != nil {
		return md.Body.(*contactsBody)
	}
	return nil
}

// refreshContacts relists the open contact manager's roster.
func (m *Model) refreshContacts() {
	body := m.contactsOpen()
	if body == nil {
		return
	}
	contacts := m.contactsOf(body.accountIdx)
	body.addrs = make([]string, len(contacts))
	items := make([]modal.MenuItem, len(contacts))
	for i, c := range contacts {
		body.addrs[i] = c.Address
		label := c.Address
		if c.Name != "" && c.Name != c.Address {
			label = fmt.Sprintf("%s <%s>", c.Name, c.Address)
		}
		if s := subscriptionLabel(c); s != "" {
			label += " (" + s + ")"
		}
		if c.Hidden {
			label += " (hidden)"
		}
		items[i] = modal.MenuItem{Label: label}
	}
	body.SetItems(items...)
}

// contactsOf is accountIdx's roster in display order, drawn from the same
// Chats list the sidebar renders.
func (m Model) contactsOf(accountIdx int) []Chat {
	var out []Chat
	for _, item := range m.accounts[accountIdx].Chats {
		if c, ok := item.(Chat); ok && c.Address != "" {
			out = append(out, c)
		}
	}
	// Hidden chats are not in the account's list at all (see
	// ui/hidden_chats.go), and this is where they're reachable again.
	return append(out, m.hiddenChatsFor(accountIdx)...)
}

// openAddContact asks for the address of a contact to add to the contact
// manager's account; the form stays open (busy) until the server answers
// (ContactAddedMsg).
func (m *Model) openAddContact() tea.Cmd {
	form := m.newForm("", "add", m.newInput("", "user@server", "JID: ", ""))
	return m.openModal(&modal.Modal[*Model]{
		ID:    addContactModalID,
		Title: "Add contact",
		Body:  form,
		OnAct: func(m *Model, v any) tea.Cmd {
			addr := strings.TrimSpace(v.([]string)[0])
			body := m.contactsOpen()
			if addr == "" || body == nil {
				form.SetError("JID is required")
				return nil
			}
			form.SetBusy("adding…")
			accountIdx, manager := body.accountIdx, m.contactManager
			return func() tea.Msg { return manager.AddContact(accountIdx, addr) }
		},
	})
}

// subscriptionLabel says what's missing from a contact's presence
// subscription, "" when nothing is (both ways) or it's unknown - the
// difference between "offline" and "can't see whether they're online".
func subscriptionLabel(c Chat) string {
	switch {
	case c.SubscriptionPending:
		return "request pending"
	case c.Subscription == "to":
		return "you see them, they don't see you"
	case c.Subscription == "from":
		return "they see you, you don't see them"
	case c.Subscription == "none":
		return "not subscribed"
	default:
		return ""
	}
}

// removeContact removes address from the contact manager's account.
func (m *Model) removeContact(address string) tea.Cmd {
	body := m.contactsOpen()
	if body == nil || m.contactManager == nil {
		return nil
	}
	body.setBusy("removing " + address + "…")
	accountIdx, manager := body.accountIdx, m.contactManager
	return func() tea.Msg { return manager.RemoveContact(accountIdx, address) }
}

// contactResult applies the server's answer to a contact change on
// accountIdx: the manager stops being busy and shows err, if any; on success
// its list is refreshed.
func (m *Model) contactResult(accountIdx int, err error) bool {
	body := m.contactsOpen()
	if body == nil || body.accountIdx != accountIdx {
		return false
	}
	body.setBusy("")
	if err != nil {
		body.err = err.Error()
		return false
	}
	m.refreshContacts()
	return true
}
