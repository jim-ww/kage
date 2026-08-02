package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"codeberg.org/jim-ww/kage/config"
	"codeberg.org/jim-ww/kage/crypto/gpg"
	"codeberg.org/jim-ww/kage/storage"
	"codeberg.org/jim-ww/kage/ui"
	"codeberg.org/jim-ww/kage/xmpp"
	"golang.org/x/term"
	"mellium.im/xmpp/jid"
)

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

// runSetupWizard interactively prompts for a JID and password on the
// terminal and writes a new account into the config file, so a first-time
// user doesn't have to hand-edit TOML. Tries the OS keyring first; if that
// fails (no Secret Service, etc.) it asks whether to fall back to a
// password_cmd or a plaintext password in the config file.
// ensureGPGKeys fills in GPGKeyID for any account that doesn't have one set,
// using gpg.DefaultSecretKeyID (the keyring's configured default-key, or its
// sole secret key) — and persists whatever it finds back to cfg.Path so this
// only has to happen once. Accounts it can't resolve (no default, multiple
// keys) are left alone; connectAccounts already warns about those.
func ensureGPGKeys(cfg *config.Config) {
	for i := range cfg.Accounts {
		acct := &cfg.Accounts[i]
		if acct.GPGKeyID != "" {
			continue
		}
		keyID, err := gpg.DefaultSecretKeyID()
		if err != nil {
			fmt.Fprintf(os.Stderr, "note: couldn't auto-detect a gpg key for %s: %v\n", acct.JID, err)
			continue
		}
		acct.GPGKeyID = keyID
		if err := config.SetAccountGPGKeyID(cfg.Path, acct.JID, keyID); err != nil {
			fmt.Fprintf(os.Stderr, "warning: detected gpg key %s for %s but couldn't save it to %s: %v\n", keyID, acct.JID, cfg.Path, err)
			continue
		}
		fmt.Printf("Using gpg key %s for %s (saved to %s).\n", keyID, acct.JID, cfg.Path)
	}
}

func runSetupWizard() error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("no accounts configured and not running interactively; add an [[accounts]] entry to config.toml yourself")
	}

	fmt.Println("No accounts configured yet — let's set one up.")

	reader := bufio.NewReader(os.Stdin)
	var addr string
	for {
		fmt.Print("XMPP address (e.g. user@example.com): ")
		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("reading input: %w", err)
		}
		addr = strings.TrimSpace(line)
		if _, err := jid.Parse(addr); err != nil {
			fmt.Printf("  %q doesn't look like a valid JID: %v\n", addr, err)
			continue
		}
		break
	}

	fmt.Print("Password: ")
	passBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return fmt.Errorf("reading password: %w", err)
	}
	password := string(passBytes)
	if password == "" {
		return fmt.Errorf("password cannot be empty")
	}

	acct := config.Account{JID: addr}

	if err := config.SetKeyringPassword(addr, password); err == nil {
		fmt.Println("Password stored in the OS keyring.")
	} else {
		fmt.Printf("Couldn't store the password in the OS keyring (%v).\n", err)
		fmt.Println("Fall back to: (1) a command that prints the password (password_cmd), or (2) storing it in plaintext in config.toml?")
		for {
			fmt.Print("[1/2]: ")
			line, err := reader.ReadString('\n')
			if err != nil {
				return fmt.Errorf("reading input: %w", err)
			}
			switch strings.TrimSpace(line) {
			case "1":
				fmt.Print("Command to print the password on stdout (e.g. `pass show xmpp/me`): ")
				cmdLine, err := reader.ReadString('\n')
				if err != nil {
					return fmt.Errorf("reading input: %w", err)
				}
				acct.PasswordCmd = strings.TrimSpace(cmdLine)
				if acct.PasswordCmd == "" {
					fmt.Println("  command cannot be empty")
					continue
				}
			case "2":
				fmt.Println("Warning: the password will be stored in plaintext in config.toml.")
				acct.Password = password
			default:
				fmt.Println("  please enter 1 or 2")
				continue
			}
			break
		}
	}

	path, err := config.DefaultWritePath()
	if err != nil {
		return fmt.Errorf("determining config path: %w", err)
	}
	if err := config.WriteAccount(path, acct); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	fmt.Printf("Account %s saved to %s.\n", addr, path)
	return nil
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
		if err := runSetupWizard(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		cfg, err = config.Load(*cfgPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if len(cfg.Accounts) == 0 {
			fmt.Fprintln(os.Stderr, "no accounts configured; add an [[accounts]] entry to config.toml")
			os.Exit(1)
		}
	}

	ensureGPGKeys(&cfg)

	ctx := context.Background()
	sessions, uiAccounts, err := connectAccounts(ctx, cfg.Accounts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sender := &adapter{sessions: sessions, cfgPath: cfg.Path}
	defer func() {
		sender.mu.Lock()
		defer sender.mu.Unlock()
		for _, s := range sender.sessions {
			s.client.Load().Close()
			s.dbConn.Close()
		}
	}()

	model := ui.New(uiAccounts, cfg.UI.KeyMap, cfg.UI.Theme, sender, sender)
	p := tea.NewProgram(model)
	sender.program = p

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
		sess, uiAcct, err := connectAccount(ctx, acct)
		if err != nil {
			for _, s := range sessions {
				s.client.Load().Close()
				s.dbConn.Close()
			}
			return nil, nil, err
		}
		sessions = append(sessions, sess)
		uiAccounts = append(uiAccounts, uiAcct)
	}

	return sessions, uiAccounts, nil
}

// connectAccount dials, opens storage, publishes our GPG key (if configured),
// and loads the roster + history for a single account.
func connectAccount(ctx context.Context, acct config.Account) (*accountSession, ui.Account, error) {
	password, err := acct.ResolvePassword()
	if err != nil {
		return nil, ui.Account{}, fmt.Errorf("account %s: %w", acct.JID, err)
	}

	var tlsConfig *tls.Config // nil: Dial's default verified config; future config.toml option could set a custom RootCAs pool here
	client, err := xmpp.Dial(ctx, acct.JID, password, tlsConfig)
	if err != nil {
		return nil, ui.Account{}, fmt.Errorf("account %s: %w", acct.JID, err)
	}

	dbPath, err := dataFilePath(acct.JID)
	if err != nil {
		client.Close()
		return nil, ui.Account{}, fmt.Errorf("account %s: %w", acct.JID, err)
	}
	db, queries, err := storage.Open(dbPath)
	if err != nil {
		client.Close()
		return nil, ui.Account{}, fmt.Errorf("account %s: %w", acct.JID, err)
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

	if acct.GPGKeyID != "" {
		publishOwnGPGKey(ctx, sess)
	}

	contacts, err := client.Roster(ctx)
	if err != nil {
		client.Close()
		db.Close()
		return nil, ui.Account{}, fmt.Errorf("account %s: fetching roster: %w", acct.JID, err)
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

	return sess, ui.Account{Name: acct.JID, Chats: chats, Messages: messages}, nil
}

// adapter implements ui.MessageSender and ui.AccountAdder, encrypting
// outgoing bodies when a peer key is configured and persisting sent messages
// to storage. sessions is only ever appended to (by AddAccount, from the
// Bubble Tea event loop's own goroutine via a tea.Cmd) after startup, so
// existing indices stay stable and Send doesn't need to hold mu itself —
// mu only guards the append plus the read of len(sessions) it races with.
type adapter struct {
	mu       sync.Mutex
	sessions []*accountSession
	cfgPath  string
	program  *tea.Program
}

// AddAccount implements ui.AccountAdder: resolves and stores the password in
// the OS keyring, persists the account to config.toml, connects it live, and
// starts its supervisor goroutine — mirroring what main does for accounts
// configured at startup, just for one account added mid-session.
func (a *adapter) AddAccount(jid, password, gpgKeyID string) tea.Msg {
	acct := config.Account{JID: jid, GPGKeyID: gpgKeyID}
	if password != "" {
		// Prefer the OS keyring; if that's unavailable (no Secret Service
		// running, headless box, etc.) fall back to storing the password in
		// plaintext in config.toml rather than failing the add outright.
		if err := config.SetKeyringPassword(jid, password); err != nil {
			acct.Password = password
			fmt.Fprintf(os.Stderr, "warning: storing password in keyring for %s: %v; falling back to plaintext in config\n", jid, err)
		}
	}
	if err := config.WriteAccount(a.cfgPath, acct); err != nil {
		return ui.AccountAddErrorMsg{Err: fmt.Errorf("saving account to config: %w", err)}
	}

	ctx := context.Background()
	sess, uiAcct, err := connectAccount(ctx, acct)
	if err != nil {
		return ui.AccountAddErrorMsg{Err: err}
	}

	a.mu.Lock()
	accountIdx := len(a.sessions)
	a.sessions = append(a.sessions, sess)
	a.mu.Unlock()

	go superviseAccount(ctx, a.program, accountIdx, sess)

	return ui.AccountAddedMsg{Account: uiAcct}
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

// publishOwnGPGKey exports our own public key and publishes it to our PEP
// nodes (XEP-0373) so contacts can discover it automatically instead of us
// having to hand them a fingerprint out of band. Best-effort: some servers
// don't support PEP or restrict node creation, so failure here just means
// contacts fall back to a manually configured gpg_peers entry, same as
// before this existed.
func publishOwnGPGKey(ctx context.Context, s *accountSession) {
	keyData, err := s.gpg.Export(s.account.GPGKeyID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: exporting own gpg key: %v\n", err)
		return
	}
	if err := s.client.Load().PublishOpenPGPKey(ctx, s.account.GPGKeyID, keyData); err != nil {
		fmt.Fprintf(os.Stderr, "warning: publishing gpg key via PEP (XEP-0373): %v\n", err)
		return
	}
}

// resolvePeerKey returns the GPG key fingerprint to encrypt to-messages with
// for peerJID, or "" if none is available. Order: an explicit gpg_peers
// override in config always wins (lets a user pin a specific key); then a
// previously discovered-and-cached fingerprint; then a live XEP-0373 PEP
// lookup, which gets cached in storage so it's a one-time cost per contact.
func resolvePeerKey(ctx context.Context, s *accountSession, peerJID string) string {
	if key := s.account.GPGPeers[peerJID]; key != "" {
		return key
	}

	if fpr, err := s.db.GetPGPPeerKey(ctx, peerJID); err == nil && fpr != "" {
		return fpr
	}

	fpr, err := discoverPeerKey(ctx, s, peerJID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "note: no gpg key found for %s (%v); sending unencrypted\n", peerJID, err)
		return ""
	}
	if err := s.db.UpsertPGPPeerKey(ctx, storage.UpsertPGPPeerKeyParams{Jid: peerJID, Fingerprint: fpr}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: caching discovered gpg key for %s: %v\n", peerJID, err)
	}
	return fpr
}

// discoverPeerKey fetches peerJID's most-recently-published OpenPGP key via
// their PEP nodes (XEP-0373) and imports it into the local keyring.
func discoverPeerKey(ctx context.Context, s *accountSession, peerJID string) (string, error) {
	client := s.client.Load()
	fprs, err := client.FetchOpenPGPFingerprints(ctx, peerJID)
	if err != nil {
		return "", err
	}
	if len(fprs) == 0 {
		return "", fmt.Errorf("no key published")
	}
	fpr := fprs[0]

	keyData, err := client.FetchOpenPGPKey(ctx, peerJID, fpr)
	if err != nil {
		return "", err
	}
	if err := s.gpg.Import(keyData, fpr); err != nil {
		return "", err
	}
	return fpr, nil
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
	replyTo := make([]string, 0, len(rows)) // parallel to msgs: each entry's ReplyToIdAttr, resolved to an index below
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
			ID:        row.Idattr.String,
			Author:    author,
			Content:   pt,
			SentAt:    time.Unix(row.Delay, 0),
			IsMe:      row.Sent,
			Retracted: row.Retracted,
			Reactions: loadReactionsForMessage(ctx, s, row.Idattr.String),
		})
		replyTo = append(replyTo, row.Replytoidattr.String)
	}

	// Resolve stored reply-target IDs into local indices now that the whole
	// slice (and thus every message's position) is known.
	for i, id := range replyTo {
		if idx := messageIndexByIDs(msgs, id); idx >= 0 {
			msgs[i].ReplyTo = &idx
		}
	}
	return msgs
}

// meReactorJID is the fromJID value used for our own account's rows in the
// messageReactions table — reactions are always local to one account's
// storage, so there's no ambiguity in using a fixed sentinel rather than the
// account's real JID.
const meReactorJID = "me"

// replaceReactions fully replaces reactorJID's reaction set on msgID with
// emojis, matching XEP-0444 semantics (a new reaction stanza always replaces
// the sender's previous set, never adds to it).
func replaceReactions(ctx context.Context, s *accountSession, msgID, reactorJID string, emojis []string) error {
	if err := s.db.DeleteReactionsByReactor(ctx, storage.DeleteReactionsByReactorParams{
		IDAttr: msgID, FromJid: reactorJID,
	}); err != nil {
		return err
	}
	for _, e := range emojis {
		if err := s.db.InsertReaction(ctx, storage.InsertReactionParams{
			IDAttr: msgID, FromJid: reactorJID, Emoji: e,
		}); err != nil {
			return err
		}
	}
	return nil
}

// loadReactionsForMessage aggregates all reactors' current sets on msgID into
// per-emoji counts, flagging whether our own account is among the reactors.
func loadReactionsForMessage(ctx context.Context, s *accountSession, msgID string) []ui.Reaction {
	rows, err := s.db.ListReactionsForMessage(ctx, msgID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: loading reactions for %s: %v\n", msgID, err)
		return nil
	}

	order := make([]string, 0, len(rows))
	counts := make(map[string]int, len(rows))
	mine := make(map[string]bool, len(rows))
	for _, row := range rows {
		if counts[row.Emoji] == 0 {
			order = append(order, row.Emoji)
		}
		counts[row.Emoji]++
		if row.Fromjid == meReactorJID {
			mine[row.Emoji] = true
		}
	}

	reactions := make([]ui.Reaction, len(order))
	for i, emoji := range order {
		reactions[i] = ui.Reaction{Emoji: emoji, Count: counts[emoji], Mine: mine[emoji]}
	}
	return reactions
}

// messageIndexByIDs returns the index of the message with the given stanza
// ID within msgs, or -1 if none matches (or id is empty). Same idea as
// ui.messageIndexByID, duplicated here since it's an unexported ui helper.
func messageIndexByIDs(msgs []ui.Message, id string) int {
	if id == "" {
		return -1
	}
	for i, msg := range msgs {
		if msg.ID == id {
			return i
		}
	}
	return -1
}

func (a *adapter) Send(accountIdx int, to, body string, opts ui.SendOptions) (string, error) {
	a.mu.Lock()
	valid := accountIdx >= 0 && accountIdx < len(a.sessions)
	var s *accountSession
	if valid {
		s = a.sessions[accountIdx]
	}
	a.mu.Unlock()
	if !valid {
		return "", fmt.Errorf("unknown account %d", accountIdx)
	}
	ctx := context.Background()

	if opts.ReactionTargetID != "" {
		id, err := s.client.Load().Send(ctx, to, "", xmpp.SendOptions{
			ReactionTargetID: opts.ReactionTargetID,
			Reactions:        opts.Reactions,
		})
		if err != nil {
			return "", err
		}
		if err := replaceReactions(ctx, s, opts.ReactionTargetID, meReactorJID, opts.Reactions); err != nil {
			fmt.Fprintf(os.Stderr, "warning: persisting our own reactions: %v\n", err)
		}
		return id, nil
	}

	if opts.RetractID != "" {
		// The retraction body is fixed fallback text, not user content —
		// nothing to encrypt for the peer. Once the peer's been told to
		// retract it, there's no reason to keep our own copy either.
		id, err := s.client.Load().Send(ctx, to, "", xmpp.SendOptions{RetractID: opts.RetractID})
		if err != nil {
			return "", err
		}
		if _, err := s.db.DeleteMessageByID(ctx, storage.DeleteMessageByIDParams{
			IDAttr:    nullString(opts.RetractID),
			RosterJid: nullString(to),
		}); err != nil {
			fmt.Fprintf(os.Stderr, "warning: deleting retracted message from storage: %v\n", err)
		}
		return id, nil
	}

	wireBody := body
	if peerKey := resolvePeerKey(ctx, s, to); peerKey != "" {
		ct, err := s.gpg.Encrypt(body, peerKey)
		if err != nil {
			return "", fmt.Errorf("encrypting to %s: %w", to, err)
		}
		wireBody = ct
	}

	id, err := s.client.Load().Send(ctx, to, wireBody, xmpp.SendOptions{
		ReplaceID:    opts.ReplaceID,
		ReplyToID:    opts.ReplyToID,
		QuotedAuthor: opts.QuotedAuthor,
		QuotedBody:   opts.QuotedBody,
	})
	if err != nil {
		return "", err
	}

	if opts.ReplaceID != "" {
		// A correction amends the original message in place; it isn't a new
		// row in history.
		if _, err := s.db.UpdateMessageBodyByID(ctx, storage.UpdateMessageBodyByIDParams{
			Body:      encryptForStorage(s, body),
			IDAttr:    nullString(opts.ReplaceID),
			RosterJid: nullString(to),
		}); err != nil {
			fmt.Fprintf(os.Stderr, "warning: persisting correction: %v\n", err)
		}
		return id, nil
	}

	if _, err := s.db.InsertMessage(ctx, storage.InsertMessageParams{
		Sent:          true,
		ToAttr:        nullString(to),
		IDAttr:        nullString(id),
		Body:          encryptForStorage(s, body),
		StanzaType:    "chat",
		RosterJid:     nullString(to),
		ReplyToIDAttr: nullString(opts.ReplyToID),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: persisting sent message: %v\n", err)
	}
	return id, nil
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
		switch ev := ev.(type) {
		case xmpp.PresenceEvent:
			p.Send(ui.PresenceMsg{
				AccountIdx: accountIdx,
				From:       bareJID(ev.From),
				Presence:   mapPresence(ev),
			})
			continue
		case xmpp.MessageEvent:
			handleIncomingMessage(ctx, p, accountIdx, s, ev)
		}
	}
}

// mapPresence reduces an xmpp.PresenceEvent to the UI's coarse online/away/
// offline distinction.
func mapPresence(ev xmpp.PresenceEvent) ui.Presence {
	if !ev.Available {
		return ui.PresenceOffline
	}
	switch ev.Show {
	case "away", "xa", "dnd":
		return ui.PresenceAway
	default:
		return ui.PresenceOnline
	}
}

// handleIncomingMessage decrypts, persists, and forwards a single incoming
// chat message — routing XEP-0308 corrections to storage.UpdateMessageBodyByID
// and a MessageCorrectedMsg instead of appending a new message, and XEP-0424
// retractions to a flag (never an actual delete — see ui.Message.Retracted).
func handleIncomingMessage(ctx context.Context, p *tea.Program, accountIdx int, s *accountSession, msgEv xmpp.MessageEvent) {
	if msgEv.ReactionTargetID != "" {
		from := bareJID(msgEv.From)
		if err := replaceReactions(ctx, s, msgEv.ReactionTargetID, from, msgEv.Reactions); err != nil {
			fmt.Fprintf(os.Stderr, "warning: persisting reactions from %s: %v\n", from, err)
		}
		p.Send(ui.MessageReactionsMsg{
			AccountIdx: accountIdx,
			From:       from,
			MessageID:  msgEv.ReactionTargetID,
			Reactions:  loadReactionsForMessage(ctx, s, msgEv.ReactionTargetID),
		})
		return
	}

	if msgEv.RetractID != "" {
		from := bareJID(msgEv.From)
		if _, err := s.db.MarkMessageRetracted(ctx, storage.MarkMessageRetractedParams{
			IDAttr:    nullString(msgEv.RetractID),
			RosterJid: nullString(from),
		}); err != nil {
			fmt.Fprintf(os.Stderr, "warning: persisting retraction flag: %v\n", err)
		}
		p.Send(ui.MessageRetractedMsg{
			AccountIdx: accountIdx,
			From:       from,
			RetractID:  msgEv.RetractID,
		})
		return
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

	from := bareJID(msgEv.From) // chats are keyed by bare JID (roster entries); From with a resource never matches

	if msgEv.ReplaceID != "" {
		if _, err := s.db.UpdateMessageBodyByID(ctx, storage.UpdateMessageBodyByIDParams{
			Body:      encryptForStorage(s, body),
			IDAttr:    nullString(msgEv.ReplaceID),
			RosterJid: nullString(from),
		}); err != nil {
			fmt.Fprintf(os.Stderr, "warning: persisting correction: %v\n", err)
		}
		p.Send(ui.MessageCorrectedMsg{
			AccountIdx: accountIdx,
			From:       from,
			ReplaceID:  msgEv.ReplaceID,
			NewContent: body,
		})
		return
	}

	if _, err := s.db.InsertMessage(ctx, storage.InsertMessageParams{
		Sent:          false,
		FromAttr:      nullString(msgEv.From),
		IDAttr:        nullString(msgEv.ID),
		Body:          encryptForStorage(s, body),
		StanzaType:    "chat",
		RosterJid:     nullString(from),
		ReplyToIDAttr: nullString(msgEv.ReplyToID),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: persisting received message: %v\n", err)
	}

	p.Send(ui.IncomingMessageMsg{
		AccountIdx: accountIdx,
		From:       from,
		ReplyToID:  msgEv.ReplyToID,
		Message: ui.Message{
			ID:      msgEv.ID,
			Author:  msgEv.From,
			Content: body,
			SentAt:  msgEv.SentAt,
			IsMe:    false,
		},
	})
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
