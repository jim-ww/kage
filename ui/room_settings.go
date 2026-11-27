package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/jim-ww/kage/ui/modal"
)

// curatedRoomSettings are the room settings offered, in order and by our
// labels (XEP-0045 §16.5.3), each by the first of its Vars the room's form
// has, the rest left as they are. Some servers name one their own way.
var curatedRoomSettings = []struct {
	Vars  []string
	Label string
}{
	{[]string{"muc#roomconfig_roomname"}, "Name"},
	{[]string{"muc#roomconfig_roomdesc"}, "Description"},
	{[]string{"muc#roomconfig_roomsecret"}, "Password"},
	{[]string{"muc#roomconfig_membersonly"}, "Members only"},
	{[]string{"muc#roomconfig_whois"}, "Addresses visible to"},
	{[]string{"muc#roomconfig_moderatedroom"}, "Moderated: only those with voice speak"},
	{[]string{"muc#roomconfig_allowinvites", "{http://prosody.im/protocol/muc}roomconfig_allowmemberinvites"}, "Members may invite others"},
	{[]string{"muc#roomconfig_changesubject"}, "Anyone may change the subject"},
	{[]string{"muc#roomconfig_allowpm"}, "Private messages from"},
	{[]string{"muc#roomconfig_publicroom"}, "Listed in the server's public rooms"},
	{[]string{"muc#roomconfig_persistentroom"}, "Kept when everyone leaves"},
	{[]string{"muc#roomconfig_enablearchiving"}, "History archived on the server"},
}

// passwordProtectedVar goes with the password where a room has both: on
// with one set, off without.
const passwordProtectedVar = "muc#roomconfig_passwordprotectedroom"

// actionRoomSettings reads the selected room's settings, to change them.
func (m *Model) actionRoomSettings() tea.Cmd {
	chat, ok := m.currentChat()
	if !ok || !chat.Room {
		return nil
	}
	accountIdx, daemon := m.currentAccount, m.daemon
	return tea.Batch(
		m.showNotification("reading the room's settings…"),
		func() tea.Msg { return daemon.RoomSettings(accountIdx, chat.Address) },
	)
}

// openRoomSettings opens the settings screen of room with settings, as
// the room sent them.
func (m *Model) openRoomSettings(accountIdx int, room string, settings []RoomSetting) tea.Cmd {
	byVar := make(map[string]RoomSetting, len(settings))
	for _, s := range settings {
		byVar[s.Var] = s
	}
	var shown []RoomSetting
	var fields []modal.Field
	for _, c := range curatedRoomSettings {
		var s RoomSetting
		ok := false
		for _, v := range c.Vars {
			if s, ok = byVar[v]; ok {
				break
			}
		}
		if !ok {
			continue
		}
		var field modal.Field
		switch s.Kind {
		case "text":
			field = m.newInput(s.Value, "", c.Label+": ", "")
		case "secret":
			in := m.newPasswordInput(c.Label + ": ")
			in.Model.SetValue(s.Value)
			in.Model.Placeholder = "none"
			field = in
		case "bool":
			field = m.newToggle(c.Label, s.Value == "1")
		case "choice":
			options := make([]modal.ChoiceOption, len(s.Options))
			for i, o := range s.Options {
				options[i] = modal.ChoiceOption{Value: o.Value, Label: o.Label}
			}
			field = m.newChoice(c.Label, s.Value, options...)
		default:
			continue
		}
		shown = append(shown, s)
		fields = append(fields, field)
	}
	if len(fields) == 0 {
		return m.showNotification("the room offers no settings kage knows")
	}
	name := room
	if chat, ok := m.chatByAddress(accountIdx, room); ok && chat.Name != "" {
		name = chat.Name
	}
	form := m.newForm("Members only, with addresses visible to anyone, makes it a private room: its messages are encrypted.", "save", fields...)
	_, protectable := byVar[passwordProtectedVar]
	daemon := m.daemon
	return m.openModal(&modal.Modal[*Model]{
		ID:    roomSettingsModalID,
		Title: name + " · Settings",
		Body:  form,
		OnAct: func(m *Model, v any) tea.Cmd {
			changed := changedRoomSettings(shown, v.([]string))
			if secret, ok := changed["muc#roomconfig_roomsecret"]; ok && protectable {
				changed[passwordProtectedVar] = boolSetting(secret != "")
			}
			if len(changed) == 0 {
				m.modals.Remove(roomSettingsModalID)
				return nil
			}
			form.SetBusy("saving…")
			return func() tea.Msg { return daemon.SaveRoomSettings(accountIdx, room, changed) }
		},
	})
}

// changedRoomSettings is what values, the form's for settings, change, by
// Var.
func changedRoomSettings(settings []RoomSetting, values []string) map[string]string {
	changed := map[string]string{}
	for i, s := range settings {
		v := values[i]
		if s.Kind == "bool" {
			v = boolSetting(v == "true")
		}
		if v != s.Value {
			changed[s.Var] = v
		}
	}
	return changed
}

func boolSetting(on bool) string {
	if on {
		return "1"
	}
	return "0"
}
