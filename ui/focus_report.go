package ui

import (
	"log/slog"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
)

// focusReporter keeps SetFocusState reports in order. Each goes out from its
// own tea.Cmd goroutine and the daemon runs every call on its own goroutine,
// so two reports close together (blur then focus, or a chat switch) could
// land in either order and leave the daemon with the stale one. Reports are
// numbered and sent one at a time; one overtaken by a later report is
// dropped.
type focusReporter struct {
	mu   sync.Mutex
	next uint64 // last number handed out
	sent uint64 // last number sent
}

// report returns a Cmd sending this focus state, unless a later one has
// already been sent by the time it runs.
func (r *focusReporter) report(d Daemon, accountJID, chatAddress string, focused bool) tea.Cmd {
	if r == nil || d == nil {
		return nil
	}
	r.mu.Lock()
	r.next++
	seq := r.next
	r.mu.Unlock()
	return func() tea.Msg {
		r.mu.Lock()
		defer r.mu.Unlock()
		if seq < r.sent {
			return nil
		}
		r.sent = seq
		start := time.Now()
		if err := d.SetFocusState(accountJID, chatAddress, focused); err != nil {
			slog.Warn("ui: SetFocusState RPC failed", "err", err, "elapsed", time.Since(start))
		}
		return nil
	}
}
