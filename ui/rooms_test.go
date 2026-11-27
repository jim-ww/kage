package ui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"

	"github.com/jim-ww/kage/ui/modal"
)

type fakeRoomDaemon struct {
	fakeSuccessSender
	joins   []string
	starts  []string
	nicks   []string
	joinErr error
}

func (f *fakeRoomDaemon) JoinRoom(accountIdx int, room, nick, password string) tea.Msg {
	f.joins = append(f.joins, strings.TrimSpace(room+" as "+nick+" "+password))
	return RoomJoinedMsg{AccountIdx: accountIdx, Room: room, Err: f.joinErr}
}

func (f *fakeRoomDaemon) SetRoomNick(accountIdx int, room, nick string) tea.Msg {
	f.nicks = append(f.nicks, room+" "+nick)
	return RoomNickSetMsg{AccountIdx: accountIdx, Room: room, Nick: nick}
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
	for _, unwanted := range []string{"Rename", "Encryption", "Block", "Re-sync"} {
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
