//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"path/filepath"
	"slices"
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
