package main

import (
	"context"
	"crypto/tls"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

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
			s.client.Load().Close()
			s.dbConn.Close()
		}
	}()

	sender := &adapter{sessions: sessions}
	model := ui.New(uiAccounts, cfg.UI.KeyMap, cfg.UI.Theme, sender)
	p := tea.NewProgram(model)

	for i := range sessions {
		go superviseAccount(ctx, p, i, sessions[i])
	}

	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// accountSession bundles everything needed to send/receive/persist for one
// configured account. client is swapped out on reconnect, so it's stored
// behind an atomic pointer — Send (called from the Bubble Tea event loop)
// and the reconnect supervisor (its own goroutine) touch it concurrently.
type accountSession struct {
	account   config.Account
	client    atomic.Pointer[xmpp.Client]
	tlsConfig *tls.Config // reused on reconnect; nil means Dial's default verified config
	db        *storage.Queries
	dbConn    interface{ Close() error }
	gpg       gpg.Encrypter
}

func connectAccounts(ctx context.Context, accounts []config.Account) ([]*accountSession, []ui.Account, error) {
	sessions := make([]*accountSession, 0, len(accounts))
	uiAccounts := make([]ui.Account, 0, len(accounts))

	for _, acct := range accounts {
		password, err := acct.ResolvePassword()
		if err != nil {
			return nil, nil, fmt.Errorf("account %s: %w", acct.JID, err)
		}

		var tlsConfig *tls.Config // nil: Dial's default verified config; future config.toml option could set a custom RootCAs pool here
		client, err := xmpp.Dial(ctx, acct.JID, password, tlsConfig)
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

		sess := &accountSession{
			account:   acct,
			tlsConfig: tlsConfig,
			db:        queries,
			dbConn:    db,
			gpg:       gpg.Encrypter{},
		}
		sess.client.Store(client)

		contacts, err := client.Roster(ctx)
		if err != nil {
			client.Close()
			db.Close()
			return nil, nil, fmt.Errorf("account %s: fetching roster: %w", acct.JID, err)
		}

		chats := make([]list.Item, 0, len(contacts))
		messages := make(map[int][]ui.Message, len(contacts))
		for i, c := range contacts {
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
			if hist := loadHistory(ctx, sess, c.JID, name); len(hist) > 0 {
				messages[i] = hist
			}
		}

		sessions = append(sessions, sess)
		uiAccounts = append(uiAccounts, ui.Account{
			Name:     acct.JID,
			Chats:    chats,
			Messages: messages,
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

// bareJID strips the resource part (after "/") from a full JID, matching the
// bare-JID form roster entries and stored messages are keyed by.
func bareJID(addr string) string {
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		return addr[:i]
	}
	return addr
}

// loadHistory reads chatAddr's persisted history back from storage, decrypting
// each body with the account's own GPG key. Rows with no body (never
// persisted — no key configured at the time, or encryption failed) are
// skipped since there is nothing to recover.
func loadHistory(ctx context.Context, s *accountSession, chatAddr, chatName string) []ui.Message {
	rows, err := s.db.ListMessagesByRoster(ctx, storage.ListMessagesByRosterParams{
		Rosterjid: nullString(chatAddr),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: loading history for %s: %v\n", chatAddr, err)
		return nil
	}

	msgs := make([]ui.Message, 0, len(rows))
	for _, row := range rows {
		if !row.Body.Valid {
			continue
		}
		pt, err := s.gpg.Decrypt(row.Body.String, s.account.GPGKeyID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: decrypting history for %s: %v\n", chatAddr, err)
			continue
		}
		author := chatName
		if row.Sent {
			author = "me"
		}
		msgs = append(msgs, ui.Message{
			Author:  author,
			Content: pt,
			SentAt:  time.Unix(row.Delay, 0),
			IsMe:    row.Sent,
		})
	}
	return msgs
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
	if err := s.client.Load().Send(ctx, to, wireBody); err != nil {
		return err
	}
	if _, err := s.db.InsertMessage(ctx, storage.InsertMessageParams{
		Sent:       true,
		ToAttr:     nullString(to),
		Body:       encryptForStorage(s, body),
		StanzaType: "chat",
		RosterJid:  nullString(to),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: persisting sent message: %v\n", err)
	}
	return nil
}

// superviseAccount runs listen for s, and on an unexpected disconnect (the
// Events channel closing without Close having been called), reconnects with
// exponential backoff and resumes. Returns once the client is intentionally
// closed (app shutdown).
func superviseAccount(ctx context.Context, p *tea.Program, accountIdx int, s *accountSession) {
	for {
		listen(ctx, p, accountIdx, s)

		client := s.client.Load()
		if client.Closed() || ctx.Err() != nil {
			return
		}
		fmt.Fprintf(os.Stderr, "warning: account %s disconnected (%v); reconnecting...\n", s.account.JID, client.Err())
		reconnectWithBackoff(ctx, s)
	}
}

// reconnectWithBackoff retries Dial with exponential backoff (capped at 60s)
// until it succeeds or ctx is done, then stores the new client on s.
func reconnectWithBackoff(ctx context.Context, s *accountSession) {
	const maxBackoff = 60 * time.Second
	backoff := time.Second

	for {
		if ctx.Err() != nil {
			return
		}

		password, err := s.account.ResolvePassword()
		if err == nil {
			var client *xmpp.Client
			client, err = xmpp.Dial(ctx, s.account.JID, password, s.tlsConfig)
			if err == nil {
				s.client.Store(client)
				fmt.Fprintf(os.Stderr, "account %s reconnected\n", s.account.JID)
				return
			}
		}

		fmt.Fprintf(os.Stderr, "warning: reconnecting %s failed: %v; retrying in %s\n", s.account.JID, err, backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// listen bridges one account's xmpp events into the Bubble Tea program.
// Returns when the current client's Events channel closes (client swapped
// out from under it, e.g. by a reconnect, or the client was closed).
func listen(ctx context.Context, p *tea.Program, accountIdx int, s *accountSession) {
	for ev := range s.client.Load().Events() {
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
			RosterJid:  nullString(bareJID(msgEv.From)),
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
