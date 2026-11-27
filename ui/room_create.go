package ui

import (
	"cmp"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jim-ww/kage/ui/modal"
)

// createRoomModalID is the new group chat form's, to answer it.
const createRoomModalID = "create-room"

// Kinds of room the new group chat form makes.
const (
	roomKindPrivate = "private"
	roomKindPublic  = "public"
)

// openCreateRoom asks what group chat to make on the current account:
// its name, a private group or a public channel, its address, and which
// contacts to invite.
func (m *Model) openCreateRoom() tea.Cmd {
	accountIdx := m.currentAccount
	if accountIdx < 0 || accountIdx >= len(m.accounts) {
		return nil
	}
	if m.accounts[accountIdx].Connecting {
		return m.showNotification("account is still connecting, try again shortly")
	}
	fields := []modal.Field{
		m.newInput("", "what it's called", "Name: ", ""),
		m.newChoice("Kind", roomKindPrivate,
			modal.ChoiceOption{Value: roomKindPrivate, Label: "private group: members only, encrypted"},
			modal.ChoiceOption{Value: roomKindPublic, Label: "public channel: anyone joins, listed"}),
		m.newInput("", "from the name for a channel, else made up", "Address: ", ""),
	}
	contacts := m.invitableContacts(accountIdx)
	for _, c := range contacts {
		label := c.Address
		if c.Name != "" && c.Name != c.Address {
			label = c.Name + " <" + c.Address + ">"
		}
		fields = append(fields, m.newToggle("Invite "+label, false))
	}
	form := m.newForm("A group chat on your server, kept when everyone leaves; you're its owner.", "create", fields...)
	daemon := m.daemon
	return m.openModal(&modal.Modal[*Model]{
		ID:    createRoomModalID,
		Title: "New group chat",
		Body:  form,
		OnAct: func(m *Model, v any) tea.Cmd {
			values := v.([]string)
			name, public, localpart := strings.TrimSpace(values[0]), values[1] == roomKindPublic, strings.TrimSpace(values[2])
			if name == "" {
				form.SetError("name is required")
				return nil
			}
			var invitees []string
			for i, c := range contacts {
				if values[3+i] == "true" {
					invitees = append(invitees, c.Address)
				}
			}
			form.SetBusy("creating…")
			return func() tea.Msg { return daemon.CreateRoom(accountIdx, name, localpart, public, invitees) }
		},
	})
}

// invitableContacts are accountIdx's contacts, by name: not rooms, nor
// private chats in them, nor the blocked.
func (m Model) invitableContacts(accountIdx int) []Chat {
	var out []Chat
	for _, item := range m.accounts[accountIdx].Chats {
		c, ok := item.(Chat)
		if !ok || c.InRoom() || c.Blocked || !strings.Contains(c.Address, "@") || strings.Contains(c.Address, "/") {
			continue
		}
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b Chat) int {
		return cmp.Compare(strings.ToLower(cmp.Or(a.Name, a.Address)), strings.ToLower(cmp.Or(b.Name, b.Address)))
	})
	return out
}

// handleRoomCreated answers the new group chat form: closed and the room
// opened once it's made.
func (m *Model) handleRoomCreated(msg RoomCreatedMsg) tea.Cmd {
	md := m.modals.Find(createRoomModalID)
	if msg.Room == "" {
		if md != nil {
			form := md.Body.(*modal.Form)
			form.SetBusy("")
			form.SetError(msg.Err.Error())
			return nil
		}
		return m.showNotification("creating the group chat: " + msg.Err.Error())
	}
	if md != nil {
		m.modals.Remove(createRoomModalID)
	}
	cmd := m.openChatByAddress(msg.AccountIdx, msg.Room)
	if msg.Err != nil {
		return tea.Batch(cmd, m.showNotification("created, but "+msg.Err.Error()))
	}
	return cmd
}
