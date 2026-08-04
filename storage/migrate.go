package storage

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"

	_ "modernc.org/sqlite"
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
	// CREATE TABLE IF NOT EXISTS doesn't add columns to a table that already
	// exists from before this column was introduced, so add it explicitly —
	// ignoring the error when it's already there.
	db.ExecContext(context.Background(), "ALTER TABLE messages ADD COLUMN e2eEncrypted BOOLEAN NOT NULL DEFAULT FALSE")
	return db, New(db), nil
}
