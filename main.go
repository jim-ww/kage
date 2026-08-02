package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"codeberg.org/jim-ww/kage/config"
	"codeberg.org/jim-ww/kage/crypto/gpg"
	"codeberg.org/jim-ww/kage/storage"
	"codeberg.org/jim-ww/kage/ui"
	"codeberg.org/jim-ww/kage/xmpp"
)

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func main() {
	cfgPath := flag.String("c", "", "path to config")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if len(cfg.Accounts) == 0 {
		fmt.Fprintln(os.Stderr, "no accounts configured; add an [[accounts]] entry to config.toml")
		os.Exit(1)
	}

	ctx := context.Background()
	sessions, uiAccounts, err := connectAccounts(ctx, cfg.Accounts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer func() {
		for _, s := range sessions {
			s.client.Close()
			s.dbConn.Close()
		}
	}()

	sender := &adapter{sessions: sessions}
	model := ui.New(uiAccounts, cfg.UI.KeyMap, cfg.UI.Theme, sender)
	p := tea.NewProgram(model)

	for i := range sessions {
		go listen(ctx, p, i, sessions[i])
	}

	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// accountSession bundles everything needed to send/receive/persist for one
// configured account.
type accountSession struct {
	account config.Account
	client  *xmpp.Client
	db      *storage.Queries
	dbConn  interface{ Close() error }
	gpg     gpg.Encrypter
}

func connectAccounts(ctx context.Context, accounts []config.Account) ([]*accountSession, []ui.Account, error) {
	sessions := make([]*accountSession, 0, len(accounts))
	uiAccounts := make([]ui.Account, 0, len(accounts))

	for _, acct := range accounts {
		password, err := acct.ResolvePassword()
		if err != nil {
			return nil, nil, fmt.Errorf("account %s: %w", acct.JID, err)
		}

		client, err := xmpp.Dial(ctx, acct.JID, password, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("account %s: %w", acct.JID, err)
		}

		dbPath, err := dataFilePath(acct.JID)
		if err != nil {
			client.Close()
			return nil, nil, fmt.Errorf("account %s: %w", acct.JID, err)
		}
		db, queries, err := storage.Open(dbPath)
		if err != nil {
			client.Close()
			return nil, nil, fmt.Errorf("account %s: %w", acct.JID, err)
		}

		if acct.GPGKeyID == "" {
			fmt.Fprintf(os.Stderr, "warning: account %s has no gpg_key_id configured; message bodies will NOT be persisted to disk (history is encryption-only)\n", acct.JID)
		}

		contacts, err := client.Roster(ctx)
		if err != nil {
			client.Close()
			db.Close()
			return nil, nil, fmt.Errorf("account %s: fetching roster: %w", acct.JID, err)
		}

		chats := make([]list.Item, 0, len(contacts))
		for _, c := range contacts {
			name := c.Name
			if name == "" {
				name = c.JID
			}
			chats = append(chats, ui.Chat{Name: name, Address: c.JID})
			if err := queries.UpsertRoster(ctx, storage.UpsertRosterParams{
				Jid: c.JID, Name: c.Name, Subs: c.Subscription,
			}); err != nil {
				fmt.Fprintf(os.Stderr, "warning: persisting roster entry %s: %v\n", c.JID, err)
			}
		}

		sessions = append(sessions, &accountSession{
			account: acct,
			client:  client,
			db:      queries,
			dbConn:  db,
			gpg:     gpg.Encrypter{},
		})
		uiAccounts = append(uiAccounts, ui.Account{
			Name:     acct.JID,
			Chats:    chats,
			Messages: make(map[int][]ui.Message),
		})
	}

	return sessions, uiAccounts, nil
}

// adapter implements ui.MessageSender, encrypting outgoing bodies when a
// peer key is configured and persisting sent messages to storage.
type adapter struct {
	sessions []*accountSession
}

// encryptForStorage encrypts plaintext to the account's own GPG key for
// at-rest storage (independent of any peer encryption used on the wire).
// Message content must never be written to disk unencrypted: if no key is
// configured or encryption fails, the body is dropped (NULL) rather than
// stored in the clear.
func encryptForStorage(s *accountSession, plaintext string) sql.NullString {
	if s.account.GPGKeyID == "" {
		return sql.NullString{}
	}
	ct, err := s.gpg.Encrypt(plaintext, s.account.GPGKeyID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: encrypting message for storage: %v\n", err)
		return sql.NullString{}
	}
	return sql.NullString{String: ct, Valid: true}
}

func (a *adapter) Send(accountIdx int, to, body string) error {
	if accountIdx < 0 || accountIdx >= len(a.sessions) {
		return fmt.Errorf("unknown account %d", accountIdx)
	}
	s := a.sessions[accountIdx]

	wireBody := body
	if peerKey := s.account.GPGPeers[to]; peerKey != "" {
		ct, err := s.gpg.Encrypt(body, peerKey)
		if err != nil {
			return fmt.Errorf("encrypting to %s: %w", to, err)
		}
		wireBody = ct
	}

	ctx := context.Background()
	if err := s.client.Send(ctx, to, wireBody); err != nil {
		return err
	}
	if _, err := s.db.InsertMessage(ctx, storage.InsertMessageParams{
		Sent:       true,
		ToAttr:     nullString(to),
		Body:       encryptForStorage(s, body),
		StanzaType: "chat",
	}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: persisting sent message: %v\n", err)
	}
	return nil
}

// listen bridges one account's xmpp events into the Bubble Tea program.
func listen(ctx context.Context, p *tea.Program, accountIdx int, s *accountSession) {
	for ev := range s.client.Events() {
		msgEv, ok := ev.(xmpp.MessageEvent)
		if !ok {
			continue
		}

		body := msgEv.Body
		if gpg.Looks(body) {
			pt, err := s.gpg.Decrypt(body, s.account.GPGPeers[msgEv.From])
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: decrypting message from %s: %v\n", msgEv.From, err)
			} else {
				body = pt
			}
		}

		if _, err := s.db.InsertMessage(ctx, storage.InsertMessageParams{
			Sent:       false,
			FromAttr:   nullString(msgEv.From),
			Body:       encryptForStorage(s, body),
			StanzaType: "chat",
		}); err != nil {
			fmt.Fprintf(os.Stderr, "warning: persisting received message: %v\n", err)
		}

		p.Send(ui.IncomingMessageMsg{
			AccountIdx: accountIdx,
			From:       msgEv.From,
			Message: ui.Message{
				Author:  msgEv.From,
				Content: body,
				SentAt:  msgEv.SentAt,
				IsMe:    false,
			},
		})
	}
}

func dataFilePath(jid string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "kage")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, strings.ReplaceAll(jid, "/", "_")+".db"), nil
}
