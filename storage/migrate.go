package storage

import (
	"context"
	"database/sql"
	_ "embed" // for go:embed schema.sql below
	"fmt"
	"strings"

	_ "modernc.org/sqlite" // registers the sqlite database/sql driver
)

//go:embed schema.sql
var schema string

// Open opens (creating if necessary) the sqlite database at path and applies
// schema.sql, returning ready-to-use Queries.
func Open(path string) (*sql.DB, *Queries, error) {
	// Every account connects and syncs concurrently (see
	// connectAndSuperviseAccount), all writing to this one shared database.
	// _pragma query params (rather than a plain ExecContext) apply to every
	// pooled connection database/sql opens, not just the first — WAL lets
	// readers proceed alongside a writer instead of erroring immediately, and
	// busy_timeout makes a genuine writer/writer conflict retry for a while
	// instead of failing fast with SQLITE_BUSY. SetMaxOpenConns(1) on top
	// serializes all access through one connection, which for a small local
	// desktop database is simpler and cheaper than tuning around real
	// concurrent writers.
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, nil, fmt.Errorf("opening %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(context.Background(), "PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("enabling foreign keys: %w", err)
	}
	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("applying schema: %w", err)
	}
	if err := addColumns(context.Background(), db); err != nil {
		db.Close()
		return nil, nil, err
	}
	return db, New(db), nil
}

// addedColumns are columns added to a table that predates them. schema.sql's
// CREATE TABLE IF NOT EXISTS is a no-op on a database that already has the
// table, so a new column only reaches existing installs through an explicit
// ALTER - which sqlite has no "IF NOT EXISTS" form of, hence the
// already-applied error being tolerated below rather than tracked with a
// version number.
var addedColumns = []struct{ table, column, decl string }{
	{"mamSyncCursor", "lastSentAt", "INTEGER NOT NULL DEFAULT 0"},
}

func addColumns(ctx context.Context, db *sql.DB) error {
	for _, c := range addedColumns {
		_, err := db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", c.table, c.column, c.decl))
		if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("adding %s.%s: %w", c.table, c.column, err)
		}
	}
	return nil
}
