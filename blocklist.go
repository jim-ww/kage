package main

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/xmpp"
)

// blocklistFetchTimeout bounds asking the server for the blocklist; one
// without XEP-0191 may never answer usefully.
const blocklistFetchTimeout = 10 * time.Second

// blockedSet is an account's server blocklist (XEP-0191) as last fetched
// or pushed. The server is what blocks; this is for showing it.
type blockedSet struct {
	mu   sync.Mutex
	jids map[string]bool
}

func (b *blockedSet) has(jid string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.jids[jid]
}

// all is every blocked JID.
func (b *blockedSet) all() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	jids := make([]string, 0, len(b.jids))
	for j := range b.jids {
		jids = append(jids, j)
	}
	return jids
}

// set marks jids blocked or not, returning those that changed.
func (b *blockedSet) set(jids []string, blocked bool) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.jids == nil {
		b.jids = make(map[string]bool)
	}
	var changed []string
	for _, j := range jids {
		if b.jids[j] == blocked {
			continue
		}
		if blocked {
			b.jids[j] = true
		} else {
			delete(b.jids, j)
		}
		changed = append(changed, j)
	}
	return changed
}

// replace makes jids the whole blocklist, returning what got blocked and
// what unblocked.
func (b *blockedSet) replace(jids []string) (blocked, unblocked []string) {
	var gone []string
	for _, j := range b.all() {
		if !slices.Contains(jids, j) {
			gone = append(gone, j)
		}
	}
	return b.set(jids, true), b.set(gone, false)
}

// broadcastBlocked tells every TUI jids got blocked, or unblocked.
func (s *accountSession) broadcastBlocked(srv *ipc.Server, jids []string, blocked bool) {
	for _, j := range jids {
		broadcast(srv, ui.ChatSettingMsg{AccountJID: s.account.JID, ChatAddress: j, Setting: ui.ChatBlocked, On: blocked})
	}
}

// refreshBlocklist fetches the blocklist on a new stream, which also
// subscribes it to the server's pushes of changes, and reports what
// changed since the last one.
func (s *accountSession) refreshBlocklist(ctx context.Context, srv *ipc.Server, client *xmpp.Client) {
	ctx, cancel := context.WithTimeout(ctx, blocklistFetchTimeout)
	defer cancel()
	jids, err := client.Blocklist(ctx)
	if err != nil {
		slog.Debug("fetching blocklist", "jid", s.account.JID, "err", err)
		return
	}
	blocked, unblocked := s.blocked.replace(jids)
	s.broadcastBlocked(srv, blocked, true)
	s.broadcastBlocked(srv, unblocked, false)
}

// applyBlocklistPush applies a change to the blocklist, made by any of
// our devices.
func (s *accountSession) applyBlocklistPush(srv *ipc.Server, ev xmpp.BlocklistPushEvent) {
	jids := ev.JIDs
	if !ev.Blocked && len(jids) == 0 {
		jids = s.blocked.all()
	}
	s.broadcastBlocked(srv, s.blocked.set(jids, ev.Blocked), ev.Blocked)
}

// SetBlocked blocks or unblocks address on the server.
func (a *adapter) SetBlocked(accountIdx int, address string, blocked bool) error {
	s, client, err := a.online(accountIdx)
	if err != nil {
		return err
	}
	if blocked {
		err = client.Block(context.Background(), address)
	} else {
		err = client.Unblock(context.Background(), address)
	}
	if err != nil {
		return err
	}
	// The server pushes the change to us too; whichever comes first.
	s.broadcastBlocked(a.srv, s.blocked.set([]string{address}, blocked), blocked)
	return nil
}
