package main

import (
	"testing"

	"github.com/jim-ww/kage/daemon"
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
