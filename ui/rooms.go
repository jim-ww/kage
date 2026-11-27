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
		{label: pin, key: 'p', run: (*Model).actionTogglePinChat},
		{label: mute, key: 'm', run: (*Model).actionToggleMuteChat},
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
	lines := []modal.Line{
		{Text: "Address: " + chat.Address, Value: chat.Address},
		{Text: "Nick: " + chat.Nick},
		{Text: "Status: " + roomStatus(chat)},
	}
	if chat.Subject != "" {
		lines = append(lines, modal.Line{Text: "Subject: " + chat.Subject})
	}
	lines = append(lines, modal.Line{Text: "Encryption: none (group chats are plaintext)"})
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
	if o.JID == "" {
		return []contextMenuItem{private, mention}
	}
	daemon, addr := m.daemon, o.JID
	return []contextMenuItem{
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
	}
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
		cmd, _ := m.updateChatEverywhere(msg.AccountIdx, msg.Room, func(c *Chat) {
			c.Nick, c.Subject, c.Joined, c.JoinError, c.Occupants = msg.Nick, msg.Subject, msg.Joined, msg.JoinError, msg.Occupants
		})
		cmds := []tea.Cmd{cmd}
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
