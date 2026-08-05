package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// contactManagerState holds the state of the "manage contacts" popup, active
// while non-nil. Lists the current account's roster (drawn from its Chats,
// same data the sidebar uses), with an add-contact text-input sub-mode and a
// digit-select-then-confirm delete flow, mirroring deviceListState.
type contactManagerState struct {
	accountIdx int
	page       int
	busy       bool
	err        string

	// pendingRemove is the address staged for removal; non-empty while the
	// "remove this contact?" confirmation is showing.
	pendingRemove string

	adding   bool // true while the add-contact text input is focused
	addInput textinput.Model
}

// openContactManager opens the contact-manager popup for the current
// account.
func (m Model) openContactManager() (Model, tea.Cmd) {
	if m.contactManager == nil {
		return m, m.showNotification("contact management unavailable")
	}
	accountIdx := m.currentAccount
	if accountIdx < 0 || accountIdx >= len(m.accounts) {
		return m, nil
	}
	if m.accounts[accountIdx].Connecting {
		return m, m.showNotification("account is still connecting, try again shortly")
	}
	m.contactManagerState = &contactManagerState{accountIdx: accountIdx}
	return m, nil
}

// contacts returns the account's roster entries in display order, drawn from
// the same Chats list the sidebar renders.
func (cs *contactManagerState) contacts(m Model) []Chat {
	var out []Chat
	for _, item := range m.accounts[cs.accountIdx].Chats {
		if c, ok := item.(Chat); ok && c.Address != "" {
			out = append(out, c)
		}
	}
	return out
}

func newAddContactInput(m Model) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "user@server"
	ti.Prompt = "JID: "
	ti.KeyMap = m.keys.TextInputKeys
	ti.SetWidth(addAccountFieldWidth)
	applyTextInputStyles(&ti, m.styles.colors)
	ti.Focus()
	return ti
}

func (m Model) renderContactManagerPopup() string {
	cw := m.chatAreaWidth()
	vh := m.height - m.inputAreaHeight()

	popup := m.styles.popupDialog(m.styles.colors.borderA, m.contactManagerPrompt())
	return lipgloss.Place(cw, vh, lipgloss.Center, lipgloss.Center, popup)
}

func (m Model) contactManagerPrompt() string {
	cs := m.contactManagerState
	if cs == nil {
		return ""
	}
	closeKey := m.keys.ContactManager.Help().Key

	if cs.adding {
		rows := []string{cs.addInput.View()}
		if cs.busy {
			rows = append(rows, "", "adding...")
		} else if cs.err != "" {
			rows = append(rows, "", m.styles.popupDanger.Render(cs.err))
		}
		return m.styles.listPopup("Add contact", rows, "[enter] add  ·  [esc] cancel")
	}

	if cs.err != "" {
		return m.styles.infoPopup("Contacts", []string{"Error: " + cs.err}, closeKey)
	}
	if cs.busy {
		return m.styles.infoPopup("Contacts", []string{"Working…"}, closeKey)
	}

	contacts := cs.contacts(m)

	if cs.pendingRemove != "" {
		return m.styles.deletePrompt("Remove "+cs.pendingRemove+"?", "This also unsubscribes from their presence.")
	}

	start, end := openPageBounds(len(contacts), cs.page)
	page := contacts[start:end]
	rows := make([]string, 0, len(page)+2)
	if len(contacts) == 0 {
		rows = append(rows, "  no contacts")
	}
	for i, c := range page {
		label := c.Address
		if c.Name != "" && c.Name != c.Address {
			label = fmt.Sprintf("%s <%s>", c.Name, c.Address)
		}
		rows = append(rows, fmt.Sprintf("%d. %s", i+1, label))
	}

	hint := "a: add contact · digit: remove"
	if pages := openPageCount(len(contacts)); pages > 1 {
		hint = fmt.Sprintf("page %d/%d · left/right: page · %s", cs.page+1, pages, hint)
	}
	rows = append(rows, "", hint)

	return m.styles.infoPopup("Contacts", rows, closeKey)
}

// updateContactManagerKey handles all input while the contact-manager popup
// (m.contactManagerState != nil) is open.
func (m Model) updateContactManagerKey(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	cs := m.contactManagerState

	if cs.busy {
		return m, nil, true
	}

	if cs.adding {
		switch {
		case msg.String() == "esc":
			cs.adding = false
			cs.err = ""
			return m, nil, true
		case key.Matches(msg, m.keys.SelectSend):
			addr := strings.TrimSpace(cs.addInput.Value())
			if addr == "" {
				cs.err = "JID is required"
				return m, nil, true
			}
			accountIdx := cs.accountIdx
			manager := m.contactManager
			cs.busy = true
			cs.err = ""
			return m, func() tea.Msg { return manager.AddContact(accountIdx, addr) }, true
		default:
			var cmd tea.Cmd
			cs.addInput, cmd = cs.addInput.Update(msg)
			return m, cmd, true
		}
	}

	if cs.err != "" {
		if key.Matches(msg, m.keys.Back) || key.Matches(msg, m.keys.ConfirmNo) || key.Matches(msg, m.keys.ContactManager) {
			m.contactManagerState = nil
		}
		return m, nil, true
	}

	if cs.pendingRemove != "" {
		switch {
		case key.Matches(msg, m.keys.ConfirmYes):
			accountIdx := cs.accountIdx
			addr := cs.pendingRemove
			manager := m.contactManager
			cs.busy = true
			cs.pendingRemove = ""
			return m, func() tea.Msg { return manager.RemoveContact(accountIdx, addr) }, true
		case key.Matches(msg, m.keys.ConfirmNo):
			cs.pendingRemove = ""
		}
		return m, nil, true
	}

	contacts := cs.contacts(m)
	start, end := openPageBounds(len(contacts), cs.page)
	if i, ok := digitKey(msg); ok && i >= 1 && i <= end-start {
		cs.pendingRemove = contacts[start+i-1].Address
		return m, nil, true
	}

	switch msg.String() {
	case "left", "h":
		cs.page = max(0, cs.page-1)
		return m, nil, true
	case "right", "l":
		if cs.page < openPageCount(len(contacts))-1 {
			cs.page++
		}
		return m, nil, true
	case "a":
		cs.adding = true
		cs.addInput = newAddContactInput(m)
		return m, textinput.Blink, true
	}

	switch {
	case key.Matches(msg, m.keys.Back), key.Matches(msg, m.keys.ConfirmNo), key.Matches(msg, m.keys.ContactManager):
		m.contactManagerState = nil
	}
	return m, nil, true
}
