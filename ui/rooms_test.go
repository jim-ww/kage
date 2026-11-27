package ui

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"

	"github.com/jim-ww/kage/ui/modal"
)

type fakeRoomDaemon struct {
	fakeSuccessSender
	joins    []string
	starts   []string
	nicks    []string
	subjects []string
	invites  []string
	mods     []string
	saved    []map[string]string
	settings []RoomSetting
	joinErr  error
}

func (f *fakeRoomDaemon) JoinRoom(accountIdx int, room, nick, password string) tea.Msg {
	f.joins = append(f.joins, strings.TrimSpace(room+" as "+nick+" "+password))
	return RoomJoinedMsg{AccountIdx: accountIdx, Room: room, Err: f.joinErr}
}

func (f *fakeRoomDaemon) SetRoomNick(accountIdx int, room, nick string) tea.Msg {
	f.nicks = append(f.nicks, room+" "+nick)
	return RoomNickSetMsg{AccountIdx: accountIdx, Room: room, Nick: nick}
}

func (f *fakeRoomDaemon) SetRoomSubject(accountIdx int, room, subject string) tea.Msg {
	f.subjects = append(f.subjects, room+" "+subject)
	return RoomSubjectSetMsg{AccountIdx: accountIdx, Room: room}
}

func (f *fakeRoomDaemon) InviteToRoom(accountIdx int, room, who string) tea.Msg {
	f.invites = append(f.invites, room+" "+who)
	return RoomInviteSentMsg{AccountIdx: accountIdx, Room: room, Who: who}
}

func (f *fakeRoomDaemon) ModerateOccupant(accountIdx int, room, nick, action string) tea.Msg {
	f.mods = append(f.mods, action+" "+nick)
	return OccupantModeratedMsg{AccountIdx: accountIdx, Room: room, Nick: nick, Action: action}
}

func (f *fakeRoomDaemon) RoomSettings(accountIdx int, room string) tea.Msg {
	return RoomSettingsMsg{AccountIdx: accountIdx, Room: room, Settings: f.settings}
}

func (f *fakeRoomDaemon) SaveRoomSettings(accountIdx int, room string, values map[string]string) tea.Msg {
	f.saved = append(f.saved, values)
	return RoomSettingsSavedMsg{AccountIdx: accountIdx, Room: room}
}

func (f *fakeRoomDaemon) StartChat(accountIdx int, address string) tea.Msg {
	f.starts = append(f.starts, address)
	return ChatStartedMsg{AccountIdx: accountIdx, Address: address}
}

const testRoomAddr = "room@muc.example.test"

func roomModel(t *testing.T, d Daemon) Model {
	t.Helper()
	m := newTestModelWithSender(d, nil)
	room := Chat{Name: "Room", Address: testRoomAddr, Room: true, Nick: "me", Joined: true,
		Occupants: []Occupant{{Nick: "bob", JID: "bob@example.test"}, {Nick: "me"}}}
	m.accounts = []Account{{Name: "me@example.test", Chats: []list.Item{room}, Messages: map[int][]Message{}}}
	m.chats.SetItems(m.accounts[0].Chats)
	m.chats.Select(0)
	m.selectedView = viewChats
	return m
}

// TestRoomChatMenu checks a room's chat menu has what a room has, and
// none of what only a contact has.
func TestRoomChatMenu(t *testing.T) {
	m := press(t, roomModel(t, &fakeRoomDaemon{}), letter('m'))
	labels := strings.Join(callMenuLabels(m), "|")
	for _, want := range []string{"Participants", "Leave", "Info"} {
		if !strings.Contains(labels, want) {
			t.Errorf("room menu %q lacks %q", labels, want)
		}
	}
	for _, unwanted := range []string{"Rename", "Block", "Re-sync"} {
		if strings.Contains(labels, unwanted) {
			t.Errorf("room menu %q has %q", labels, unwanted)
		}
	}
}

func TestRoomNotCallable(t *testing.T) {
	m := roomModel(t, &fakeRoomDaemon{})
	title, items := m.callMenuItems()
	for _, it := range items {
		if strings.Contains(it.label, "call") {
			t.Errorf("call menu %q offers %q for a room", title, it.label)
		}
	}
}

// TestRoomStateMsg checks the daemon's room state lands on the chat, and
// the glyph shows it.
func TestRoomStateMsg(t *testing.T) {
	m := roomModel(t, &fakeRoomDaemon{})
	m = updated(t, m, RoomStateMsg{AccountIdx: 0, Room: testRoomAddr, Nick: "me", JoinError: "kicked from the room"})
	chat := m.accounts[0].Chats[0].(Chat)
	if chat.Joined || chat.JoinError != "kicked from the room" || len(chat.Occupants) != 0 {
		t.Fatalf("chat = %+v", chat)
	}
	if !strings.Contains(chat.Title(), "#") {
		t.Errorf("title = %q", chat.Title())
	}
	if got := m.chatStatusLabel(chat); !strings.Contains(got, "not in the room: kicked") {
		t.Errorf("status = %q", got)
	}
	menu := strings.Join(callMenuLabels(press(t, m, letter('m'))), "|")
	if !strings.Contains(menu, "Join again") {
		t.Errorf("menu of a room we're out of: %q", menu)
	}
}

// TestJoinRoomForm checks the join form joins as the account's localpart
// by default, and opens the room once in.
func TestJoinRoomForm(t *testing.T) {
	d := &fakeRoomDaemon{}
	m := newTestModelWithSender(d, nil)
	m.accounts = []Account{{Name: "me@example.test", Messages: map[int][]Message{}}}
	m.openJoinRoom("", "")
	for _, r := range testRoomAddr {
		m = press(t, m, letter(r))
	}
	m = pressAndDeliver(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = pressAndDeliver(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = pressAndDeliver(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !slices.Equal(d.joins, []string{testRoomAddr + " as me"}) {
		t.Fatalf("joins = %v", d.joins)
	}
	if m.modals.Find(joinRoomModalID) != nil {
		t.Error("join form still open after joining")
	}
	if chat, ok := m.currentChat(); !ok || chat.Address != testRoomAddr {
		t.Errorf("current chat = %+v", chat)
	}
}

func TestJoinRoomFormShowsRefusal(t *testing.T) {
	d := &fakeRoomDaemon{joinErr: errors.New("the room is for members only")}
	m := newTestModelWithSender(d, nil)
	m.accounts = []Account{{Name: "me@example.test", Messages: map[int][]Message{}}}
	m.openJoinRoom(testRoomAddr, "")
	for range 3 {
		m = pressAndDeliver(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	}
	md := m.modals.Find(joinRoomModalID)
	if md == nil {
		t.Fatal("join form closed on a refusal")
	}
	if view := md.Body.(*modal.Form).View(); !strings.Contains(view, "members only") {
		t.Errorf("form = %q", view)
	}
}

// TestMessageParticipant checks a participant with a known address can be
// messaged from the participants screen.
func TestMessageParticipant(t *testing.T) {
	d := &fakeRoomDaemon{}
	m := roomModel(t, d)
	m.openParticipants()
	body := m.modals.Find(participantsModalID).Body.(*participantsBody)
	if len(body.occupants) != 2 || body.occupants[0].Nick != "bob" {
		t.Fatalf("participants = %+v", body.occupants)
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = pressAndDeliver(t, m, letter('c'))
	if !slices.Equal(d.starts, []string{"bob@example.test"}) {
		t.Fatalf("started chats = %v", d.starts)
	}
	if chat, ok := m.currentChat(); !ok || chat.Address != "bob@example.test" {
		t.Errorf("current chat = %+v", chat)
	}
}

// TestChatRemovedLeavesNoStalePane checks the chat on screen going away
// (a room left) shows the next one, not the gone one's messages.
func TestChatRemovedLeavesNoStalePane(t *testing.T) {
	m := roomModel(t, &fakeRoomDaemon{})
	m.accounts[0].Chats = append(m.accounts[0].Chats, Chat{Name: "Bob", Address: "bob@example.test"})
	m.accounts[0].Messages[0] = []Message{{ID: "r1", Author: "bob", Content: "in the room"}}
	m.chats.SetItems(m.accounts[0].Chats)
	m.chats.Select(0)
	m = updated(t, m, ChatRemovedMsg{AccountIdx: 0, Address: testRoomAddr})
	if chat, ok := m.currentChat(); !ok || chat.Address != "bob@example.test" {
		t.Fatalf("current chat = %+v", chat)
	}
	if msgs := m.currentMessages(); len(msgs) != 0 {
		t.Errorf("messages shown = %+v", msgs)
	}
}

// TestJoinRoomFormTakesALink checks a pasted xmpp: link joins its room,
// with the password it carries.
func TestJoinRoomFormTakesALink(t *testing.T) {
	d := &fakeRoomDaemon{}
	m := newTestModelWithSender(d, nil)
	m.accounts = []Account{{Name: "me@example.test", Messages: map[int][]Message{}}}
	m.openJoinRoom("xmpp:"+testRoomAddr+"?join;password=pw", "")
	for range 3 {
		m = pressAndDeliver(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	}
	if !slices.Equal(d.joins, []string{testRoomAddr + " as me pw"}) {
		t.Fatalf("joins = %v", d.joins)
	}
}

// TestPrivateMessageParticipant checks a participant can be messaged
// privately through the room, even with their address hidden.
func TestPrivateMessageParticipant(t *testing.T) {
	d := &fakeRoomDaemon{}
	m := roomModel(t, d)
	m.updateChatEverywhere(0, testRoomAddr, func(c *Chat) { c.Occupants = []Occupant{{Nick: "anon"}, {Nick: "me"}} })
	m.openParticipants()
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = pressAndDeliver(t, m, letter('p'))
	if !slices.Equal(d.starts, []string{testRoomAddr + "/anon"}) {
		t.Fatalf("started chats = %v", d.starts)
	}
	chat, ok := m.currentChat()
	if !ok || chat.Address != testRoomAddr+"/anon" {
		t.Fatalf("current chat = %+v", chat)
	}
	// The daemon's row comes after its answer.
	m = updated(t, m, ChatAddedMsg{AccountIdx: 0, Chat: Chat{Name: "anon · Room", Address: testRoomAddr + "/anon", PrivateRoom: testRoomAddr}})
	if c, _ := m.chatByAddress(0, testRoomAddr+"/anon"); c.PrivateRoom != testRoomAddr || c.Name != "anon · Room" {
		t.Fatalf("private chat = %+v", c)
	}
	m = updated(t, m, RoomStateMsg{AccountIdx: 0, Room: testRoomAddr, Joined: true, Occupants: []Occupant{{Nick: "anon", Presence: PresenceAway}}})
	if c, _ := m.chatByAddress(0, testRoomAddr+"/anon"); c.Presence != PresenceAway {
		t.Errorf("private chat presence = %v", c.Presence)
	}
}

func TestChangeRoomNick(t *testing.T) {
	d := &fakeRoomDaemon{}
	m := roomModel(t, d)
	m = press(t, m, letter('m'))
	m = press(t, m, letter('n'))
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	for _, r := range "neo" {
		m = press(t, m, letter(r))
	}
	pressAndDeliver(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !slices.Equal(d.nicks, []string{testRoomAddr + " neo"}) {
		t.Errorf("nick changes = %v", d.nicks)
	}
}

func TestMentions(t *testing.T) {
	for body, want := range map[string]bool{
		"hey Alice, look": true,
		"alice":           true,
		"@alice:":         true,
		"malice":          false,
		"alicebob":        false,
		"nothing here":    false,
	} {
		if got := Mentions(body, "alice"); got != want {
			t.Errorf("Mentions(%q) = %v, want %v", body, got, want)
		}
	}
}

// TestMentionHighlighted checks a room message naming our nick, or
// replying to us, is told apart from the rest.
func TestMentionHighlighted(t *testing.T) {
	m := roomModel(t, &fakeRoomDaemon{})
	msgs := []Message{
		{ID: "a", Author: "bob", Content: "hi all"},
		{ID: "b", Author: "bob", Content: "me: look"},
		{ID: "c", Author: "me", Content: "mine", IsMe: true},
		{ID: "d", Author: "bob", Content: "sure", ReplyToID: "c"},
	}
	for i, want := range []bool{false, true, false, true} {
		if got := m.isForUs(msgs[i], messageIndexByID(msgs, msgs[i].ReplyToID), msgs); got != want {
			t.Errorf("isForUs(%q) = %v, want %v", msgs[i].Content, got, want)
		}
	}
}

// TestNickTabComplete checks tab completes a nick in the room, cycling
// through the matches, addressing them at the start of a message.
func TestNickTabComplete(t *testing.T) {
	m := roomModel(t, &fakeRoomDaemon{})
	m.updateChatEverywhere(0, testRoomAddr, func(c *Chat) {
		c.Occupants = []Occupant{{Nick: "bob"}, {Nick: "Bobby"}, {Nick: "me"}, {Nick: "carol"}}
	})
	m.accounts[0].Messages[0] = []Message{{ID: "1", Author: "Bobby", Content: "hi"}}
	m.enterChatPane()
	tab := tea.KeyPressMsg{Code: tea.KeyTab}
	for _, r := range "bo" {
		m = press(t, m, letter(r))
	}
	m = press(t, m, tab)
	if got := m.composeValue(); got != "Bobby: " {
		t.Fatalf("after tab: %q", got)
	}
	m = press(t, m, tab)
	if got := m.composeValue(); got != "bob: " {
		t.Fatalf("after second tab: %q", got)
	}
	for _, r := range "see @c" {
		m = press(t, m, letter(r))
	}
	m = press(t, m, tab)
	if got := m.composeValue(); got != "bob: see @carol " {
		t.Fatalf("mid-message: %q", got)
	}
	m = press(t, m, letter('m'))
	m = press(t, m, tab)
	if got := m.composeValue(); got != "bob: see @carol m" {
		t.Errorf("our own nick completed: %q", got)
	}
}

func TestChangeRoomSubject(t *testing.T) {
	d := &fakeRoomDaemon{}
	m := roomModel(t, d)
	m = press(t, m, letter('m'))
	m = press(t, m, letter('s'))
	for _, r := range "news" {
		m = press(t, m, letter(r))
	}
	pressAndDeliver(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !slices.Equal(d.subjects, []string{testRoomAddr + " news"}) {
		t.Errorf("subject changes = %v", d.subjects)
	}
}

func TestInviteToRoom(t *testing.T) {
	d := &fakeRoomDaemon{}
	m := roomModel(t, d)
	m = press(t, m, letter('m'))
	m = press(t, m, letter('v'))
	for _, r := range "xmpp:carol@example.test" {
		m = press(t, m, letter(r))
	}
	pressAndDeliver(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !slices.Equal(d.invites, []string{testRoomAddr + " carol@example.test"}) {
		t.Errorf("invites = %v", d.invites)
	}
}

// TestRoomTyping checks who's typing in a room shows by nick, and stops
// with their message, their leaving, or the timeout.
func TestRoomTyping(t *testing.T) {
	m := roomModel(t, &fakeRoomDaemon{})
	m.updateChatEverywhere(0, testRoomAddr, func(c *Chat) {
		c.Occupants = []Occupant{{Nick: "bob"}, {Nick: "carol"}, {Nick: "me"}}
	})
	typing := func() []string { c, _ := m.chatByAddress(0, testRoomAddr); return c.TypingNicks }
	m = updated(t, m, TypingMsg{AccountIdx: 0, From: testRoomAddr, Nick: "bob", Typing: true})
	m = updated(t, m, TypingMsg{AccountIdx: 0, From: testRoomAddr, Nick: "carol", Typing: true})
	if got := typing(); !slices.Equal(got, []string{"bob", "carol"}) {
		t.Fatalf("typing = %v", got)
	}
	chat, _ := m.currentChat()
	if label := m.chatStatusLabel(chat); !strings.Contains(label, "bob, carol typing") {
		t.Errorf("status = %q", label)
	}
	m = updated(t, m, IncomingMessageMsg{AccountIdx: 0, From: testRoomAddr, Message: Message{ID: "x", Author: "bob", Content: "hi"}})
	if got := typing(); !slices.Equal(got, []string{"carol"}) {
		t.Fatalf("typing after bob's message = %v", got)
	}
	m = updated(t, m, RoomStateMsg{AccountIdx: 0, Room: testRoomAddr, Nick: "me", Joined: true, Occupants: []Occupant{{Nick: "bob"}, {Nick: "me"}}})
	if got := typing(); len(got) != 0 {
		t.Fatalf("typing after carol left = %v", got)
	}
	m = updated(t, m, TypingMsg{AccountIdx: 0, From: testRoomAddr, Nick: "bob", Typing: true})
	gen := m.peerTypingGen[peerTypingKey(0, testRoomAddr, "bob")]
	m = updated(t, m, peerTypingExpiredMsg{accountIdx: 0, from: testRoomAddr, nick: "bob", gen: gen})
	if got := typing(); len(got) != 0 {
		t.Errorf("typing after the timeout = %v", got)
	}
}

// TestModerationItems checks the participant menu offers what our role
// and affiliation allow over theirs, and nothing over ourselves.
func TestModerationItems(t *testing.T) {
	bob := Occupant{Nick: "bob", JID: "bob@example.test", Affiliation: "none", Role: "participant"}
	admin := Occupant{Nick: "ann", JID: "ann@example.test", Affiliation: "admin", Role: "moderator"}
	for _, tc := range []struct {
		name   string
		me     Occupant
		target Occupant
		want   []string
	}{
		{"participant", Occupant{Nick: "me", Role: "participant"}, bob, nil},
		{"moderator", Occupant{Nick: "me", Role: "moderator"}, bob, []string{"Take voice", "Kick"}},
		{"moderator on an admin", Occupant{Nick: "me", Role: "moderator"}, admin, nil},
		{"admin", Occupant{Nick: "me", Role: "moderator", Affiliation: "admin"}, bob, []string{"Take voice", "Kick", "Make moderator", "Ban"}},
		{"admin on an admin", Occupant{Nick: "me", Role: "moderator", Affiliation: "admin"}, admin, nil},
		{"owner on an admin", Occupant{Nick: "me", Role: "moderator", Affiliation: "owner"}, admin, []string{"Ban"}},
		{"ourselves", Occupant{Nick: "me", Role: "moderator", Affiliation: "owner"}, Occupant{Nick: "me", Role: "moderator", Affiliation: "owner"}, nil},
	} {
		m := roomModel(t, &fakeRoomDaemon{})
		m.updateChatEverywhere(0, testRoomAddr, func(c *Chat) { c.Occupants = []Occupant{tc.me, tc.target} })
		var got []string
		for _, it := range m.moderationItems(0, testRoomAddr, tc.target) {
			got = append(got, it.label)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestKickConfirmed(t *testing.T) {
	d := &fakeRoomDaemon{}
	m := roomModel(t, d)
	m.updateChatEverywhere(0, testRoomAddr, func(c *Chat) {
		c.Occupants = []Occupant{{Nick: "bob", Role: "participant"}, {Nick: "me", Role: "moderator"}}
	})
	m.openParticipants()
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = press(t, m, letter('k'))
	if len(d.mods) != 0 {
		t.Fatalf("kicked unconfirmed: %v", d.mods)
	}
	pressAndDeliver(t, m, letter('y'))
	if !slices.Equal(d.mods, []string{ModKick + " bob"}) {
		t.Errorf("moderations = %v", d.mods)
	}
}

// TestRoomSettings checks an owner's settings screen shows the settings
// kage knows, in its order, and saves only what changed.
func TestRoomSettings(t *testing.T) {
	d := &fakeRoomDaemon{settings: []RoomSetting{
		{Var: "muc#roomconfig_membersonly", Kind: "bool", Label: "Make room members-only?", Value: "0"},
		{Var: "muc#roomconfig_roomname", Kind: "text", Label: "Title", Value: "Tea"},
		{Var: "muc#roomconfig_whois", Kind: "choice", Value: "moderators", Options: []RoomSettingOption{{"moderators", "Moderators"}, {"anyone", "Anyone"}}},
		{Var: "muc#roomconfig_roomsecret", Kind: "secret"},
		{Var: "muc#roomconfig_passwordprotectedroom", Kind: "bool", Value: "0"},
		{Var: "{http://prosody.im/protocol/muc}roomconfig_allowmemberinvites", Kind: "bool", Value: "0"},
		{Var: "x-server#unknown", Kind: "bool", Value: "1"},
	}}
	m := roomModel(t, d)
	if labels := strings.Join(callMenuLabels(press(t, m, letter('m'))), "|"); strings.Contains(labels, "Settings") {
		t.Errorf("a participant's menu offers Settings: %q", labels)
	}
	m.updateChatEverywhere(0, testRoomAddr, func(c *Chat) { c.Occupants = []Occupant{{Nick: "me", Affiliation: "owner", Role: "moderator"}} })
	m = press(t, m, letter('m'))
	m = pressAndDeliver(t, m, letter('t'))
	md := m.modals.Find(roomSettingsModalID)
	if md == nil {
		t.Fatal("settings screen not open")
	}
	form := md.Body.(*modal.Form)
	if len(form.Fields) != 5 {
		t.Fatalf("fields = %d, want name, password, members only, addresses, invites", len(form.Fields))
	}
	// Name: typed onto; password; members only: on; addresses: anyone.
	for _, r := range "house" {
		m = press(t, m, letter(r))
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	for _, r := range "pw" {
		m = press(t, m, letter(r))
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	m = pressAndDeliver(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	want := map[string]string{
		"muc#roomconfig_roomname":              "Teahouse",
		"muc#roomconfig_roomsecret":            "pw",
		"muc#roomconfig_passwordprotectedroom": "1",
		"muc#roomconfig_membersonly":           "1",
		"muc#roomconfig_whois":                 "anyone",
	}
	if len(d.saved) != 1 || !maps.Equal(d.saved[0], want) {
		t.Fatalf("saved %v, want %v", d.saved, want)
	}
	if m.modals.Find(roomSettingsModalID) != nil {
		t.Error("settings screen still open after saving")
	}
}

type fakeCreateDaemon struct {
	fakeRoomDaemon
	created []string
}

func (f *fakeCreateDaemon) CreateRoom(accountIdx int, name, localpart string, public bool, invitees []string) tea.Msg {
	f.created = append(f.created, fmt.Sprintf("%s|%s|%v|%v", name, localpart, public, invitees))
	return RoomCreatedMsg{AccountIdx: accountIdx, Room: "new@muc.example.test"}
}

// TestCreateRoomForm checks the new group chat form makes a private group
// by default, inviting the contacts ticked, and opens it.
func TestCreateRoomForm(t *testing.T) {
	d := &fakeCreateDaemon{}
	m := newTestModelWithSender(d, nil)
	m.accounts = []Account{{Name: "me@example.test", Messages: map[int][]Message{}, Chats: []list.Item{
		Chat{Name: "Zed", Address: "zed@example.test"},
		Chat{Name: "Bob", Address: "bob@example.test"},
		Chat{Name: "Room", Address: testRoomAddr, Room: true},
	}}}
	m.chats.SetItems(m.accounts[0].Chats)
	m.openCreateRoom()
	form := m.modals.Find(createRoomModalID).Body.(*modal.Form)
	if len(form.Fields) != 5 {
		t.Fatalf("fields = %d: name, kind, address, then Bob and Zed", len(form.Fields))
	}
	m = pressAndDeliver(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if form.Err() == "" || len(d.created) != 0 {
		t.Fatalf("created without a name: %v", d.created)
	}
	for _, r := range "Tea" {
		m = press(t, m, letter(r))
	}
	for range 3 {
		m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}) // Bob
	m = pressAndDeliver(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !slices.Equal(d.created, []string{"Tea||false|[bob@example.test]"}) {
		t.Fatalf("created %v", d.created)
	}
	if m.modals.Find(createRoomModalID) != nil {
		t.Error("form still open")
	}
	if chat, ok := m.currentChat(); !ok || chat.Address != "new@muc.example.test" {
		t.Errorf("current chat = %+v", chat)
	}
}
