package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/ui"
)

// daemonServer answers calls from attached TUI clients (see ipc_rpc.go) by
// calling into adapter.
type daemonServer struct {
	a   *adapter
	srv *ipc.Server
}

// listAccounts builds a fresh snapshot of every configured account from
// local storage (reusing connectAccountLocal - a plain disk read, no
// network) so a newly-attached client, even one attaching well after the
// daemon finished its own boot sequence, gets accurate current state instead
// of stale "connecting…" placeholders.
func (d *daemonServer) listAccounts() []ui.Account {
	ctx := context.Background()
	d.a.mu.Lock()
	accts := append([]config.Account(nil), d.a.cfgAccounts...)
	sessions := append([]*accountSession(nil), d.a.sessions...)
	queries := d.a.queries
	localKey := d.a.localKey
	d.a.mu.Unlock()

	out := make([]ui.Account, len(accts))
	for i, acct := range accts {
		if i < len(sessions) && sessions[i] != nil {
			acct = sessions[i].account
		}
		// Per-account timing: this call is what a starting TUI waits on
		// before it can draw, so when a launch feels slow the answer is
		// almost always which account's local load took the time.
		acctStart := time.Now()
		_, uiAcct, err := connectAccountLocal(ctx, acct, queries, localKey)
		if err != nil {
			out[i] = ui.Account{Name: acct.JID, Alias: acct.Alias, ConnectError: err.Error()}
			continue
		}
		uiAcct.Connecting = false
		if i < len(sessions) && sessions[i] != nil {
			if client := sessions[i].client.Load(); client != nil && !client.Closed() {
				uiAcct.Status = accountStatus(sessions[i].account.Status)
				// Cache-only, never a live disco round trip: a TUI cannot
				// draw its first frame until this call returns, and asking
				// here cost up to the full disco timeout per account against
				// a slow server - seconds of blank terminal. connectAccountLive
				// warms this off the critical path, and an account still
				// connecting reports false here and is corrected by the
				// AccountConnectedMsg that follows.
				uiAcct.SupportsInvisible, _ = client.InvisibleSupportedCached()
			}
			// connectAccountLocal above rebuilds a fresh roster from disk
			// (no Presence there — that's live-only state), so pull current
			// presence from the live session's roster cache, kept up to
			// date by listen()'s PresenceEvent handling.
			if live := sessions[i].roster.Load(); live != nil {
				for ci, item := range uiAcct.Chats {
					chat, ok := item.(ui.Chat)
					if !ok {
						continue
					}
					if e, ok := (*live)[chat.Address]; ok {
						chat.Presence = e.Presence
						uiAcct.Chats[ci] = chat
					}
				}
			}
		}
		out[i] = uiAcct
		slog.Debug("listAccounts: account snapshot built", "jid", acct.JID, "chats", len(uiAcct.Chats), "elapsed", time.Since(acctStart))
	}

	// A TUI attach is the one moment we know a human is about to look at
	// this data, so it's also the cheapest reliable point to catch a gap
	// syncArchive otherwise only closes on a detected disconnect+reconnect
	// (see keepAlive/superviseAccount). A daemon process suspended by the
	// host (laptop sleep) rather than actually losing its TCP connection
	// never trips that path - the connection resumes working normally on
	// wake with no ping failure to react to, so a message a peer sent while
	// the process was frozen silently never arrives live and stays
	// invisible until something else happens to trigger a resync. Detached
	// and best-effort: this call already blocks on connectAccountLocal disk
	// reads above, and the archive backfill can take tens of seconds.
	//
	// Nothing stops several clients from attaching around the same time (or
	// one client calling this repeatedly) - triggerArchiveSync collapses
	// that into at most one backfill in flight per account rather than each
	// attach paging through every contact's archive concurrently.
	for i := range sessions {
		sess := sessions[i]
		if sess == nil {
			continue
		}
		client := sess.client.Load()
		if client == nil || client.Closed() {
			continue
		}
		go triggerArchiveSync(context.Background(), d.a.srv, i, sess)
	}

	return out
}

// callStatePriority ranks callState for callState's cross-account pick:
// higher wins. An unanswered incoming ring is ranked lowest - it's the call
// the attaching user is least likely to already be dealing with, whereas a
// call actively connecting or connected is almost certainly the one they
// care about (and, if it's their own outgoing call, the one whose account
// index every call-bar key/mouse action needs to target - see rejectCall's
// pre-proceed branch for what happens when the wrong one gets picked).
func callStatePriority(s callState) int {
	switch s {
	case callConnected:
		return 5
	case callNegotiating:
		return 4
	case callRingingRemote:
		return 3
	case callProposing:
		return 2
	case callRingingLocal:
		return 1
	default:
		return 0
	}
}

// callState reports whichever call is currently in progress across all
// accounts, if any (State "" if none). There's at most one call per
// account, but with more than one account configured, more than one can be
// active at once (e.g. two of the user's own accounts calling each other) -
// callStatePriority picks the one most likely to be what the attaching user
// actually cares about, since the UI only ever shows one call bar app-wide.
func (d *daemonServer) callState() ui.CallStateMsg {
	d.a.mu.Lock()
	sessions := append([]*accountSession(nil), d.a.sessions...)
	d.a.mu.Unlock()

	var best ui.CallStateMsg
	bestPriority := -1
	for _, sess := range sessions {
		if sess == nil {
			continue
		}
		c := sess.currentCall()
		if c == nil {
			continue
		}
		c.mu.Lock()
		state, connectedAt := c.state, c.connectedAt
		ev := c.stateEventLocked(state, "")
		c.mu.Unlock()
		if state == callIdle || state == callEnded {
			continue
		}
		if p := callStatePriority(state); p > bestPriority {
			bestPriority = p
			ev.StartedAt = connectedAt
			best = ev
		}
	}
	return best
}
