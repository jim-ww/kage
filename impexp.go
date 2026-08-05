package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/crypto/localstore"
	"github.com/jim-ww/kage/storage"
)

// exportFormatVersion identifies the JSON schema of export files, so a
// future importer can tell an old export apart from a reshaped one.
const exportFormatVersion = 1

// exportReaction is one reactor's one emoji on an exported message.
type exportReaction struct {
	From  string `json:"from"`
	Emoji string `json:"emoji"`
}

// exportMessage is one messages row, decrypted to plaintext, with its
// reactions inlined.
type exportMessage struct {
	AccountJID   string           `json:"accountJID"`
	Sent         bool             `json:"sent"`
	To           string           `json:"to,omitempty"`
	From         string           `json:"from,omitempty"`
	ID           string           `json:"id,omitempty"`
	Body         string           `json:"body"`
	E2EEncrypted bool             `json:"e2eEncrypted"`
	E2EEMethod   string           `json:"e2eeMethod,omitempty"`
	OriginID     string           `json:"originID,omitempty"`
	StanzaType   string           `json:"stanzaType"`
	Received     bool             `json:"received"`
	Timestamp    time.Time        `json:"timestamp"`
	RosterJID    string           `json:"rosterJID,omitempty"`
	ArchiveID    string           `json:"archiveID,omitempty"`
	ReplyToID    string           `json:"replyToID,omitempty"`
	Retracted    bool             `json:"retracted"`
	Reactions    []exportReaction `json:"reactions,omitempty"`
}

// exportFile is the top-level shape of an export JSON document.
type exportFile struct {
	Version    int             `json:"version"`
	ExportedAt time.Time       `json:"exportedAt"`
	Messages   []exportMessage `json:"messages"`
}

// runExport writes every account's message history (decrypted to
// plaintext, regardless of how it's sealed at rest) to path as JSON.
func runExport(args []string) error {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	cfgPath := fs.String("c", "", "path to config")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: kage export [-c config] <output.json>")
	}
	outPath := fs.Arg(0)

	_, queries, localKey, closeDB, err := openStorageForCLI(*cfgPath)
	if err != nil {
		return err
	}
	defer closeDB()

	ctx := context.Background()
	rows, err := queries.ListAllMessages(ctx)
	if err != nil {
		return fmt.Errorf("listing messages: %w", err)
	}
	reactionRows, err := queries.ListAllReactions(ctx)
	if err != nil {
		return fmt.Errorf("listing reactions: %w", err)
	}

	// Group reactions by (accountJID, rosterJID, idAttr) so they can be
	// attached to their message below without one query per message.
	type reactionKey struct{ account, roster, id string }
	reactionsByMsg := make(map[reactionKey][]exportReaction, len(reactionRows))
	for _, r := range reactionRows {
		k := reactionKey{r.Accountjid, r.Rosterjid, r.Idattr}
		reactionsByMsg[k] = append(reactionsByMsg[k], exportReaction{From: r.Fromjid, Emoji: r.Emoji})
	}

	out := exportFile{Version: exportFormatVersion, ExportedAt: time.Now(), Messages: make([]exportMessage, 0, len(rows))}
	skipped := 0
	for _, r := range rows {
		body, err := decryptedBody(localKey, r.Body, r.Encrypted)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping message %s/%s (undecryptable): %v\n", r.Accountjid, r.Idattr.String, err)
			skipped++
			continue
		}
		out.Messages = append(out.Messages, exportMessage{
			AccountJID:   r.Accountjid,
			Sent:         r.Sent,
			To:           r.Toattr.String,
			From:         r.Fromattr.String,
			ID:           r.Idattr.String,
			Body:         body,
			E2EEncrypted: r.E2eencrypted,
			E2EEMethod:   r.E2eemethod.String,
			OriginID:     r.Originid.String,
			StanzaType:   r.Stanzatype,
			Received:     r.Received,
			Timestamp:    time.Unix(r.Delay, 0),
			RosterJID:    r.Rosterjid.String,
			ArchiveID:    r.Archiveid.String,
			ReplyToID:    r.Replytoidattr.String,
			Retracted:    r.Retracted,
			Reactions:    reactionsByMsg[reactionKey{r.Accountjid, r.Rosterjid.String, r.Idattr.String}],
		})
	}

	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("creating %s: %w", outPath, err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("writing %s: %w", outPath, err)
	}

	fmt.Printf("Exported %d messages to %s", len(out.Messages), outPath)
	if skipped > 0 {
		fmt.Printf(" (%d skipped, undecryptable)", skipped)
	}
	fmt.Println(".")
	return nil
}

// decryptedBody returns body in plaintext, decrypting it with localKey if
// encrypted is set. Mirrors readStoredBody's decrypt path but works from a
// bare row (no accountSession) since export reads across every account at
// once.
func decryptedBody(localKey []byte, body sql.NullString, encrypted bool) (string, error) {
	if !body.Valid {
		return "", nil
	}
	if !encrypted {
		return body.String, nil
	}
	if localKey == nil {
		return "", fmt.Errorf("message is encrypted but no local storage password is available")
	}
	return localstore.Open(localKey, body.String)
}

// runImport reads an export JSON file (see runExport) and writes every
// message back into local storage, sealing bodies under the current local
// storage key exactly like a live-received message would be (or storing
// plaintext if no local storage password is configured). Messages already
// present in storage (matched by archiveID, then by idAttr - the same
// dedup keys a MAM history sync checks in account.go) are skipped rather
// than inserted again, so re-running import, or running it before or after
// a MAM backfill of the same history, never produces duplicate rows.
func runImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	cfgPath := fs.String("c", "", "path to config")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: kage import [-c config] <input.json>")
	}
	inPath := fs.Arg(0)

	f, err := os.Open(inPath)
	if err != nil {
		return fmt.Errorf("opening %s: %w", inPath, err)
	}
	defer f.Close()
	var in exportFile
	if err := json.NewDecoder(f).Decode(&in); err != nil {
		return fmt.Errorf("parsing %s: %w", inPath, err)
	}

	_, queries, localKey, closeDB, err := openStorageForCLI(*cfgPath)
	if err != nil {
		return err
	}
	defer closeDB()

	ctx := context.Background()
	imported, skipped := 0, 0
	for _, m := range in.Messages {
		// Skip messages already in storage instead of inserting a duplicate
		// row - checked the same way a MAM backfill (account.go) dedupes
		// against the live path and earlier MAM pages, so importing twice,
		// or importing then later running a MAM history sync (or vice
		// versa), converges on one row per message instead of piling up
		// duplicates. archiveID is the stronger signal (server-assigned,
		// unique per account) so it's checked first; idAttr is the
		// fallback for messages that never had one (e.g. sent locally,
		// never MAM-fetched).
		exists := false
		if m.ArchiveID != "" {
			if ok, err := queries.MessageExistsByArchiveID(ctx, storage.MessageExistsByArchiveIDParams{
				AccountJid: m.AccountJID, ArchiveID: nullString(m.ArchiveID),
			}); err == nil && ok {
				exists = true
			}
		}
		if !exists && m.ID != "" {
			if ok, err := queries.MessageExistsByIDAttr(ctx, storage.MessageExistsByIDAttrParams{
				AccountJid: m.AccountJID, RosterJid: nullString(m.RosterJID), IDAttr: nullString(m.ID),
			}); err == nil && ok {
				exists = true
			}
		}
		if exists {
			skipped++
		} else {
			body, encrypted := sealForStorage(localKey, m.Body)
			if _, err := queries.InsertMessage(ctx, storage.InsertMessageParams{
				AccountJid:    m.AccountJID,
				Sent:          m.Sent,
				ToAttr:        nullString(m.To),
				FromAttr:      nullString(m.From),
				IDAttr:        nullString(m.ID),
				Body:          body,
				Encrypted:     encrypted,
				E2eEncrypted:  m.E2EEncrypted,
				E2eeMethod:    nullString(m.E2EEMethod),
				StanzaType:    m.StanzaType,
				OriginID:      nullString(m.OriginID),
				Delay:         m.Timestamp.Unix(),
				RosterJid:     nullString(m.RosterJID),
				ArchiveID:     nullString(m.ArchiveID),
				ReplyToIDAttr: nullString(m.ReplyToID),
			}); err != nil {
				return fmt.Errorf("importing message %s/%s: %w", m.AccountJID, m.ID, err)
			}
			imported++
		}
		if m.Retracted {
			if _, err := queries.MarkMessageRetracted(ctx, storage.MarkMessageRetractedParams{
				AccountJid: m.AccountJID, IDAttr: nullString(m.ID), RosterJid: nullString(m.RosterJID),
			}); err != nil {
				return fmt.Errorf("marking %s/%s retracted: %w", m.AccountJID, m.ID, err)
			}
		}
		// Grouped by reactor and replaced (delete then insert), same as
		// replaceReactions (history.go) does for the live/MAM path - XEP-0444
		// says a reactor's new <reactions/> stanza always replaces their
		// previous set, so re-importing an export taken before a reactor
		// removed/changed a reaction shouldn't leave the old one stuck
		// alongside it.
		byReactor := make(map[string][]string, len(m.Reactions))
		for _, r := range m.Reactions {
			byReactor[r.From] = append(byReactor[r.From], r.Emoji)
		}
		for from, emojis := range byReactor {
			if err := replaceReactions(ctx, &accountSession{account: config.Account{JID: m.AccountJID}, db: queries}, m.RosterJID, m.ID, from, emojis); err != nil {
				return fmt.Errorf("importing reactions on %s/%s: %w", m.AccountJID, m.ID, err)
			}
		}
	}

	fmt.Printf("Imported %d messages from %s", imported, inPath)
	if skipped > 0 {
		fmt.Printf(" (%d already present, skipped)", skipped)
	}
	fmt.Println(".")
	return nil
}

// sealForStorage mirrors crypto_helpers.go's encryptForStorage, but works
// without an accountSession since import runs before any account connects.
func sealForStorage(localKey []byte, plaintext string) (body sql.NullString, encrypted bool) {
	if localKey == nil {
		return sql.NullString{String: plaintext, Valid: true}, false
	}
	ct, err := localstore.Seal(localKey, plaintext)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: encrypting message for storage: %v\n", err)
		return sql.NullString{String: plaintext, Valid: true}, false
	}
	return sql.NullString{String: ct, Valid: true}, true
}

// openStorageForCLI loads config and opens the shared message database the
// same way main() does, for CLI commands that need storage access without
// starting the TUI or dialing any account.
func openStorageForCLI(cfgPath string) (cfg config.Config, queries *storage.Queries, localKey []byte, closeDB func(), err error) {
	cfg, err = config.Load(cfgPath)
	if err != nil {
		return config.Config{}, nil, nil, nil, err
	}
	dbPath, err := dataFilePath()
	if err != nil {
		return config.Config{}, nil, nil, nil, err
	}
	dbConn, queries, err := storage.Open(dbPath)
	if err != nil {
		return config.Config{}, nil, nil, nil, err
	}
	localKey, err = loadLocalKey(cfg.Storage, queries)
	if err != nil {
		dbConn.Close()
		return config.Config{}, nil, nil, nil, err
	}
	return cfg, queries, localKey, func() { dbConn.Close() }, nil
}
