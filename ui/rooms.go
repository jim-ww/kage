package ui

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"

	"github.com/jim-ww/kage/ui/modal"
)

// Modal IDs of the join form and the participants screen, to update them as
// the daemon answers.
const (
	joinRoomModalID     = "join-room"
	participantsModalID = "participants"
	roomSettingsModalID = "room-settings"
)

// roomGlyph stands for a room in the chat list where a contact has its
// presence: green while we're in it, red when the room turned us away,
// gray while joining.
func roomGlyph(c Chat) string {
	switch {
	case c.Joined:
		return presenceOnlineStyle.Render("#")
	case c.JoinError != "":
		return presenceDNDStyle.Render("#")
	}
	return presenceOfflineStyle.Render("#")
}

// roomStatus is a room's state for the status bar.
func roomStatus(c Chat) string {
	switch {
	case c.Joined:
		n := len(c.Occupants)
		status := fmt.Sprintf("%d in the room", n)
		if n == 1 {
			status = "1 in the room"
		}
		if c.Subject != "" {
			status += " · " + strings.ReplaceAll(c.Subject, "\n", " ")
		}
		return status
	case c.JoinError != "":
		return "not in the room: " + c.JoinError
	}
	return "joining…"
}

// openJoinRoom asks for a room to join on the current account, room and
// password filled in (an invitation's).
func (m *Model) openJoinRoom(room, password string) tea.Cmd {
	accountIdx := m.currentAccount
	if accountIdx < 0 || accountIdx >= len(m.accounts) {
		return nil
	}
	if m.accounts[accountIdx].Connecting {
		return m.showNotification("account is still connecting, try again shortly")
	}
	pw := m.newPasswordInput("Password: ")
	pw.Model.SetValue(password)
	pw.Model.Placeholder = "if the room has one"
	form := m.newForm("", "join",
		m.newInput(room, "room@conference.server", "Room: ", ""),
		m.newInput(defaultRoomNick(m.accounts[accountIdx].Name), "nick", "Nick: ", ""),
		pw,
	)
	return m.openModal(&modal.Modal[*Model]{
		ID:    joinRoomModalID,
		Title: "Join group chat",
		Body:  form,
		OnAct: func(m *Model, v any) tea.Cmd {
			fields := v.([]string)
			link := parseAddress(fields[0])
			room, nick, password := link.addr, strings.TrimSpace(fields[1]), fields[2]
			if password == "" {
				password = link.params["password"]
			}
			if room == "" {
				form.SetError("room is required")
				return nil
			}
			form.SetBusy("joining…")
			daemon := m.daemon
			return func() tea.Msg { return daemon.JoinRoom(accountIdx, room, nick, password) }
		},
	})
}

// defaultRoomNick is the nick offered for a room: the account JID's
// localpart.
func defaultRoomNick(accountJID string) string {
	if at := strings.IndexByte(accountJID, '@'); at > 0 {
		return accountJID[:at]
	}
	return accountJID
}

// Mentions reports whether body names nick, as a word of its own.
func Mentions(body, nick string) bool {
	if nick == "" {
		return false
	}
	lower, n := strings.ToLower(body), strings.ToLower(nick)
	for i := 0; ; {
		j := strings.Index(lower[i:], n)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(n)
		if (start == 0 || !isWordByte(lower[start-1])) && (end == len(lower) || !isWordByte(lower[end])) {
			return true
		}
		i = start + 1
	}
}

func isWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 0x80
}

// roomChatMenuItems is the chat menu for a room: what applies to a group
// chat of the contact's actions, and what only a room has.
func (m *Model) roomChatMenuItems(chat Chat) []contextMenuItem {
	pin, mute := "Pin chat", "Mute notifications"
	if chat.Pinned {
		pin = "Unpin chat"
	}
	if chat.Muted {
		mute = "Unmute notifications"
	}
	items := []contextMenuItem{
		{label: "Participants", key: 'o', run: (*Model).openParticipants},
		{label: "Change nick", key: 'n', run: (*Model).actionChangeRoomNick},
		{label: "Change subject", key: 's', run: (*Model).actionChangeRoomSubject},
		{label: "Invite", key: 'v', run: (*Model).actionInviteToRoom},
		{label: "Encryption", key: 'e', run: (*Model).actionOpenEncryptionMenu},
		{label: pin, key: 'p', run: (*Model).actionTogglePinChat},
		{label: mute, key: 'm', run: (*Model).actionToggleMuteChat},
	}
	if chat.Joined && ownOccupant(chat).Affiliation == "owner" {
		items = append(items, contextMenuItem{label: "Settings", key: 't', run: (*Model).actionRoomSettings})
	}
	if !chat.Joined {
		items = append(items, contextMenuItem{label: "Join again", key: 'j', run: func(m *Model) tea.Cmd {
			return m.rejoinRoom(chat)
		}})
	}
	return append(items,
		contextMenuItem{label: "Hide chat", key: 'h', run: (*Model).actionHideChat},
		contextMenuItem{label: "Info", key: 'i', run: (*Model).actionChatInfo},
		contextMenuItem{label: "Leave", key: 'l', run: (*Model).actionLeaveRoom},
	)
}

// actionChangeRoomNick asks for our new nick in the selected room.
func (m *Model) actionChangeRoomNick() tea.Cmd {
	chat, ok := m.currentChat()
	if !ok || !chat.Room {
		return nil
	}
	accountIdx, daemon := m.currentAccount, m.daemon
	return m.openModal(&modal.Modal[*Model]{
		Title: "Nick in " + chat.Name,
		Body:  m.newInput(chat.Nick, "nick", "Nick: ", "change"),
		OnSubmit: func(m *Model, v any) tea.Cmd {
			nick := strings.TrimSpace(v.(string))
			if nick == "" || nick == chat.Nick {
				return nil
			}
			return func() tea.Msg { return daemon.SetRoomNick(accountIdx, chat.Address, nick) }
		},
	})
}

// actionChangeRoomSubject asks for the selected room's new subject.
func (m *Model) actionChangeRoomSubject() tea.Cmd {
	chat, ok := m.currentChat()
	if !ok || !chat.Room {
		return nil
	}
	if !chat.Joined {
		return m.showNotification("not in the room")
	}
	accountIdx, daemon := m.currentAccount, m.daemon
	return m.openModal(&modal.Modal[*Model]{
		Title: "Subject of " + chat.Name,
		Body:  m.newInput(chat.Subject, "empty clears it", "Subject: ", "change"),
		OnSubmit: func(m *Model, v any) tea.Cmd {
			subject := strings.TrimSpace(v.(string))
			if subject == chat.Subject {
				return nil
			}
			return func() tea.Msg { return daemon.SetRoomSubject(accountIdx, chat.Address, subject) }
		},
	})
}

// actionInviteToRoom asks whom to invite to the selected room.
func (m *Model) actionInviteToRoom() tea.Cmd {
	chat, ok := m.currentChat()
	if !ok || !chat.Room {
		return nil
	}
	if !chat.Joined {
		return m.showNotification("not in the room")
	}
	accountIdx, daemon := m.currentAccount, m.daemon
	return m.openModal(&modal.Modal[*Model]{
		Title: "Invite to " + chat.Name,
		Body:  m.newInput("", "someone@server", "Invite: ", "invite"),
		OnSubmit: func(m *Model, v any) tea.Cmd {
			who := parseAddress(v.(string)).addr
			if who == "" {
				return nil
			}
			return func() tea.Msg { return daemon.InviteToRoom(accountIdx, chat.Address, who) }
		},
	})
}

// rejoinRoom enters chat's room again, as the nick we had.
func (m *Model) rejoinRoom(chat Chat) tea.Cmd {
	accountIdx, daemon := m.currentAccount, m.daemon
	return tea.Batch(
		m.showNotification("joining "+chat.Name+"…"),
		func() tea.Msg { return daemon.JoinRoom(accountIdx, chat.Address, chat.Nick, "") },
	)
}

// actionLeaveRoom leaves the selected room, once confirmed.
func (m *Model) actionLeaveRoom() tea.Cmd {
	chat, ok := m.currentChat()
	if !ok || !chat.Room {
		return nil
	}
	accountIdx, daemon := m.currentAccount, m.daemon
	return m.confirm("Leave "+chat.Name+"?", "Its bookmark goes too, on every client. History stays.",
		func(m *Model) tea.Cmd {
			return func() tea.Msg { return daemon.LeaveRoom(accountIdx, chat.Address) }
		})
}

// roomInfoLines is the info screen's content for a room.
func roomInfoLines(chat Chat) []modal.Line {
	link := "xmpp:" + chat.Address + "?join"
	lines := []modal.Line{
		{Text: "Address: " + chat.Address, Value: chat.Address},
		{Text: "Link: " + link, Value: link},
		{Text: "Nick: " + chat.Nick},
		{Text: "Status: " + roomStatus(chat)},
	}
	if chat.Subject != "" {
		lines = append(lines, modal.Line{Text: "Subject: " + chat.Subject})
	}
	encryption := "none: the room isn't private (members-only, showing members' addresses)"
	switch {
	case chat.Private && chat.EncryptionMode == "none":
		encryption = "none (turned off)"
	case chat.Private:
		encryption = "OMEMO, for every member"
	}
	lines = append(lines, modal.Line{Text: "Encryption: " + encryption})
	notifications := "on mentions"
	if chat.Muted {
		notifications = "muted"
	}
	return append(lines, modal.Line{Text: "Notifications: " + notifications})
}

// participantsBody is a room's participants screen: who's in it, picking
// one opens what can be done with them.
type participantsBody struct {
	*modal.Menu
	accountIdx int
	room       string
	occupants  []Occupant
}

func (b *participantsBody) Hints() []modal.Hint {
	return []modal.Hint{modal.KeyHint(tea.KeyPressMsg{Code: tea.KeyEnter}, "options")}
}

// openParticipants opens the selected room's participants screen.
func (m *Model) openParticipants() tea.Cmd {
	chat, ok := m.currentChat()
	if !ok || !chat.Room {
		return nil
	}
	menu := modal.NewMenu()
	menu.MatchKey = matchesLetter
	menu.ActOnPick = true
	menu.Empty = "no one's here: not in the room"
	menu.Styles.Cursor = onAccent(m.styles.colors.accentCyan)
	menu.Styles.Hover = lipgloss.NewStyle().Foreground(m.styles.colors.accentCyan)
	body := &participantsBody{Menu: menu, accountIdx: m.currentAccount, room: chat.Address}
	cmd := m.openModal(&modal.Modal[*Model]{
		ID:    participantsModalID,
		Title: chat.Name + " · Participants",
		Body:  body,
		OnAct: func(m *Model, v any) tea.Cmd {
			if i, ok := v.(int); ok && i < len(body.occupants) {
				o := body.occupants[i]
				m.openContextMenu(o.Nick, m.occupantMenuItems(body.accountIdx, o))
			}
			return nil
		},
	})
	m.refreshParticipants()
	return cmd
}

// refreshParticipants relists the open participants screen.
func (m *Model) refreshParticipants() {
	md := m.modals.Find(participantsModalID)
	if md == nil {
		return
	}
	body := md.Body.(*participantsBody)
	chat, ok := m.chatByAddress(body.accountIdx, body.room)
	if !ok {
		return
	}
	body.occupants = chat.Occupants
	items := make([]modal.MenuItem, len(chat.Occupants))
	for i, o := range chat.Occupants {
		label := presenceGlyph(o.Presence) + " " + o.Nick
		if o.Nick == chat.Nick {
			label += " (you)"
		}
		if role := occupantRank(o); role != "" {
			label += " · " + role
		}
		if o.JID != "" {
			label += " <" + o.JID + ">"
		}
		items[i] = modal.MenuItem{Label: label}
	}
	body.SetItems(items...)
}

// occupantRank is what sets o apart in the room, "" for a plain
// participant.
func occupantRank(o Occupant) string {
	switch {
	case o.Affiliation == "owner" || o.Affiliation == "admin":
		return o.Affiliation
	case o.Role == "moderator" || o.Role == "visitor":
		return o.Role
	}
	return ""
}

// occupantMenuItems is what can be done with occupant o: with their real
// JID known, chat with them and copy it.
func (m *Model) occupantMenuItems(accountIdx int, o Occupant) []contextMenuItem {
	mention := contextMenuItem{label: "Mention", key: 'm', run: func(m *Model) tea.Cmd {
		m.modals.Remove(participantsModalID)
		m.input.InsertString(o.Nick + ": ")
		return m.enterChatPane()
	}}
	// Through the room: the room's service relays it, no one else sees it.
	room := m.participantsRoom()
	private := contextMenuItem{label: "Private message", key: 'p', run: func(m *Model) tea.Cmd {
		occupant := room + "/" + o.Nick
		if m.chatIndexByAddress(accountIdx, occupant) >= 0 {
			return m.openChatByAddress(accountIdx, occupant)
		}
		daemon := m.daemon
		return func() tea.Msg { return daemon.StartChat(accountIdx, occupant) }
	}}
	moderation := m.moderationItems(accountIdx, room, o)
	if o.JID == "" {
		return append([]contextMenuItem{private, mention}, moderation...)
	}
	daemon, addr := m.daemon, o.JID
	return append([]contextMenuItem{
		private,
		{label: "Message", key: 'c', run: func(m *Model) tea.Cmd {
			if m.chatIndexByAddress(accountIdx, addr) >= 0 {
				return m.openChatByAddress(accountIdx, addr)
			}
			return func() tea.Msg { return daemon.StartChat(accountIdx, addr) }
		}},
		mention,
		{label: "Copy address", key: 'y', run: func(m *Model) tea.Cmd {
			if err := clipboard.WriteAll(addr); err != nil {
				return m.showNotification("copy failed")
			}
			return m.showNotification("address copied")
		}},
	}, moderation...)
}

// ownOccupant is us among chat's occupants, zero while not in the room.
func ownOccupant(chat Chat) Occupant {
	for _, o := range chat.Occupants {
		if o.Nick == chat.Nick {
			return o
		}
	}
	return Occupant{}
}

// affiliationRank orders affiliations by what they may do to each other.
var affiliationRank = map[string]int{"owner": 3, "admin": 2, "member": 1}

// moderationItems is what we may do to occupant o of room as its
// moderator or admin (XEP-0045 §8, §9): nothing to ourselves, nor to an
// admin or owner as a mere moderator.
func (m *Model) moderationItems(accountIdx int, room string, o Occupant) []contextMenuItem {
	chat, ok := m.chatByAddress(accountIdx, room)
	if !ok || !chat.Joined || o.Nick == chat.Nick {
		return nil
	}
	me := ownOccupant(chat)
	daemon, nick := m.daemon, o.Nick
	act := func(action string) func(*Model) tea.Cmd {
		return func(*Model) tea.Cmd {
			return func() tea.Msg { return daemon.ModerateOccupant(accountIdx, room, nick, action) }
		}
	}
	confirmed := func(title, detail, action string) func(*Model) tea.Cmd {
		return func(m *Model) tea.Cmd { return m.confirm(title, detail, act(action)) }
	}
	privileged := affiliationRank[o.Affiliation] >= affiliationRank["admin"]
	var items []contextMenuItem
	if me.Role == "moderator" && !privileged {
		switch o.Role {
		case "visitor":
			items = append(items, contextMenuItem{label: "Give voice", key: 'v', run: act(ModVoice)})
		case "participant":
			items = append(items, contextMenuItem{label: "Take voice", key: 'v', run: act(ModRevokeVoice)})
		}
		items = append(items, contextMenuItem{label: "Kick", key: 'k', run: confirmed("Kick "+nick+"?", "They can join again.", ModKick)})
	}
	if affiliationRank[me.Affiliation] >= affiliationRank["admin"] && affiliationRank[o.Affiliation] < affiliationRank[me.Affiliation] {
		switch {
		case o.Role == "moderator" && !privileged:
			items = append(items, contextMenuItem{label: "Remove moderator", key: 'o', run: act(ModUnmoderator)})
		case o.Role != "moderator":
			items = append(items, contextMenuItem{label: "Make moderator", key: 'o', run: act(ModModerator)})
		}
		if o.JID != "" {
			items = append(items, contextMenuItem{label: "Ban", key: 'b', run: confirmed("Ban "+nick+"?", "Their address can't join again until unbanned.", ModBan)})
		}
	}
	return items
}

// participantsRoom is the room the participants screen is open for, ""
// with none open.
func (m Model) participantsRoom() string {
	if md := m.modals.Find(participantsModalID); md != nil {
		return md.Body.(*participantsBody).room
	}
	return ""
}

// privateChatMenuItems is the chat menu for a private chat in a room.
func (m *Model) privateChatMenuItems(chat Chat) []contextMenuItem {
	pin, mute := "Pin chat", "Mute notifications"
	if chat.Pinned {
		pin = "Unpin chat"
	}
	if chat.Muted {
		mute = "Unmute notifications"
	}
	return []contextMenuItem{
		{label: pin, key: 'p', run: (*Model).actionTogglePinChat},
		{label: mute, key: 'm', run: (*Model).actionToggleMuteChat},
		{label: "Hide chat", key: 'h', run: (*Model).actionHideChat},
		{label: "Info", key: 'i', run: (*Model).actionChatInfo},
	}
}

// privateStatus is a private chat's state for the status bar: whether the
// other is still in the room, through which it goes.
func (m Model) privateStatus(chat Chat) string {
	room, ok := m.chatByAddress(m.currentAccount, chat.PrivateRoom)
	nick := strings.TrimPrefix(chat.Address, chat.PrivateRoom+"/")
	switch {
	case !ok:
		return "private"
	case !room.Joined:
		return "private · not in the room"
	case !slices.ContainsFunc(room.Occupants, func(o Occupant) bool { return o.Nick == nick }):
		return "private · " + nick + " left the room"
	}
	return "private · through " + room.Name
}

// privateInfoLines is the info screen's content for a private chat.
func (m Model) privateInfoLines(chat Chat) []modal.Line {
	return []modal.Line{
		{Text: "Address: " + chat.Address, Value: chat.Address},
		{Text: "Status: " + m.privateStatus(chat)},
		{Text: "Encryption: none (the room's service relays it, and can read it)"},
	}
}

// chatByAddress is accountIdx's chat with address, hidden or not.
func (m Model) chatByAddress(accountIdx int, address string) (Chat, bool) {
	if idx := m.chatIndexByAddress(accountIdx, address); idx >= 0 {
		chat, ok := m.accounts[accountIdx].Chats[idx].(Chat)
		return chat, ok
	}
	if h, ok := m.hiddenChats[accountIdx][address]; ok {
		return h.chat, true
	}
	return Chat{}, false
}

// openChatByAddress closes the screens open and opens accountIdx's chat
// with address.
func (m *Model) openChatByAddress(accountIdx int, address string) tea.Cmd {
	for m.modals.Open() {
		m.modals.Close()
	}
	var cmds []tea.Cmd
	if accountIdx != m.currentAccount {
		cmds = append(cmds, m.switchAccount(accountIdx))
	}
	if m.isChatHidden(accountIdx, address) {
		cmds = append(cmds, m.unhideChat(accountIdx, address))
	}
	// The daemon's ChatAddedMsg may come after its answer: the row is made
	// here, and that upserts it.
	if m.chatIndexByAddress(accountIdx, address) < 0 && accountIdx == m.currentAccount {
		m.accounts[accountIdx].Chats = append(m.accounts[accountIdx].Chats, Chat{Name: address, Address: address})
		cmds = append(cmds, m.chats.SetItems(m.accounts[accountIdx].Chats))
	}
	for i, item := range m.chats.Items() {
		if chat, ok := item.(Chat); ok && chat.Address == address {
			m.chats.Select(i)
			model, cmd := m.openCurrentChat()
			*m = model.(Model)
			return tea.Batch(append(cmds, cmd)...)
		}
	}
	return tea.Batch(cmds...)
}

// updateChatEverywhere applies fn to accountIdx's chat with address, listed
// or hidden, reporting whether there was one.
func (m *Model) updateChatEverywhere(accountIdx int, address string, fn func(*Chat)) (tea.Cmd, bool) {
	if accountIdx < 0 || accountIdx >= len(m.accounts) {
		return nil, false
	}
	if idx := m.chatIndexByAddress(accountIdx, address); idx >= 0 {
		chat, ok := m.accounts[accountIdx].Chats[idx].(Chat)
		if !ok {
			return nil, false
		}
		fn(&chat)
		m.accounts[accountIdx].Chats[idx] = chat
		if accountIdx == m.currentAccount {
			return m.chats.SetItem(idx, chat), true
		}
		return nil, true
	}
	if h, ok := m.hiddenChats[accountIdx][address]; ok {
		fn(&h.chat)
		m.hiddenChats[accountIdx][address] = h
		return nil, true
	}
	return nil, false
}

// handleRoomMsg handles the daemon's room messages; handled is false for
// any other.
func (m Model) handleRoomMsg(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case RoomStateMsg:
		renamed := false
		cmd, _ := m.updateChatEverywhere(msg.AccountIdx, msg.Room, func(c *Chat) {
			renamed = c.Nick != msg.Nick
			c.Nick, c.Subject, c.Joined, c.JoinError, c.Occupants = msg.Nick, msg.Subject, msg.Joined, msg.JoinError, msg.Occupants
			c.Private, c.EncryptionMode = msg.Private, msg.EncryptionMode
			// Who left isn't typing anymore.
			c.TypingNicks = slices.DeleteFunc(slices.Clone(c.TypingNicks), func(nick string) bool {
				return !slices.ContainsFunc(msg.Occupants, func(o Occupant) bool { return o.Nick == nick })
			})
		})
		cmds := []tea.Cmd{cmd}
		// What mentions us is drawn by our nick.
		if chat, ok := m.currentChat(); renamed && ok && msg.AccountIdx == m.currentAccount && chat.Address == msg.Room {
			m.refreshViewport()
		}
		// A private chat's other is present while they're in the room.
		presence := make(map[string]Presence, len(msg.Occupants))
		for _, o := range msg.Occupants {
			presence[msg.Room+"/"+o.Nick] = o.Presence
		}
		if msg.AccountIdx >= 0 && msg.AccountIdx < len(m.accounts) {
			for _, item := range m.accounts[msg.AccountIdx].Chats {
				if c, ok := item.(Chat); ok && c.PrivateRoom == msg.Room && c.Presence != presence[c.Address] {
					cmd, _ := m.updateChatEverywhere(msg.AccountIdx, c.Address, func(c *Chat) { c.Presence = presence[c.Address] })
					cmds = append(cmds, cmd)
				}
			}
		}
		m.refreshParticipants()
		return m, tea.Batch(cmds...), true

	case RoomJoinedMsg:
		if md := m.modals.Find(joinRoomModalID); md != nil {
			form := md.Body.(*modal.Form)
			form.SetBusy("")
			if msg.Err != nil {
				form.SetError(msg.Err.Error())
				return m, nil, true
			}
			m.modals.Remove(joinRoomModalID)
			return m, m.openChatByAddress(msg.AccountIdx, msg.Room), true
		}
		if msg.Err != nil {
			return m, m.showNotification("joining " + msg.Room + ": " + msg.Err.Error()), true
		}
		return m, nil, true

	case RoomNickSetMsg:
		if msg.Err != nil {
			return m, m.showNotification("changing nick: " + msg.Err.Error()), true
		}
		return m, m.showNotification("you're " + msg.Nick + " in " + msg.Room), true

	case RoomSubjectSetMsg:
		if msg.Err != nil {
			return m, m.showNotification("changing the subject: " + msg.Err.Error()), true
		}
		return m, nil, true

	case OccupantModeratedMsg:
		if msg.Err != nil {
			return m, m.showNotification(strings.ReplaceAll(msg.Action, "-", " ") + " " + msg.Nick + ": " + msg.Err.Error()), true
		}
		return m, nil, true

	case RoomCreatedMsg:
		return m, m.handleRoomCreated(msg), true

	case RoomSettingsMsg:
		if msg.Err != nil {
			return m, m.showNotification("reading the room's settings: " + msg.Err.Error()), true
		}
		return m, m.openRoomSettings(msg.AccountIdx, msg.Room, msg.Settings), true

	case RoomSettingsSavedMsg:
		md := m.modals.Find(roomSettingsModalID)
		if md == nil {
			if msg.Err != nil {
				return m, m.showNotification("saving the room's settings: " + msg.Err.Error()), true
			}
			return m, nil, true
		}
		form := md.Body.(*modal.Form)
		form.SetBusy("")
		if msg.Err != nil {
			form.SetError(msg.Err.Error())
			return m, nil, true
		}
		m.modals.Remove(roomSettingsModalID)
		return m, m.showNotification("settings saved"), true

	case RoomInviteSentMsg:
		if msg.Err != nil {
			return m, m.showNotification("inviting " + msg.Who + ": " + msg.Err.Error()), true
		}
		return m, m.showNotification("invited " + msg.Who), true

	case RoomLeftMsg:
		if msg.Err != nil {
			return m, m.showNotification("leaving " + msg.Room + ": " + msg.Err.Error()), true
		}
		return m, m.showNotification("left " + msg.Room), true

	case RoomInviteMsg:
		if msg.AccountIdx != m.currentAccount {
			return m, m.showNotification(msg.From + " invited you to " + msg.Room), true
		}
		detail := msg.From + " invites you to " + msg.Room
		if msg.Reason != "" {
			detail += ": " + msg.Reason
		}
		room, password := msg.Room, msg.Password
		return m, m.confirm("Join group chat?", detail, func(m *Model) tea.Cmd {
			return m.openJoinRoom(room, password)
		}), true

	case ChatStartedMsg:
		if msg.Err != nil {
			return m, m.showNotification("starting a chat with " + msg.Address + ": " + msg.Err.Error()), true
		}
		return m, m.openChatByAddress(msg.AccountIdx, msg.Address), true
	}
	return m, nil, false
}
