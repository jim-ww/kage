package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/crypto/localstore"
	"github.com/jim-ww/kage/storage"
)

// ChangeStoragePassword implements ui.StoragePasswordChanger: re-encrypts
// every locally-encrypted message body and draft under a new password,
// derived from a freshly generated salt, then persists the new password
// (keyring if configured, else plaintext in config.yaml).
//
// Safety: the whole re-encryption (every row, plus the new salt) happens
// inside a single sqlite transaction — either every row ends up sealed under
// the new key and the new salt is committed together, or (on any error, mid-
// batch) nothing at all changes and the old password/salt are still valid.
// The new password is only written to keyring/config *after* that commit
// succeeds, so a crash or error can never leave the database re-encrypted
// under a password that was never actually saved anywhere. If persisting the
// new password itself fails (keyring and config.yaml write both fail, which
// would be unusual), the error says so explicitly rather than silently
// leaving the database unreadable on next launch.
//
// The running daemon (and every already-connected account session) still
// holds the *old* key in memory for the rest of this process's life —
// rather than hot-swap it live everywhere mid-session, this exits the
// daemon process shortly after returning success, so the next launch reads
// the new password/salt from scratch. main.go's TUI client already handles
// a dropped daemon connection with a "please restart kage" message, so this
// reuses that existing path instead of adding a second one.
func (a *adapter) ChangeStoragePassword(newPassword string) error {
	if a.db == nil || a.queries == nil {
		return fmt.Errorf("storage isn't available")
	}
	if newPassword == "" {
		return fmt.Errorf("password can't be empty")
	}

	ctx := context.Background()
	newSalt, err := localstore.NewSalt()
	if err != nil {
		return fmt.Errorf("generating new salt: %w", err)
	}
	newKey := localstore.DeriveKey(newPassword, newSalt)

	if err := rotateStorageKey(ctx, a.db, a.queries, a.localKey, newKey, newSalt); err != nil {
		return fmt.Errorf("re-encrypting local storage: %w", err)
	}

	if err := persistStoragePassword(a.cfgPath, a.useKeyring, newPassword); err != nil {
		// The database has already committed under newKey at this point -
		// this isn't a "nothing happened" failure, it's "re-encrypted but
		// nobody knows the password now", which is far worse than a normal
		// error. Say so explicitly.
		return fmt.Errorf("storage was re-encrypted successfully, but SAVING the new password failed (%w) - "+
			"set storage.password (or storage.password_cmd) in config.yaml to your new password manually, "+
			"or the database will be unreadable on next launch", err)
	}

	slog.Warn("local storage password changed; shutting down so the next launch uses it")
	go func() {
		// Give the RPC response carrying this success back to the TUI a
		// moment to actually reach the client before the socket drops out
		// from under it.
		time.Sleep(500 * time.Millisecond)
		a.db.Close()
		os.Exit(0)
	}()
	return nil
}

// rotateStorageKey does the actual decrypt-under-old/reseal-under-new sweep
// across every encrypted messages/chatDraft row plus the new salt, all
// inside one transaction — see ChangeStoragePassword's doc comment for the
// safety rationale.
func rotateStorageKey(ctx context.Context, db *sql.DB, queries *storage.Queries, oldKey, newKey, newSalt []byte) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once Commit has succeeded

	qtx := queries.WithTx(tx)

	msgs, err := qtx.ListEncryptedMessageBodies(ctx)
	if err != nil {
		return fmt.Errorf("listing encrypted messages: %w", err)
	}
	for _, m := range msgs {
		pt, err := localstore.Open(oldKey, m.Body.String)
		if err != nil {
			return fmt.Errorf("decrypting message %d under the current password: %w", m.ID, err)
		}
		ct, err := localstore.Seal(newKey, pt)
		if err != nil {
			return fmt.Errorf("re-encrypting message %d: %w", m.ID, err)
		}
		if err := qtx.UpdateMessageBodyByRowID(ctx, storage.UpdateMessageBodyByRowIDParams{
			ID:   m.ID,
			Body: sql.NullString{String: ct, Valid: true},
		}); err != nil {
			return fmt.Errorf("saving re-encrypted message %d: %w", m.ID, err)
		}
	}

	drafts, err := qtx.ListEncryptedChatDrafts(ctx)
	if err != nil {
		return fmt.Errorf("listing encrypted drafts: %w", err)
	}
	for _, d := range drafts {
		pt, err := localstore.Open(oldKey, d.Body)
		if err != nil {
			return fmt.Errorf("decrypting draft for %s under the current password: %w", d.Rosterjid, err)
		}
		ct, err := localstore.Seal(newKey, pt)
		if err != nil {
			return fmt.Errorf("re-encrypting draft for %s: %w", d.Rosterjid, err)
		}
		if err := qtx.SetChatDraft(ctx, storage.SetChatDraftParams{
			AccountJid: d.Accountjid,
			RosterJid:  d.Rosterjid,
			Body:       ct,
			Encrypted:  true,
		}); err != nil {
			return fmt.Errorf("saving re-encrypted draft for %s: %w", d.Rosterjid, err)
		}
	}

	if err := qtx.SetLocalKeySalt(ctx, newSalt); err != nil {
		return fmt.Errorf("saving new salt: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing: %w", err)
	}
	return nil
}

// persistStoragePassword saves password wherever ResolveStoragePassword will
// next look for it: the OS keyring if useKeyring is on, otherwise plaintext
// in config.yaml — same precedence ResolveStoragePassword reads back with.
// A keyring failure (no Secret Service running, etc.) falls back to the
// plaintext config write rather than erroring outright, same tolerance
// Account.ResolvePassword already extends elsewhere in this codebase.
func persistStoragePassword(cfgPath string, useKeyring bool, password string) error {
	if useKeyring {
		if err := config.SetStorageKeyringPassword(password); err == nil {
			return nil
		}
	}
	return config.SetStoragePlaintextPassword(cfgPath, password)
}
