package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// TestAddColumnsOnExistingDatabase covers the upgrade path for a database
// created before a column existed. schema.sql's CREATE TABLE IF NOT EXISTS is
// a no-op once the table is there, so without the explicit ALTER in Open
// every already-installed client keeps the old table shape and every query
// naming the new column fails at runtime.
func TestAddColumnsOnExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	// Stand up mamSyncCursor exactly as it looked before lastSentAt, with a
	// row in it, the way a client that has been syncing for a while would.
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("opening legacy db: %v", err)
	}
	_, err = legacy.ExecContext(context.Background(), `
		CREATE TABLE mamSyncCursor (
			accountJID TEXT NOT NULL,
			rosterJID  TEXT NOT NULL,
			archiveID  TEXT NOT NULL,
			PRIMARY KEY (accountJID, rosterJID)
		) WITHOUT ROWID;
		INSERT INTO mamSyncCursor (accountJID, rosterJID, archiveID)
		VALUES ('me@example.com', 'peer@example.com', '1780827424946394');`)
	if err != nil {
		t.Fatalf("creating legacy schema: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("closing legacy db: %v", err)
	}

	db, q, err := Open(path)
	if err != nil {
		t.Fatalf("Open on legacy db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	cursors, err := q.ListMamSyncCursors(ctx, "me@example.com")
	if err != nil {
		t.Fatalf("ListMamSyncCursors: %v", err)
	}
	if len(cursors) != 1 {
		t.Fatalf("got %d cursors, want 1", len(cursors))
	}
	if cursors[0].Archiveid != "1780827424946394" {
		t.Errorf("archiveID = %q, want the pre-existing row preserved", cursors[0].Archiveid)
	}
	// A cursor persisted before the column existed reads back as "timestamp
	// unknown", which is what sends recoverMAMCursor down its re-walk path
	// rather than letting it think it has a real anchor.
	if cursors[0].Lastsentat != 0 {
		t.Errorf("lastSentAt = %d, want 0 for a pre-existing row", cursors[0].Lastsentat)
	}

	// Reopening must stay a no-op rather than erroring on the duplicate ALTER.
	db.Close()
	db2, _, err := Open(path)
	if err != nil {
		t.Fatalf("reopening an already-migrated db: %v", err)
	}
	db2.Close()
}

// TestMamSyncCursorRoundTrip checks the cursor carries its timestamp, which
// is the whole point of lastSentAt: syncArchiveForContact parks the cursor on
// archive items that never get a messages row, so the timestamp cannot be
// recovered by looking the archive id up in that table later.
func TestMamSyncCursorRoundTrip(t *testing.T) {
	db, q, err := Open(filepath.Join(t.TempDir(), "kage.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	upsert := func(archiveID string, sentAt int64) {
		t.Helper()
		if err := q.UpsertMamSyncCursor(ctx, UpsertMamSyncCursorParams{
			AccountJid: "me@example.com",
			RosterJid:  "peer@example.com",
			ArchiveID:  archiveID,
			LastSentAt: sentAt,
		}); err != nil {
			t.Fatalf("UpsertMamSyncCursor(%s): %v", archiveID, err)
		}
	}
	read := func() ListMamSyncCursorsRow {
		t.Helper()
		rows, err := q.ListMamSyncCursors(ctx, "me@example.com")
		if err != nil {
			t.Fatalf("ListMamSyncCursors: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("got %d cursors, want 1", len(rows))
		}
		return rows[0]
	}

	upsert("archive-1", 1754651899)
	if got := read(); got.Archiveid != "archive-1" || got.Lastsentat != 1754651899 {
		t.Fatalf("after insert: archiveID=%q lastSentAt=%d", got.Archiveid, got.Lastsentat)
	}

	// The upsert has to move both fields together - a stale timestamp left
	// behind next to a fresh archive id would anchor recovery at the wrong
	// point in the archive.
	upsert("archive-2", 1754738299)
	if got := read(); got.Archiveid != "archive-2" || got.Lastsentat != 1754738299 {
		t.Fatalf("after update: archiveID=%q lastSentAt=%d", got.Archiveid, got.Lastsentat)
	}
}
