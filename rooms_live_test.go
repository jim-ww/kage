//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/xmpp"
)

// newRoomTestAccount is a throwaway account dialed into the devtest server
// with its event loop running, and the adapter its RPCs go through.
func newRoomTestAccount(ctx context.Context, t *testing.T, tlsConfig *tls.Config) (*adapter, *accountSession) {
	t.Helper()
	jid, pass := newThrowawayAccount(ctx, t, tlsConfig)
	client, err := xmpp.Dial(ctx, jid, pass, tlsConfig)
	if err != nil {
		t.Fatalf("dial %s: %v", jid, err)
	}
	t.Cleanup(func() { client.Close() })
	_, q, err := storage.Open(filepath.Join(t.TempDir(), "rooms.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	s := &accountSession{account: config.Account{JID: jid, Password: pass}, db: q, tlsConfig: tlsConfig}
	s.client.Store(client)
	s.roster.Store(&map[string]rosterEntry{})
	srv := ipc.NewServer()
	t.Cleanup(srv.Close)
	listenCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go listen(listenCtx, srv, 0, s)
	return &adapter{sessions: []*accountSession{s}, srv: srv}, s
}

// eventually polls cond until it holds or d passes.
func eventually(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestGroupChatLive runs a room end to end on a real server: creating it
// by joining, a second account joining, a message and a reaction each way,
// the bookmark, and leaving.
func TestGroupChatLive(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()
	aliceA, alice := newRoomTestAccount(ctx, t, tlsConfig)
	bobA, bob := newRoomTestAccount(ctx, t, tlsConfig)

	var b [6]byte
	_, _ = rand.Read(b[:])
	room := "kagetest-" + hex.EncodeToString(b[:]) + "@conference.localhost"

	// A room that doesn't exist yet is created by joining it.
	if err := aliceA.JoinRoom(0, room, "alice", ""); err != nil {
		t.Fatalf("alice creating the room: %v", err)
	}
	bookmarks, err := alice.client.Load().Bookmarks(ctx)
	if err != nil {
		t.Fatalf("fetching alice's bookmarks: %v", err)
	}
	if !slices.ContainsFunc(bookmarks, func(b xmpp.Bookmark) bool { return b.Room == room && b.Autojoin && b.Nick == "alice" }) {
		t.Errorf("alice's bookmarks = %+v", bookmarks)
	}

	if err := bobA.JoinRoom(0, room, "bob", ""); err != nil {
		t.Fatalf("bob joining: %v", err)
	}
	eventually(t, 5*time.Second, "alice seeing bob in the room", func() bool {
		st, _ := alice.room(room)
		_, in := st.occupants["bob"]
		return in
	})
	if st, _ := alice.room(room); !st.info.OccupantID || st.occupantID == "" {
		t.Errorf("no occupant-id from the room: %+v", st.info)
	}
	if st, _ := alice.room(room); st.occupants["bob"].JID != bob.account.JID {
		t.Errorf("bob's address as alice, the owner, sees it: %q", st.occupants["bob"].JID)
	}

	id, err := aliceA.Send(0, room, "hello room", ui.SendOptions{})
	if err != nil {
		t.Fatalf("alice sending: %v", err)
	}
	eventually(t, 5*time.Second, "bob getting alice's message", func() bool {
		hist := loadHistory(ctx, bob, room, room)
		return len(hist) == 1 && hist[0].Author == "alice" && hist[0].Content == "hello room" && !hist[0].IsMe
	})
	eventually(t, 5*time.Second, "alice's echo", func() bool {
		hist := loadHistory(ctx, alice, room, room)
		return len(hist) == 1 && hist[0].Delivered && hist[0].IsMe
	})

	// Bob reacts by the room's stanza-id, which alice learned from her echo.
	bobsCopy := loadHistory(ctx, bob, room, room)[0].ID
	if _, err := bobA.Send(0, room, "", ui.SendOptions{ReactionTargetID: bobsCopy, Reactions: []string{"👍"}}); err != nil {
		t.Fatalf("bob reacting: %v", err)
	}
	eventually(t, 5*time.Second, "alice seeing bob's reaction", func() bool {
		r := loadReactionsForMessage(ctx, alice, room, id)
		return len(r) == 1 && r[0].Emoji == "👍" && r[0].Count == 1 && !r[0].Mine
	})

	if _, err := bobA.Send(0, room, "hi alice", ui.SendOptions{}); err != nil {
		t.Fatalf("bob sending: %v", err)
	}
	eventually(t, 5*time.Second, "alice getting bob's message", func() bool {
		hist := loadHistory(ctx, alice, room, room)
		return len(hist) == 2 && hist[1].Author == "bob"
	})

	// Privately, to bob alone: his chat with alice in the room, not the
	// room's.
	if err := aliceA.StartChat(0, room+"/bob"); err != nil {
		t.Fatalf("alice starting a private chat: %v", err)
	}
	if _, err := aliceA.Send(0, room+"/bob", "just for you", ui.SendOptions{}); err != nil {
		t.Fatalf("alice sending privately: %v", err)
	}
	eventually(t, 5*time.Second, "bob getting alice's private message", func() bool {
		hist := loadHistory(ctx, bob, room+"/alice", "alice")
		return len(hist) == 1 && hist[0].Content == "just for you" && hist[0].Author == "alice"
	})
	if hist := loadHistory(ctx, bob, room, room); len(hist) != 2 {
		t.Errorf("bob's room chat after the private message: %+v", hist)
	}
	if _, err := bobA.Send(0, room+"/alice", "thanks", ui.SendOptions{}); err != nil {
		t.Fatalf("bob answering privately: %v", err)
	}
	eventually(t, 5*time.Second, "alice getting bob's private answer", func() bool {
		hist := loadHistory(ctx, alice, room+"/bob", "bob")
		return len(hist) == 2 && hist[1].Content == "thanks" && hist[1].Author == "bob"
	})

	// A new nick for alice, which bob can't then take; her status follows.
	if err := aliceA.SetRoomNick(0, room, "alicia"); err != nil {
		t.Fatalf("alice changing nick: %v", err)
	}
	eventually(t, 5*time.Second, "bob seeing alice as alicia", func() bool {
		st, _ := bob.room(room)
		_, old := st.occupants["alice"]
		_, renamed := st.occupants["alicia"]
		return renamed && !old
	})
	if err := bobA.SetRoomNick(0, room, "alicia"); err == nil || err.Error() != "that nick is taken in the room" {
		t.Errorf("bob taking alice's nick: %v", err)
	}
	if st, _ := bob.room(room); st.nick != "bob" || !st.joined {
		t.Errorf("bob after the refused nick change: nick %q joined %v", st.nick, st.joined)
	}
	alice.presenceToRooms(ctx, alice.client.Load(), "away")
	eventually(t, 5*time.Second, "bob seeing alicia away", func() bool {
		st, _ := bob.room(room)
		return st.occupants["alicia"].Presence == ui.PresenceAway
	})

	if err := bobA.LeaveRoom(0, room); err != nil {
		t.Fatalf("bob leaving: %v", err)
	}
	if bob.isRoom(room) {
		t.Error("bob still has the room after leaving")
	}
	eventually(t, 5*time.Second, "alice seeing bob leave", func() bool {
		st, _ := alice.room(room)
		_, in := st.occupants["bob"]
		return !in
	})
	bookmarks, err = bob.client.Load().Bookmarks(ctx)
	if err != nil {
		t.Fatalf("fetching bob's bookmarks: %v", err)
	}
	if len(bookmarks) != 0 {
		t.Errorf("bob's bookmarks after leaving = %+v", bookmarks)
	}
}

// TestGroupChatNickTaken checks a join as a nick someone has is refused,
// and the room isn't kept.
func TestGroupChatNickTaken(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()
	aliceA, _ := newRoomTestAccount(ctx, t, tlsConfig)
	bobA, bob := newRoomTestAccount(ctx, t, tlsConfig)

	var b [6]byte
	_, _ = rand.Read(b[:])
	room := "kagetest-" + hex.EncodeToString(b[:]) + "@conference.localhost"
	if err := aliceA.JoinRoom(0, room, "same", ""); err != nil {
		t.Fatalf("alice creating the room: %v", err)
	}
	err := bobA.JoinRoom(0, room, "same", "")
	if err == nil || err.Error() != "that nick is taken in the room" {
		t.Fatalf("bob joining as alice's nick: %v", err)
	}
	if bob.isRoom(room) {
		t.Error("a refused room kept")
	}
}

// TestBookmarkedElsewhereJoined checks a room bookmarked by another client
// of the account is joined on connect.
func TestBookmarkedElsewhereJoined(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()
	aliceA, alice := newRoomTestAccount(ctx, t, tlsConfig)

	var b [6]byte
	_, _ = rand.Read(b[:])
	room := "kagetest-" + hex.EncodeToString(b[:]) + "@conference.localhost"
	if err := aliceA.JoinRoom(0, room, "alice", ""); err != nil {
		t.Fatalf("creating the room: %v", err)
	}

	// Another device: the same account, nothing stored.
	client, err := xmpp.Dial(ctx, alice.account.JID, alice.account.Password, tlsConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	_, q, err := storage.Open(filepath.Join(t.TempDir(), "other.db"))
	if err != nil {
		t.Fatal(err)
	}
	other := &accountSession{account: alice.account, db: q}
	other.client.Store(client)
	other.roster.Store(&map[string]rosterEntry{})
	srv := ipc.NewServer()
	t.Cleanup(srv.Close)
	listenCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go listen(listenCtx, srv, 0, other)

	enterRooms(ctx, srv, 0, other)
	eventually(t, 5*time.Second, "the other device joining the bookmarked room", func() bool {
		st, ok := other.room(room)
		return ok && st.joined && st.bookmarked
	})
}

// TestRoomArchiveCatchUp checks joining a room with an archive reads what
// was said before from it, once, and again only what came after.
func TestRoomArchiveCatchUp(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()
	aliceA, _ := newRoomTestAccount(ctx, t, tlsConfig)
	bobA, bob := newRoomTestAccount(ctx, t, tlsConfig)

	var b [6]byte
	_, _ = rand.Read(b[:])
	room := "kagetest-" + hex.EncodeToString(b[:]) + "@conference.localhost"
	if err := aliceA.JoinRoom(0, room, "alice", ""); err != nil {
		t.Fatalf("creating the room: %v", err)
	}
	for _, body := range []string{"one", "two", "three"} {
		if _, err := aliceA.Send(0, room, body, ui.SendOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(300 * time.Millisecond) // archived

	if err := bobA.JoinRoom(0, room, "bob", ""); err != nil {
		t.Fatalf("bob joining: %v", err)
	}
	if st, _ := bob.room(room); !st.info.MAM {
		t.Fatalf("room has no archive: %+v", st.info)
	}
	eventually(t, 5*time.Second, "bob reading the room's archive", func() bool {
		hist := loadHistory(ctx, bob, room, room)
		return len(hist) == 3 && hist[0].Content == "one" && hist[2].Content == "three" && hist[0].Author == "alice"
	})

	// Rejoining reads only what's new.
	client := bob.client.Load()
	if err := client.LeaveRoom(ctx, room, "bob"); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "bob out", func() bool { st, _ := bob.room(room); return !st.joined })
	if _, err := aliceA.Send(0, room, "four", ui.SendOptions{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	bob.sendJoin(ctx, bobA.srv, 0, client, room)
	eventually(t, 5*time.Second, "bob catching up", func() bool {
		hist := loadHistory(ctx, bob, room, room)
		return len(hist) == 4 && hist[3].Content == "four"
	})
	time.Sleep(300 * time.Millisecond)
	if hist := loadHistory(ctx, bob, room, room); len(hist) != 4 {
		t.Errorf("history after catching up = %d messages", len(hist))
	}
}

// newOmemoRoomTestAccount is newRoomTestAccount with OMEMO set up.
func newOmemoRoomTestAccount(ctx context.Context, t *testing.T, tlsConfig *tls.Config) (*adapter, *accountSession) {
	t.Helper()
	a, s := newRoomTestAccount(ctx, t, tlsConfig)
	setupOmemo(ctx, s)
	if s.omemoV1() == nil {
		t.Fatalf("setupOmemo(%s): no legacy OMEMO", s.account.JID)
	}
	return a, s
}

// TestEncryptedRoomLive checks a private room (members-only, showing real
// JIDs) is encrypted for every member by default, both ways, corrections
// too, and that our own echo doesn't undo what we sent.
func TestEncryptedRoomLive(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()
	aliceA, alice := newOmemoRoomTestAccount(ctx, t, tlsConfig)
	bobA, bob := newOmemoRoomTestAccount(ctx, t, tlsConfig)

	var b [6]byte
	_, _ = rand.Read(b[:])
	room := "kagetest-" + hex.EncodeToString(b[:]) + "@conference.localhost"
	if err := aliceA.JoinRoom(0, room, "alice", ""); err != nil {
		t.Fatalf("creating the room: %v", err)
	}
	client := alice.client.Load()
	if err := client.SetRoomConfig(ctx, room, map[string]string{
		"muc#roomconfig_membersonly": "1",
		"muc#roomconfig_whois":       "anyone",
	}); err != nil {
		t.Fatalf("making the room private: %v", err)
	}
	if err := client.SetAffiliation(ctx, room, bob.account.JID, "member"); err != nil {
		t.Fatalf("making bob a member: %v", err)
	}
	alice.refreshRoomInfo(ctx, client, room)
	if mode := resolveEncryptionMode(ctx, alice, room); mode != "omemo-v1" {
		t.Fatalf("private room's encryption = %q", mode)
	}

	if err := bobA.JoinRoom(0, room, "bob", ""); err != nil {
		t.Fatalf("bob joining: %v", err)
	}
	if mode := resolveEncryptionMode(ctx, bob, room); mode != "omemo-v1" {
		t.Fatalf("bob's view of the room's encryption = %q", mode)
	}
	eventually(t, 5*time.Second, "alice seeing bob", func() bool { return slices.Contains(alice.roomRecipients(room), bob.account.JID) })

	id, err := aliceA.Send(0, room, "secret plan", ui.SendOptions{})
	if err != nil {
		t.Fatalf("alice sending: %v", err)
	}
	eventually(t, 5*time.Second, "bob reading alice's encrypted message", func() bool {
		hist := loadHistory(ctx, bob, room, room)
		return len(hist) == 1 && hist[0].Content == "secret plan" && hist[0].Encrypted && hist[0].EncMethod == "omemo-v1"
	})

	if _, err := bobA.Send(0, room, "got it", ui.SendOptions{}); err != nil {
		t.Fatalf("bob sending: %v", err)
	}
	eventually(t, 5*time.Second, "alice reading bob's encrypted message", func() bool {
		hist := loadHistory(ctx, alice, room, room)
		return len(hist) == 2 && hist[1].Content == "got it" && hist[1].Encrypted
	})

	if _, err := aliceA.Send(0, room, "secret plan B", ui.SendOptions{ReplaceID: id}); err != nil {
		t.Fatalf("alice correcting: %v", err)
	}
	eventually(t, 5*time.Second, "bob seeing the correction", func() bool {
		hist := loadHistory(ctx, bob, room, room)
		return len(hist) == 2 && hist[0].Content == "secret plan B" && hist[0].Edited
	})
	time.Sleep(500 * time.Millisecond) // alice's own echoes
	if hist := loadHistory(ctx, alice, room, room); hist[0].Content != "secret plan B" || !hist[0].Delivered {
		t.Errorf("alice's own message after its echoes: %+v", hist[0])
	}
}

// TestRoomSubjectLive checks the owner's new subject reaches everyone,
// and one from someone the room doesn't let is refused, not waited on.
func TestRoomSubjectLive(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()
	aliceA, _ := newRoomTestAccount(ctx, t, tlsConfig)
	bobA, bob := newRoomTestAccount(ctx, t, tlsConfig)
	var b [6]byte
	_, _ = rand.Read(b[:])
	room := "kagetest-" + hex.EncodeToString(b[:]) + "@conference.localhost"
	if err := aliceA.JoinRoom(0, room, "alice", ""); err != nil {
		t.Fatalf("alice creating the room: %v", err)
	}
	if err := bobA.JoinRoom(0, room, "bob", ""); err != nil {
		t.Fatalf("bob joining: %v", err)
	}
	if err := aliceA.SetRoomSubject(0, room, "today: tests"); err != nil {
		t.Fatalf("alice setting the subject: %v", err)
	}
	eventually(t, 5*time.Second, "bob seeing the subject", func() bool {
		st, _ := bob.room(room)
		return st.subject == "today: tests"
	})
	start := time.Now()
	err := bobA.SetRoomSubject(0, room, "bob was here")
	if err == nil {
		t.Fatal("bob, a plain participant, changed the subject")
	}
	if time.Since(start) >= roomNickTimeout {
		t.Errorf("bob's refusal timed out instead of bouncing: %v", err)
	}
	if st, _ := bob.room(room); st.subject != "today: tests" {
		t.Errorf("subject = %q", st.subject)
	}
}

// TestInviteToMembersOnlyRoom checks inviting someone to a members-only
// room we own lets them in, where they were turned away before.
func TestInviteToMembersOnlyRoom(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()
	aliceA, alice := newRoomTestAccount(ctx, t, tlsConfig)
	bobA, bob := newRoomTestAccount(ctx, t, tlsConfig)
	var b [6]byte
	_, _ = rand.Read(b[:])
	room := "kagetest-" + hex.EncodeToString(b[:]) + "@conference.localhost"
	if err := aliceA.JoinRoom(0, room, "alice", ""); err != nil {
		t.Fatalf("alice creating the room: %v", err)
	}
	client := alice.client.Load()
	if err := client.SetRoomConfig(ctx, room, map[string]string{"muc#roomconfig_membersonly": "1"}); err != nil {
		t.Fatalf("making the room members-only: %v", err)
	}
	alice.refreshRoomInfo(ctx, client, room)
	if err := bobA.JoinRoom(0, room, "bob", ""); err == nil {
		t.Fatal("bob got into a members-only room uninvited")
	}
	if err := aliceA.InviteToRoom(0, room, bob.account.JID); err != nil {
		t.Fatalf("inviting bob: %v", err)
	}
	if err := bobA.JoinRoom(0, room, "bob", ""); err != nil {
		t.Fatalf("bob joining once invited: %v", err)
	}
}

// TestModerateOccupantLive runs the owner's moderation on a real room:
// voice, moderator, kick, then a ban that keeps them out.
func TestModerateOccupantLive(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()
	aliceA, alice := newRoomTestAccount(ctx, t, tlsConfig)
	bobA, bob := newRoomTestAccount(ctx, t, tlsConfig)
	var b [6]byte
	_, _ = rand.Read(b[:])
	room := "kagetest-" + hex.EncodeToString(b[:]) + "@conference.localhost"
	if err := aliceA.JoinRoom(0, room, "alice", ""); err != nil {
		t.Fatalf("alice creating the room: %v", err)
	}
	join := func() {
		t.Helper()
		if err := bobA.JoinRoom(0, room, "bob", ""); err != nil {
			t.Fatalf("bob joining: %v", err)
		}
		eventually(t, 5*time.Second, "alice seeing bob", func() bool {
			st, _ := alice.room(room)
			_, in := st.occupants["bob"]
			return in
		})
	}
	role := func(want string) {
		t.Helper()
		eventually(t, 5*time.Second, "bob's role "+want, func() bool {
			st, _ := alice.room(room)
			return st.occupants["bob"].Role == want
		})
	}
	join()
	for _, step := range []struct{ action, role string }{
		{ui.ModRevokeVoice, "visitor"},
		{ui.ModVoice, "participant"},
		{ui.ModModerator, "moderator"},
		{ui.ModUnmoderator, "participant"},
	} {
		if err := aliceA.ModerateOccupant(0, room, "bob", step.action); err != nil {
			t.Fatalf("%s: %v", step.action, err)
		}
		role(step.role)
	}
	if err := bobA.ModerateOccupant(0, room, "alice", ui.ModKick); err == nil {
		t.Error("bob, a participant, kicked the owner")
	}
	if err := aliceA.ModerateOccupant(0, room, "bob", ui.ModKick); err != nil {
		t.Fatalf("kicking bob: %v", err)
	}
	eventually(t, 5*time.Second, "bob out of the room", func() bool {
		st, _ := bob.room(room)
		return !st.joined && st.joinErr != ""
	})
	join()
	if err := aliceA.ModerateOccupant(0, room, "bob", ui.ModBan); err != nil {
		t.Fatalf("banning bob: %v", err)
	}
	eventually(t, 5*time.Second, "bob banned", func() bool {
		st, _ := bob.room(room)
		return !st.joined
	})
	if err := bobA.JoinRoom(0, room, "bob", ""); err == nil {
		t.Error("bob got back in after the ban")
	}
}

// TestRoomSettingsLive reads and changes a room's settings as its owner:
// renamed, it's renamed for us; made private, it's encrypted. A member
// isn't let read them.
func TestRoomSettingsLive(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()
	aliceA, alice := newRoomTestAccount(ctx, t, tlsConfig)
	bobA, _ := newRoomTestAccount(ctx, t, tlsConfig)
	var b [6]byte
	_, _ = rand.Read(b[:])
	room := "kagetest-" + hex.EncodeToString(b[:]) + "@conference.localhost"
	if err := aliceA.JoinRoom(0, room, "alice", ""); err != nil {
		t.Fatalf("alice creating the room: %v", err)
	}
	if err := bobA.JoinRoom(0, room, "bob", ""); err != nil {
		t.Fatalf("bob joining: %v", err)
	}
	if _, err := bobA.RoomSettings(0, room); err == nil {
		t.Error("bob, not an owner, read the settings")
	}
	settings, err := aliceA.RoomSettings(0, room)
	if err != nil {
		t.Fatalf("reading the settings: %v", err)
	}
	have := map[string]ui.RoomSetting{}
	for _, s := range settings {
		have[s.Var] = s
	}
	t.Logf("settings: %v", slices.Sorted(maps.Keys(have)))
	for _, v := range []string{"muc#roomconfig_roomname", "muc#roomconfig_membersonly", "muc#roomconfig_whois"} {
		if _, ok := have[v]; !ok {
			t.Fatalf("no %s in %+v", v, settings)
		}
	}
	if resolveEncryptionMode(ctx, alice, room) != "none" {
		t.Fatal("an open room is encrypted")
	}
	if err := aliceA.SaveRoomSettings(0, room, map[string]string{
		"muc#roomconfig_roomname":    "Tea house",
		"muc#roomconfig_membersonly": "1",
		"muc#roomconfig_whois":       "anyone",
	}); err != nil {
		t.Fatalf("saving: %v", err)
	}
	if st, _ := alice.room(room); st.name != "Tea house" || !st.info.Private() {
		t.Errorf("after saving: name %q, info %+v", st.name, st.info)
	}
	if mode := resolveEncryptionMode(ctx, alice, room); mode != "omemo-v1" {
		t.Errorf("private room's encryption = %q", mode)
	}
	settings, _ = aliceA.RoomSettings(0, room)
	for _, s := range settings {
		if s.Var == "muc#roomconfig_membersonly" && s.Value != "1" {
			t.Errorf("members only read back as %q", s.Value)
		}
	}
}

// TestCreateRoomLive creates a private group inviting bob, who can then
// get in, and a public channel by its name's address.
func TestCreateRoomLive(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()
	aliceA, alice := newRoomTestAccount(ctx, t, tlsConfig)
	bobA, bob := newRoomTestAccount(ctx, t, tlsConfig)

	room, err := aliceA.CreateRoom(0, "Secret plans", "", false, []string{bob.account.JID})
	if err != nil {
		t.Fatalf("creating the group: %v", err)
	}
	st, _ := alice.room(room)
	if !strings.HasSuffix(room, "@conference.localhost") || st.name != "Secret plans" || !st.info.Private() || !st.bookmarked {
		t.Fatalf("group %s: %+v", room, st)
	}
	if mode := resolveEncryptionMode(ctx, alice, room); mode != "omemo-v1" {
		t.Errorf("group's encryption = %q", mode)
	}
	if err := bobA.JoinRoom(0, room, "bob", ""); err != nil {
		t.Errorf("bob, invited, joining: %v", err)
	}

	var b [4]byte
	_, _ = rand.Read(b[:])
	name := "Kage test " + hex.EncodeToString(b[:])
	channel, err := aliceA.CreateRoom(0, name, "", true, nil)
	if err != nil {
		t.Fatalf("creating the channel: %v", err)
	}
	if want := "kage-test-" + hex.EncodeToString(b[:]) + "@conference.localhost"; channel != want {
		t.Errorf("channel at %s, want %s", channel, want)
	}
	if st, _ := alice.room(channel); st.info.Private() || st.info.MembersOnly {
		t.Errorf("channel info %+v", st.info)
	}
	if _, err := aliceA.CreateRoom(0, name, "", true, nil); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("creating it again: %v", err)
	}
}
