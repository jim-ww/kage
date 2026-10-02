package storage

import (
	"context"
	"path/filepath"
	"testing"
)

// BumpChatUnread's upsert adds its delta on conflict (via excluded.count) and
// hands back the running total, which is what the daemon stores in the tray
// badge - a plain INSERT-or-replace here would silently reset the count every
// time instead of accumulating.
func TestBumpChatUnreadAccumulates(t *testing.T) {
	db, q, err := Open(filepath.Join(t.TempDir(), "kage.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	bump := func(delta int64) int64 {
		t.Helper()
		count, err := q.BumpChatUnread(ctx, BumpChatUnreadParams{
			AccountJid: "me@example.com",
			RosterJid:  "peer@example.com",
			Delta:      delta,
		})
		if err != nil {
			t.Fatalf("BumpChatUnread(%d): %v", delta, err)
		}
		return count
	}

	if got := bump(1); got != 1 {
		t.Fatalf("first bump = %d, want 1", got)
	}
	if got := bump(3); got != 4 {
		t.Fatalf("second bump = %d, want 4", got)
	}

	if err := q.ResetChatUnread(ctx, ResetChatUnreadParams{
		AccountJid: "me@example.com",
		RosterJid:  "peer@example.com",
	}); err != nil {
		t.Fatalf("ResetChatUnread: %v", err)
	}
	if got := bump(2); got != 2 {
		t.Fatalf("bump after reset = %d, want 2", got)
	}
}
