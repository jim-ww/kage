package main

import (
	"context"
	"path/filepath"
	"slices"
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
	} {
		handleIncomingMessage(ctx, srv, 0, s, ev)
	}
	hist := roomHistory(t, s)
	want := []struct {
		author string
		me     bool
	}{{"bob", false}, {"me", true}}
	if len(hist) != len(want) {
		t.Fatalf("history = %+v", hist)
	}
	for i, w := range want {
		if hist[i].Author != w.author || hist[i].IsMe != w.me {
			t.Errorf("message %d: author %q me %v, want %q %v", i, hist[i].Author, hist[i].IsMe, w.author, w.me)
		}
	}
}

// TestPrivateMessageHasItsOwnChat checks a message an occupant sends us
// privately, and our answer from another device, go to a chat of their
// own, never into the room's for everyone to see.
func TestPrivateMessageHasItsOwnChat(t *testing.T) {
	ctx := context.Background()
	srv, _ := attachedClient(t)
	s := roomSession(t)
	carol := testRoom + "/carol"
	handleIncomingMessage(ctx, srv, 0, s, xmpp.MessageEvent{ID: "p1", From: carol, Body: "psst", MUCPrivate: true, SentAt: time.Unix(100, 0)})
	handleIncomingMessage(ctx, srv, 0, s, xmpp.MessageEvent{ID: "p2", From: s.account.JID + "/phone", To: carol, Outgoing: true, Body: "yes?", MUCPrivate: true, SentAt: time.Unix(101, 0)})

	if hist := roomHistory(t, s); len(hist) != 0 {
		t.Fatalf("private messages in the room's chat: %+v", hist)
	}
	if !s.isRoomPrivate(carol) || !s.hasChat(carol) {
		t.Fatal("no private chat with carol")
	}
	hist := loadHistory(ctx, s, carol, "carol")
	if len(hist) != 2 || hist[0].Author != "carol" || hist[0].IsMe || hist[1].Author != "me" || !hist[1].IsMe {
		t.Fatalf("private history = %+v", hist)
	}
	if mode := resolveEncryptionMode(ctx, s, carol); mode != "none" {
		t.Errorf("private chat encryption = %q", mode)
	}

	_, acct, err := connectAccountLocal(ctx, s.account, s.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, item := range acct.Chats {
		if c := item.(ui.Chat); c.Address == carol {
			found = c.PrivateRoom == testRoom && !c.Room
		}
	}
	if !found {
		t.Errorf("private chat not listed on load: %+v", acct.Chats)
	}

	s.forgetRoom(ctx, srv, 0, testRoom)
	if s.hasChat(carol) {
		t.Error("private chat kept after leaving its room")
	}
}

// TestPrivateMessageNotThroughUnknownRoom checks a private message through
// a room we're not in is dropped, not made a contact's chat.
func TestPrivateMessageNotThroughUnknownRoom(t *testing.T) {
	srv, _ := attachedClient(t)
	s := roomSession(t)
	handleIncomingMessage(context.Background(), srv, 0, s, xmpp.MessageEvent{ID: "p", From: "other@muc.example.com/bob", Body: "hi", MUCPrivate: true})
	if s.hasChat("other@muc.example.com") || s.hasChat("other@muc.example.com/bob") {
		t.Error("a chat was made for it")
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

// TestRoomEncryption checks a room is encrypted by default only when
// private, can be turned off there, and a private chat in one never is.
func TestRoomEncryption(t *testing.T) {
	ctx := context.Background()
	s := roomSession(t)
	if mode := resolveEncryptionMode(ctx, s, testRoom); mode != "none" {
		t.Errorf("open room's encryption = %q", mode)
	}
	s.updateRoom(testRoom, func(st *roomState) { st.info.MembersOnly, st.info.NonAnonymous = true, true })
	if mode := resolveEncryptionMode(ctx, s, testRoom); mode != "omemo-v1" {
		t.Errorf("private room's encryption = %q", mode)
	}
	if err := s.db.SetChatEncryptionMode(ctx, storage.SetChatEncryptionModeParams{AccountJid: s.account.JID, RosterJid: testRoom, Mode: "none"}); err != nil {
		t.Fatal(err)
	}
	if mode := resolveEncryptionMode(ctx, s, testRoom); mode != "none" {
		t.Errorf("private room turned off = %q", mode)
	}
	s.updateRoom(testRoom, func(st *roomState) { st.privates["bob"] = true })
	if mode := resolveEncryptionMode(ctx, s, testRoom+"/bob"); mode != "none" {
		t.Errorf("private chat's encryption = %q", mode)
	}
}

// TestRoomRecipients checks a room's message is encrypted for its members
// and everyone in it, once each, and not for us.
func TestRoomRecipients(t *testing.T) {
	ctx := context.Background()
	srv, _ := attachedClient(t)
	s := roomSession(t)
	s.updateRoom(testRoom, func(st *roomState) { st.members["carol@example.com"] = true })
	for _, ev := range []xmpp.RoomPresenceEvent{
		{Room: testRoom, Nick: "bob", RealJID: "bob@example.com", Affiliation: "member", Available: true},
		{Room: testRoom, Nick: "dave", RealJID: "dave@example.com", Affiliation: "none", Available: true},
		{Room: testRoom, Nick: "me", RealJID: "me@example.com", Affiliation: "owner", Available: true, StatusCodes: []int{xmpp.MUCStatusSelf}},
	} {
		s.handleRoomPresence(ctx, srv, 0, ev)
	}
	want := []string{"bob@example.com", "carol@example.com", "dave@example.com"}
	if got := s.roomRecipients(testRoom); !slices.Equal(got, want) {
		t.Errorf("recipients = %v, want %v", got, want)
	}
	// Bob left and was made a non-member: no longer one.
	s.handleRoomPresence(ctx, srv, 0, xmpp.RoomPresenceEvent{Room: testRoom, Nick: "bob", RealJID: "bob@example.com", Affiliation: "none"})
	if got := s.roomRecipients(testRoom); slices.Contains(got, "bob@example.com") {
		t.Errorf("recipients after bob's removal = %v", got)
	}
	if st, _ := s.room(testRoom); st.realJIDs["bob"] != "bob@example.com" {
		t.Error("bob's address forgotten: his earlier messages couldn't be decrypted")
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

// TestRoomOccupantIDOutlivesNick checks that in a room stamping
// occupant-ids, a message is its sender's across a nick change, and not
// whoever takes their old nick.
func TestRoomOccupantIDOutlivesNick(t *testing.T) {
	ctx := context.Background()
	srv, _ := attachedClient(t)
	s := roomSession(t)
	s.updateRoom(testRoom, func(st *roomState) { st.info.OccupantID = true })
	handleIncomingMessage(ctx, srv, 0, s, xmpp.MessageEvent{ID: "a", From: testRoom + "/bob", OccupantID: "occ-bob", Body: "hi", Groupchat: true})

	// Bob is now "robert"; mallory took "bob".
	handleIncomingMessage(ctx, srv, 0, s, xmpp.MessageEvent{ID: "c1", From: testRoom + "/bob", OccupantID: "occ-mallory", Body: "forged", ReplaceID: "a", Groupchat: true})
	if hist := roomHistory(t, s); hist[0].Content != "hi" {
		t.Fatalf("bob's old nick corrected his message: %+v", hist[0])
	}
	handleIncomingMessage(ctx, srv, 0, s, xmpp.MessageEvent{ID: "c2", From: testRoom + "/robert", OccupantID: "occ-bob", Body: "hello", ReplaceID: "a", Groupchat: true})
	if hist := roomHistory(t, s); hist[0].Content != "hello" {
		t.Errorf("bob's correction under his new nick not applied: %+v", hist[0])
	}

	// One reaction set per person, whatever their nick.
	for _, from := range []string{"bob", "robert"} {
		handleIncomingMessage(ctx, srv, 0, s, xmpp.MessageEvent{From: testRoom + "/" + from, OccupantID: "occ-bob", ReactionTargetID: "a", Reactions: []string{"👍"}, Groupchat: true})
	}
	if r := loadReactionsForMessage(ctx, s, testRoom, "a"); len(r) != 1 || r[0].Count != 1 {
		t.Errorf("reactions = %+v", r)
	}
}

// TestRoomOwnByOccupantID checks our own message is known as ours by
// occupant-id, whatever nick it came under.
func TestRoomOwnByOccupantID(t *testing.T) {
	ctx := context.Background()
	srv, _ := attachedClient(t)
	s := roomSession(t)
	s.updateRoom(testRoom, func(st *roomState) { st.info.OccupantID, st.occupantID = true, "occ-me" })
	handleIncomingMessage(ctx, srv, 0, s, xmpp.MessageEvent{ID: "a", From: testRoom + "/me-old", OccupantID: "occ-me", Body: "from my phone", Groupchat: true})
	handleIncomingMessage(ctx, srv, 0, s, xmpp.MessageEvent{ID: "b", From: testRoom + "/me", OccupantID: "occ-other", Body: "not me", Groupchat: true})
	hist := roomHistory(t, s)
	if !hist[0].IsMe || hist[1].IsMe {
		t.Errorf("history = %+v", hist)
	}
}

// TestRoomSelfPingRejoins checks a room that no longer has us is joined
// again, and one that does, or can't tell, is left alone.
func TestRoomSelfPingRejoins(t *testing.T) {
	s := roomSession(t)
	s.putRoom("quiet@muc.example.com", "", "me", "", true)
	s.putRoom("gone@muc.example.com", "", "me", "", true)
	for _, room := range s.roomAddrs() {
		s.updateRoom(room, func(st *roomState) { st.joined = true })
	}
	answers := map[string]xmpp.SelfPingResult{
		testRoom:                xmpp.StillJoined,
		"quiet@muc.example.com": xmpp.PingUnknown,
		"gone@muc.example.com":  xmpp.NotJoined,
	}
	var rejoined []string
	s.checkRooms(context.Background(), func(_ context.Context, room, nick string) xmpp.SelfPingResult {
		return answers[room]
	}, func(room string) { rejoined = append(rejoined, room) })
	if len(rejoined) != 1 || rejoined[0] != "gone@muc.example.com" {
		t.Errorf("rejoined %v", rejoined)
	}
}

// TestRoomArchiveItems checks an item of a room's archive is stored as the
// live message would be, known by the room's id when it has none of its
// own, and only once.
func TestRoomArchiveItems(t *testing.T) {
	ctx := context.Background()
	srv, _ := attachedClient(t)
	s := roomSession(t)
	am := xmpp.ArchivedMessage{ArchiveID: "s1", From: testRoom + "/bob", Body: "earlier", SentAt: time.Unix(100, 0), OccupantID: "occ-bob"}
	msg, ok := roomMessage(ctx, srv, 0, s, roomArchiveEvent(am), true)
	if !ok || msg == nil || msg.ID != "s1" || msg.Author != "bob" {
		t.Fatalf("archived message = %+v", msg)
	}
	if again, _ := roomMessage(ctx, srv, 0, s, roomArchiveEvent(am), true); again != nil {
		t.Errorf("stored twice: %+v", again)
	}
	if ref, err := s.db.GetLatestRoomStanzaID(ctx, storage.GetLatestRoomStanzaIDParams{AccountJid: s.account.JID, RosterJid: nullString(testRoom)}); err != nil || ref.String != "s1" {
		t.Errorf("archive cursor = %q, %v", ref.String, err)
	}
}

func TestRoomRepliesToOwn(t *testing.T) {
	ctx := context.Background()
	srv, _ := attachedClient(t)
	s := roomSession(t)
	handleIncomingMessage(ctx, srv, 0, s, xmpp.MessageEvent{ID: "a", From: testRoom + "/bob", Body: "hi", Groupchat: true})
	handleIncomingMessage(ctx, srv, 0, s, xmpp.MessageEvent{ID: "b", From: testRoom + "/me", Body: "mine", Groupchat: true})
	for id, want := range map[string]bool{"a": false, "b": true, "": false, "gone": false} {
		if got := s.repliesToOwn(ctx, testRoom, id); got != want {
			t.Errorf("repliesToOwn(%q) = %v, want %v", id, got, want)
		}
	}
}

// TestRoomTypingBroadcast checks an occupant's typing in a room reaches
// the TUI by nick, and our own, echoed back, doesn't.
func TestRoomTypingBroadcast(t *testing.T) {
	srv, events := attachedClient(t)
	s := roomSession(t)
	s.roomTyping(srv, 0, xmpp.ChatStateEvent{From: testRoom + "/me", State: xmpp.ChatStateComposing, Groupchat: true})
	s.roomTyping(srv, 0, xmpp.ChatStateEvent{From: testRoom + "/bob", State: xmpp.ChatStateComposing, Groupchat: true})
	select {
	case ev := <-events:
		if got, ok := ev.(ui.TypingMsg); !ok || got.From != testRoom || got.Nick != "bob" || !got.Typing {
			t.Fatalf("event = %#v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no typing broadcast")
	}
	select {
	case ev := <-events:
		t.Errorf("another event: %#v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestRoomUnreadOnlyForUs checks a room's unread count grows only with
// what names us or replies to us.
func TestRoomUnreadOnlyForUs(t *testing.T) {
	ctx := context.Background()
	srv, _ := attachedClient(t)
	s := roomSession(t)
	resetUnreadBadge(t)
	unread := func() int {
		t.Helper()
		rows, err := s.db.ListChatUnread(ctx, s.account.JID)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.Rosterjid == testRoom {
				return int(r.Count)
			}
		}
		return 0
	}
	for _, ev := range []xmpp.MessageEvent{
		{ID: "a", From: testRoom + "/bob", Body: "chatter", Groupchat: true},
		{ID: "b", From: testRoom + "/me", Body: "mine", Groupchat: true},
		{ID: "c", From: testRoom + "/bob", Body: "me: look", Groupchat: true},
		{ID: "d", From: testRoom + "/bob", Body: "agreed", ReplyToID: "b", Groupchat: true},
	} {
		handleIncomingMessage(ctx, srv, 0, s, ev)
	}
	if got := unread(); got != 2 {
		t.Errorf("unread = %d, want 2", got)
	}
}

func TestRoomSlug(t *testing.T) {
	for name, want := range map[string]string{
		"Tea House":      "tea-house",
		"  Go, Rust & C": "go-rust-c",
		"Чай":            "",
	} {
		if got := roomSlug(name); got != want {
			t.Errorf("roomSlug(%q) = %q, want %q", name, got, want)
		}
	}
	if a, b := randomRoomLocalpart(), randomRoomLocalpart(); len(a) != 12 || a == b {
		t.Errorf("random localparts %q, %q", a, b)
	}
}

// TestRoomEncryptedFileIsAnAttachment checks an aesgcm:// link, all an
// encrypted file is, comes in as an attachment, in the room and in a
// private chat in it.
func TestRoomEncryptedFileIsAnAttachment(t *testing.T) {
	ctx := context.Background()
	srv, events := attachedClient(t)
	s := roomSession(t)
	const link = "aesgcm://upload.example.com/f/pic.png#00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff00112233"
	msg, _ := roomMessage(ctx, srv, 0, s, xmpp.MessageEvent{ID: "a", From: testRoom + "/bob", Body: link, Groupchat: true}, true)
	if msg == nil || !slices.Equal(msg.Attachments, []string{link}) {
		t.Fatalf("room message = %+v", msg)
	}
	handlePrivateMessage(ctx, srv, 0, s, xmpp.MessageEvent{ID: "b", From: testRoom + "/bob", Body: link, MUCPrivate: true})
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-events:
			if in, ok := ev.(ui.IncomingMessageMsg); ok {
				if !slices.Equal(in.Message.Attachments, []string{link}) {
					t.Fatalf("private message = %+v", in.Message)
				}
				return
			}
		case <-deadline:
			t.Fatal("no private message broadcast")
		}
	}
}
