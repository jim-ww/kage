package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/xmpp"
)

const testRoom = "room@muc.example.com"

// roomSession is an account in testRoom as "me".
func roomSession(t *testing.T) *accountSession {
	t.Helper()
	_, q, err := storage.Open(filepath.Join(t.TempDir(), "kage.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	s := &accountSession{account: config.Account{JID: "me@example.com"}, db: q}
	s.putRoom(testRoom, "Room", "me", "", true)
	s.persistRoom(context.Background(), testRoom)
	return s
}

func roomHistory(t *testing.T, s *accountSession) []ui.Message {
	t.Helper()
	return loadHistory(context.Background(), s, testRoom, "Room")
}

// TestRoomMessageAuthors checks a room's messages are each by their nick,
// ours being "me", live and from storage alike.
func TestRoomMessageAuthors(t *testing.T) {
	ctx := context.Background()
	srv, _ := attachedClient(t)
	s := roomSession(t)

	for _, ev := range []xmpp.MessageEvent{
		{ID: "a", From: testRoom + "/bob", Body: "hi", Groupchat: true, SentAt: time.Unix(100, 0)},
		{ID: "b", From: testRoom + "/me", Body: "from my phone", Groupchat: true, SentAt: time.Unix(101, 0)},
		{ID: "c", From: testRoom + "/carol", Body: "psst", MUCPrivate: true, SentAt: time.Unix(102, 0)},
	} {
		handleIncomingMessage(ctx, srv, 0, s, ev)
	}
	hist := roomHistory(t, s)
	want := []struct {
		author string
		me     bool
	}{{"bob", false}, {"me", true}, {"carol (private)", false}}
	if len(hist) != len(want) {
		t.Fatalf("history = %+v", hist)
	}
	for i, w := range want {
		if hist[i].Author != w.author || hist[i].IsMe != w.me {
			t.Errorf("message %d: author %q me %v, want %q %v", i, hist[i].Author, hist[i].IsMe, w.author, w.me)
		}
	}
}

// TestRoomMessageNotFromUnknownRoom checks a groupchat message from a room
// we're not in is dropped, not made a chat.
func TestRoomMessageNotFromUnknownRoom(t *testing.T) {
	srv, _ := attachedClient(t)
	s := roomSession(t)
	handleIncomingMessage(context.Background(), srv, 0, s, xmpp.MessageEvent{
		ID: "x", From: "other@muc.example.com/bob", Body: "hi", Groupchat: true,
	})
	if s.inRoster("other@muc.example.com") {
		t.Error("a chat was made for a room we're not in")
	}
}

// TestRoomEchoDedupes checks our own message coming back from the room is
// not stored twice, but marks ours delivered and learns its stanza-id.
func TestRoomEchoDedupes(t *testing.T) {
	ctx := context.Background()
	srv, _ := attachedClient(t)
	s := roomSession(t)
	if _, err := s.db.InsertMessage(ctx, storage.InsertMessageParams{
		AccountJid: s.account.JID, Sent: true, IDAttr: nullString("mine"), Body: nullString("hello"),
		StanzaType: "groupchat", RosterJid: nullString(testRoom), Delay: 100,
	}); err != nil {
		t.Fatal(err)
	}
	handleIncomingMessage(ctx, srv, 0, s, xmpp.MessageEvent{
		ID: "mine", From: testRoom + "/me", Body: "hello", Groupchat: true, RoomStanzaID: "s1",
	})
	hist := roomHistory(t, s)
	if len(hist) != 1 || !hist[0].Delivered {
		t.Fatalf("history = %+v", hist)
	}
	if ref := s.roomStanzaRef(ctx, testRoom, "mine"); ref != "s1" {
		t.Errorf("stanza ref = %q, want s1", ref)
	}
	if id := s.roomTargetID(ctx, testRoom, "s1"); id != "mine" {
		t.Errorf("target of s1 = %q, want mine", id)
	}
}

// TestRoomCorrectionOnlyBySender checks only a message's own sender can
// correct or retract it in a room, where everyone else's message is just
// as "received" as theirs.
func TestRoomCorrectionOnlyBySender(t *testing.T) {
	ctx := context.Background()
	srv, _ := attachedClient(t)
	s := roomSession(t)
	handleIncomingMessage(ctx, srv, 0, s, xmpp.MessageEvent{ID: "a", From: testRoom + "/bob", Body: "hi", Groupchat: true})

	handleIncomingMessage(ctx, srv, 0, s, xmpp.MessageEvent{ID: "c1", From: testRoom + "/mallory", Body: "bob is wrong", ReplaceID: "a", Groupchat: true})
	handleIncomingMessage(ctx, srv, 0, s, xmpp.MessageEvent{ID: "r1", From: testRoom + "/mallory", RetractID: "a", Groupchat: true})
	if hist := roomHistory(t, s); hist[0].Content != "hi" || hist[0].Edited || hist[0].Retracted {
		t.Fatalf("someone else changed bob's message: %+v", hist[0])
	}

	handleIncomingMessage(ctx, srv, 0, s, xmpp.MessageEvent{ID: "c2", From: testRoom + "/bob", Body: "hello", ReplaceID: "a", Groupchat: true})
	if hist := roomHistory(t, s); hist[0].Content != "hello" || !hist[0].Edited {
		t.Errorf("bob's own correction not applied: %+v", hist[0])
	}
}

// TestRoomReactionsByOccupant checks reactions in a room count per occupant
// and find their message by the room's stanza-id.
func TestRoomReactionsByOccupant(t *testing.T) {
	ctx := context.Background()
	srv, _ := attachedClient(t)
	s := roomSession(t)
	handleIncomingMessage(ctx, srv, 0, s, xmpp.MessageEvent{ID: "a", From: testRoom + "/bob", Body: "hi", Groupchat: true, RoomStanzaID: "s1"})
	for _, from := range []string{"carol", "dave"} {
		handleIncomingMessage(ctx, srv, 0, s, xmpp.MessageEvent{
			From: testRoom + "/" + from, ReactionTargetID: "s1", Reactions: []string{"👍"}, Groupchat: true,
		})
	}
	r := loadReactionsForMessage(ctx, s, testRoom, "a")
	if len(r) != 1 || r[0].Count != 2 || r[0].Mine {
		t.Errorf("reactions = %+v", r)
	}
}

func TestRoomPresence(t *testing.T) {
	ctx := context.Background()
	srv, _ := attachedClient(t)
	s := roomSession(t)
	answer := make(chan error, 1)
	s.updateRoom(testRoom, func(st *roomState) { st.waiters = append(st.waiters, answer) })

	s.handleRoomPresence(ctx, srv, 0, xmpp.RoomPresenceEvent{Room: testRoom, Nick: "bob", RealJID: "bob@example.com", Role: "participant", Available: true})
	s.handleRoomPresence(ctx, srv, 0, xmpp.RoomPresenceEvent{Room: testRoom, Nick: "me2", Role: "participant", Available: true, StatusCodes: []int{xmpp.MUCStatusSelf, xmpp.MUCStatusNickByRoom}})
	if err := <-answer; err != nil {
		t.Fatalf("join answered %v", err)
	}
	st, _ := s.room(testRoom)
	if !st.joined || st.nick != "me2" || len(st.occupants) != 2 || st.occupants["bob"].JID != "bob@example.com" {
		t.Fatalf("room = %+v", st)
	}

	s.handleRoomPresence(ctx, srv, 0, xmpp.RoomPresenceEvent{Room: testRoom, Nick: "bob"})
	s.handleRoomPresence(ctx, srv, 0, xmpp.RoomPresenceEvent{Room: testRoom, Nick: "me2", StatusCodes: []int{xmpp.MUCStatusSelf, xmpp.MUCStatusKicked}})
	st, _ = s.room(testRoom)
	if st.joined || st.joinErr != "kicked from the room" || len(st.occupants) != 0 {
		t.Errorf("after the kick: %+v", st)
	}
}

// TestRoomJoinRefused checks a room's error presence answers the join.
func TestRoomJoinRefused(t *testing.T) {
	srv, _ := attachedClient(t)
	s := roomSession(t)
	answer := make(chan error, 1)
	s.updateRoom(testRoom, func(st *roomState) { st.waiters = append(st.waiters, answer) })
	if !s.handlePresenceError(srv, 0, xmpp.PresenceErrorEvent{From: testRoom + "/me", Condition: "conflict"}) {
		t.Fatal("not taken as the room's")
	}
	if err := <-answer; err == nil || err.Error() != "that nick is taken in the room" {
		t.Errorf("join answered %v", err)
	}
	if s.handlePresenceError(srv, 0, xmpp.PresenceErrorEvent{From: "bob@example.com/pc"}) {
		t.Error("a contact's error presence taken as a room's")
	}
}

func TestRoomEncryptionIsNone(t *testing.T) {
	s := roomSession(t)
	if mode := resolveEncryptionMode(context.Background(), s, testRoom); mode != "none" {
		t.Errorf("room encryption = %q", mode)
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
		if got := mentions(body, "alice"); got != want {
			t.Errorf("mentions(%q) = %v, want %v", body, got, want)
		}
	}
}

// TestRoomsLoadedAsChats checks stored rooms come back as room chats.
func TestRoomsLoadedAsChats(t *testing.T) {
	ctx := context.Background()
	s := roomSession(t)
	_, acct, err := connectAccountLocal(ctx, s.account, s.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(acct.Chats) != 1 {
		t.Fatalf("chats = %+v", acct.Chats)
	}
	chat := acct.Chats[0].(ui.Chat)
	if !chat.Room || chat.Address != testRoom || chat.Name != "Room" || chat.Nick != "me" || chat.EncryptionMode != "none" {
		t.Errorf("chat = %+v", chat)
	}
}
