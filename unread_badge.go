package main

import (
	"context"
	"log/slog"
	"sync"

	"github.com/jim-ww/kage/daemon"
	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
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
	if count > 0 && badgeable(accountJID, chatAddress) {
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
		if n > 0 && badgeable(accountJID, addr) {
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

// badgeable reports whether a chat's unread count has any business lighting
// the tray dot. A hidden chat doesn't: the TUI keeps it out of the chat list
// entirely (ui/hidden_chats.go), so there is no row to open and nothing that
// would ever call ResetChatUnread for it - a dot it lit would stay lit
// forever. The stored count itself is kept, so unhiding the chat brings it
// back.
func badgeable(accountJID, chatAddress string) bool {
	return !hiddenChats.has(accountJID, chatAddress)
}

// forgetChat drops one chat's unread count for good - both the stored row
// and the badge entry - for a chat that's ceasing to exist (its roster entry
// is being deleted). Without it the row outlives every chat row built from
// the roster, and seedAccount keeps re-lighting the dot from it on every
// daemon start and TUI attach.
func forgetChat(ctx context.Context, db *storage.Queries, accountJID, chatAddress string) {
	if err := db.DeleteChatUnread(ctx, storage.DeleteChatUnreadParams{
		AccountJid: accountJID,
		RosterJid:  chatAddress,
	}); err != nil {
		slog.Warn("deleting unread count for removed chat", "jid", accountJID, "peer", chatAddress, "err", err)
	}
	unreadBadge.set(accountJID, chatAddress, 0)
}

// forgetAccount drops a removed account's counts, which would otherwise keep
// the dot lit for chats that no longer exist anywhere in the UI.
func (t *unreadTracker) forgetAccount(accountJID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.counts, accountJID)
	t.applyLocked()
}

// countUnreadWhileDetached counts msgs toward chatAddress's persisted unread
// total when no TUI is attached. The attached TUIs normally own that total
// (they're the ones that know which chat is actually being looked at, and
// they write it back absolute — see adapter.SetChatUnread), so this does
// nothing while any of them is connected; with none, everything that arrives
// would otherwise be silently read-on-arrival, leaving nothing for the tray
// dot to show and nothing for the next TUI to open to.
//
// Applies the same rule the UI does for messages it receives while a chat
// isn't focused (ui/update_messages.go): our own messages and ones that
// failed to decrypt don't count.
func countUnreadWhileDetached(ctx context.Context, srv *ipc.Server, s *accountSession, chatAddress string, msgs []ui.Message) {
	if srv == nil || srv.ClientCount() > 0 {
		return
	}
	delta := 0
	for _, m := range msgs {
		if !m.IsMe && !m.DecryptFailed {
			delta++
		}
	}
	if delta == 0 {
		return
	}
	count, err := s.db.BumpChatUnread(ctx, storage.BumpChatUnreadParams{
		AccountJid: s.account.JID,
		RosterJid:  chatAddress,
		Delta:      int64(delta),
	})
	if err != nil {
		slog.Warn("counting unread messages received while detached", "jid", s.account.JID, "peer", chatAddress, "err", err)
		return
	}
	unreadBadge.set(s.account.JID, chatAddress, int(count))
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
