package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jim-ww/kage/daemon"
	"github.com/jim-ww/kage/storage"
)

func TestUnreadBadgeTracksLastUnreadChat(t *testing.T) {
	tr := &unreadTracker{}
	t.Cleanup(func() { daemon.SetUnread(false) })

	tr.set("me@example.com", "alice@example.com", 2)
	if !daemon.Unread() {
		t.Fatal("badge off with an unread chat")
	}

	// A second account's chat keeps the badge lit after the first is read:
	// the dot goes off only when nothing anywhere is unread.
	tr.set("other@example.com", "bob@example.com", 1)
	tr.set("me@example.com", "alice@example.com", 0)
	if !daemon.Unread() {
		t.Fatal("badge off while another account still has unread messages")
	}

	tr.set("other@example.com", "bob@example.com", 0)
	if daemon.Unread() {
		t.Fatal("badge still on with everything read")
	}
}

func TestUnreadBadgeSeedAndForgetAccount(t *testing.T) {
	tr := &unreadTracker{}
	t.Cleanup(func() { daemon.SetUnread(false) })

	tr.seedAccount("me@example.com", map[string]int{"alice@example.com": 3, "bob@example.com": 0})
	if !daemon.Unread() {
		t.Fatal("badge off after seeding a nonzero count")
	}

	// Seeding is a replace, not a merge - storage is the truth every time a
	// client attaches.
	tr.seedAccount("me@example.com", map[string]int{})
	if daemon.Unread() {
		t.Fatal("badge still on after reseeding with no unread chats")
	}

	tr.set("me@example.com", "alice@example.com", 1)
	tr.forgetAccount("me@example.com")
	if daemon.Unread() {
		t.Fatal("badge still on after the account was removed")
	}
}

// A hidden chat has no row in any TUI's chat list, so nothing would ever
// call ResetChatUnread for it: a dot it lit could never be dismissed.
func TestUnreadBadgeIgnoresHiddenChats(t *testing.T) {
	tr := &unreadTracker{}
	t.Cleanup(func() { daemon.SetUnread(false) })
	const accountJID, peer = "me@example.com", "spam@example.com"
	hiddenChats.flag(accountJID, peer, true)
	t.Cleanup(func() { hiddenChats.flag(accountJID, peer, false) })

	tr.set(accountJID, peer, 4)
	if daemon.Unread() {
		t.Fatal("badge lit by a hidden chat")
	}
	tr.seedAccount(accountJID, map[string]int{peer: 4})
	if daemon.Unread() {
		t.Fatal("badge lit by a hidden chat on seed")
	}

	// Unhiding it brings the count back into play.
	hiddenChats.flag(accountJID, peer, false)
	tr.set(accountJID, peer, 4)
	if !daemon.Unread() {
		t.Fatal("badge off after the chat was unhidden with unread messages")
	}
}

// A count left behind for a contact whose roster entry is gone has no chat
// row to open, so it has to go with the entry rather than re-lighting the
// dot on every daemon start.
func TestForgetChatDropsStoredUnreadCount(t *testing.T) {
	dir := t.TempDir()
	dbConn, queries, err := storage.Open(filepath.Join(dir, "kage.db"))
	if err != nil {
		t.Fatalf("opening storage: %v", err)
	}
	t.Cleanup(func() { dbConn.Close() })
	t.Cleanup(func() { daemon.SetUnread(false) })

	ctx := context.Background()
	const accountJID, peer = "me@example.com", "gone@example.com"
	if err := queries.SetChatUnread(ctx, storage.SetChatUnreadParams{
		AccountJid: accountJID, RosterJid: peer, Count: 3,
	}); err != nil {
		t.Fatalf("SetChatUnread: %v", err)
	}
	unreadBadge.set(accountJID, peer, 3)
	t.Cleanup(func() { unreadBadge.forgetAccount(accountJID) })
	if !daemon.Unread() {
		t.Fatal("badge off with an unread chat")
	}

	forgetChat(ctx, queries, accountJID, peer)

	rows, err := queries.ListChatUnread(ctx, accountJID)
	if err != nil {
		t.Fatalf("ListChatUnread: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("unread rows after forgetChat = %v, want none", rows)
	}
	if daemon.Unread() {
		t.Fatal("badge still on after the chat was forgotten")
	}
}
