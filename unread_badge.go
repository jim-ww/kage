package main

import (
	"sync"

	"github.com/jim-ww/kage/daemon"
)

// unreadBadge mirrors the persisted per-chat unread counts (chatUnread, which
// the attached TUIs own — see adapter.SetChatUnread) into the tray icon's
// unread dot, so the icon says "something is waiting" without the TUI having
// to be open at all.
//
// Counts are kept per account/chat rather than as a single boolean because the
// dot has to go off only when the *last* chat with unread messages is read,
// and no single event carries that fact.
var unreadBadge = &unreadTracker{}

type unreadTracker struct {
	mu sync.Mutex
	// accountJID -> chat address -> count, nonzero entries only.
	counts map[string]map[string]int
}

// set records one chat's current unread count (0 clears it).
func (t *unreadTracker) set(accountJID, chatAddress string, count int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.counts == nil {
		t.counts = map[string]map[string]int{}
	}
	if t.counts[accountJID] == nil {
		t.counts[accountJID] = map[string]int{}
	}
	if count > 0 {
		t.counts[accountJID][chatAddress] = count
	} else {
		delete(t.counts[accountJID], chatAddress)
	}
	t.applyLocked()
}

// seedAccount replaces everything known about accountJID with counts freshly
// read from storage, which is where connectAccountLocal already gets them:
// that runs at daemon startup and again on every TUI attach, so the badge
// re-syncs with the stored truth at both points instead of drifting from it.
func (t *unreadTracker) seedAccount(accountJID string, counts map[string]int) {
	next := make(map[string]int, len(counts))
	for addr, n := range counts {
		if n > 0 {
			next[addr] = n
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.counts == nil {
		t.counts = map[string]map[string]int{}
	}
	t.counts[accountJID] = next
	t.applyLocked()
}

// forgetAccount drops a removed account's counts, which would otherwise keep
// the dot lit for chats that no longer exist anywhere in the UI.
func (t *unreadTracker) forgetAccount(accountJID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.counts, accountJID)
	t.applyLocked()
}

func (t *unreadTracker) applyLocked() {
	any := false
	for _, chats := range t.counts {
		if len(chats) > 0 {
			any = true
			break
		}
	}
	daemon.SetUnread(any)
}
