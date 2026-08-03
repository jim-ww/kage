package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"database/sql"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/crypto/gpg"
	"github.com/jim-ww/kage/crypto/localstore"
	kageomemo "github.com/jim-ww/kage/crypto/omemo"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/xmpp"
	omemolib "github.com/jim-ww/omemo-go"
	"golang.org/x/term"
	"mellium.im/xmpp/jid"
)

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

// debugLog is nil (all debugf calls are no-ops) unless -debug is passed.
// Written to <config dir>/kage/debug.log so it survives the TUI owning the
// terminal — stderr isn't visible while bubbletea's alt screen is active.
var debugLog *log.Logger

func debugf(format string, args ...any) {
	if debugLog == nil {
		return
	}
	debugLog.Printf(format, args...)
}

// setupDebugLog opens (or creates) the debug log file and wires it up, both
// for this package's debugf and for crypto/gpg's Debugf hook.
func setupDebugLog() {
	dir, err := os.UserConfigDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: -debug: determining config dir: %v\n", err)
		return
	}
	dir = filepath.Join(dir, "kage")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "warning: -debug: creating %s: %v\n", dir, err)
		return
	}
	path := filepath.Join(dir, "debug.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: -debug: opening %s: %v\n", path, err)
		return
	}
	debugLog = log.New(f, "", log.LstdFlags|log.Lmicroseconds)
	gpg.Debugf = debugf
	debugf("=== kage starting, debug logging to %s ===", path)
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

// primeGPGAgent forces gpg-agent to unlock each configured account's own
// secret key once, synchronously, before the TUI takes over the terminal —
// a curses/tty pinentry prompt can't render once bubbletea owns the
// terminal, so any unlock needed for wire message crypto must happen here.
func primeGPGAgent(accounts []config.Account) {
	e := gpg.Encrypter{}
	for _, acct := range accounts {
		if acct.GPGKeyID == "" {
			continue
		}
		debugf("primeGPGAgent: %s: encrypting probe to %s", acct.JID, acct.GPGKeyID)
		start := time.Now()
		ct, err := e.Encrypt("kage startup probe", acct.GPGKeyID)
		if err != nil {
			debugf("primeGPGAgent: %s: encrypt failed after %s: %v", acct.JID, time.Since(start), err)
			continue
		}
		if _, err := e.Decrypt(ct, ""); err != nil {
			debugf("primeGPGAgent: %s: decrypt failed after %s: %v", acct.JID, time.Since(start), err)
			fmt.Fprintf(os.Stderr, "warning: unlocking gpg key for %s: %v\n", acct.JID, err)
		} else {
			debugf("primeGPGAgent: %s: unlocked in %s", acct.JID, time.Since(start))
		}
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
	debug := flag.Bool("debug", false, "write debug logs to <config dir>/kage/debug.log")
	flag.Parse()

	if *debug {
		setupDebugLog()
	}

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
	primeGPGAgent(cfg.Accounts)

	dbPath, err := dataFilePath()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	dbConn, queries, err := storage.Open(dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer dbConn.Close()

	localKey, err := loadLocalKey(cfg.Storage, queries)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	ctx := context.Background()

	// Dialing, roster fetch, and local-history decrypt all happen over the
	// network and can be slow — the UI is shown immediately with a
	// placeholder ("connecting...") row per account, and each account is
	// connected in its own goroutine, reporting back via AccountConnectedMsg/
	// AccountConnectErrorMsg once ready. sender.sessions is pre-sized so
	// indices assigned here stay stable no matter which account finishes
	// first.
	uiAccounts := make([]ui.Account, len(cfg.Accounts))
	for i, acct := range cfg.Accounts {
		uiAccounts[i] = ui.Account{Name: acct.JID, Connecting: true}
	}
	sender := &adapter{sessions: make([]*accountSession, len(cfg.Accounts)), cfgPath: cfg.Path, queries: queries, localKey: localKey}
	defer func() {
		sender.mu.Lock()
		defer sender.mu.Unlock()
		for _, s := range sender.sessions {
			if s == nil {
				continue
			}
			s.client.Load().Close()
		}
	}()

	model := ui.New(uiAccounts, cfg.DefaultAccountIdx, cfg.UI.KeyMap, cfg.UI.Theme, sender, sender, cfg.UI.Mouse)
	p := tea.NewProgram(model)
	sender.program = p

	for i, acct := range cfg.Accounts {
		go connectAndSuperviseAccount(ctx, p, sender, i, acct, queries, localKey)
	}

	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// loadLocalKey resolves the local storage password (config.ResolveStoragePassword:
// OS keyring, then password_cmd, then plaintext config) and derives the
// AES-256 key message bodies are sealed under at rest — one key, shared by
// every account, never itself persisted anywhere. The salt is not secret;
// it's stored in queries and generated once on first run. Returns a nil key
// (not an error) when no password is configured at all — messages are then
// stored in plain text; a configured-but-failing password_cmd is a real
// error, since that's a broken setup rather than a deliberate choice.
func loadLocalKey(cfg config.StorageConfig, queries *storage.Queries) ([]byte, error) {
	password, configured, err := config.ResolveStoragePassword(cfg)
	if err != nil {
		return nil, fmt.Errorf("resolving local storage password: %w", err)
	}
	if !configured {
		return nil, nil
	}

	ctx := context.Background()
	salt, err := queries.GetLocalKeySalt(ctx)
	if err != nil {
		salt, err = localstore.NewSalt()
		if err != nil {
			return nil, err
		}
		if err := queries.SetLocalKeySalt(ctx, salt); err != nil {
			return nil, fmt.Errorf("persisting local storage salt: %w", err)
		}
	}

	return localstore.DeriveKey(password, salt), nil
}

// connectAndSuperviseAccount loads one configured account's local
// roster/history from disk first — fast, no network — and reports it to the
// UI immediately so local chats/messages appear on screen right away. Only
// then does it dial and fetch the live roster in the background, and fall
// into the normal event-listen/reconnect supervisor loop.
func connectAndSuperviseAccount(ctx context.Context, p *tea.Program, a *adapter, idx int, acct config.Account, queries *storage.Queries, localKey []byte) {
	debugf("account %s: connectAccountLocal starting", acct.JID)
	start := time.Now()
	sess, uiAcct, err := connectAccountLocal(ctx, acct, queries, localKey)
	if err != nil {
		debugf("account %s: connectAccountLocal failed after %s: %v", acct.JID, time.Since(start), err)
		p.Send(ui.AccountConnectErrorMsg{Index: idx, Err: err})
		return
	}
	debugf("account %s: connectAccountLocal done in %s (%d chats)", acct.JID, time.Since(start), len(uiAcct.Chats))
	p.Send(ui.AccountConnectedMsg{Index: idx, Account: uiAcct})

	debugf("account %s: connectAccountLive starting", acct.JID)
	start = time.Now()
	newChats, newMessages, err := connectAccountLive(ctx, sess, len(uiAcct.Chats))
	if err != nil {
		debugf("account %s: connectAccountLive failed after %s: %v", acct.JID, time.Since(start), err)
		p.Send(ui.AccountConnectErrorMsg{Index: idx, Err: err})
		return
	}
	debugf("account %s: connectAccountLive done in %s (%d new chats)", acct.JID, time.Since(start), len(newChats))

	a.mu.Lock()
	a.sessions[idx] = sess
	a.mu.Unlock()

	p.Send(ui.AccountLiveMsg{Index: idx, NewChats: newChats, NewMessages: newMessages})

	superviseAccount(ctx, p, idx, sess)
}

// accountSession bundles everything needed to send/receive/persist for one
// configured account. client is swapped out on reconnect, so it's stored
// behind an atomic pointer — Send (called from the Bubble Tea event loop)
// and the reconnect supervisor (its own goroutine) touch it concurrently.
type accountSession struct {
	account   config.Account
	client    atomic.Pointer[xmpp.Client]
	tlsConfig *tls.Config      // reused on reconnect; nil means Dial's default verified config
	db        *storage.Queries // shared across every account: one database, rows scoped by account.JID
	gpg       gpg.Encrypter
	omemoMgr  *omemolib.Manager // nil until connectAccountLive sets it up (needs a dialed client for its Transport)

	// localKey is the AES-256 key message bodies are sealed under at rest
	// (crypto/localstore), derived once in main from the local storage
	// password and shared by every account.
	localKey []byte

	roster atomic.Pointer[map[string]rosterEntry] // bare JID -> cached roster entry, for display and RenameContact
}

// rosterEntry is a contact's cached roster state, refreshed at connect time
// and kept in sync locally by RenameContact.
type rosterEntry struct {
	Name string
	Subs string
}

// rosterName returns bareJID's roster nickname, or bareJID itself if the
// contact has none (or isn't in the roster at all).
func (s *accountSession) rosterName(bareJID string) string {
	entries := s.roster.Load()
	if entries == nil {
		return bareJID
	}
	if e, ok := (*entries)[bareJID]; ok && e.Name != "" {
		return e.Name
	}
	return bareJID
}

// connectAccountLocal loads acct's cached roster + history from the shared
// database — no network involved, so this is as fast as local SQLite reads
// and AES decrypts allow, letting local chats/messages appear instantly
// instead of waiting on connectAccountLive. The returned ui.Account has
// Connecting set; the caller clears it once connectAccountLive finishes.
func connectAccountLocal(ctx context.Context, acct config.Account, queries *storage.Queries, localKey []byte) (*accountSession, ui.Account, error) {
	sess := &accountSession{
		account:  acct,
		db:       queries,
		gpg:      gpg.Encrypter{},
		localKey: localKey,
	}

	rows, err := queries.ListRoster(ctx, acct.JID)
	if err != nil {
		return nil, ui.Account{}, fmt.Errorf("account %s: loading local roster: %w", acct.JID, err)
	}
	debugf("account %s: local roster has %d contacts", acct.JID, len(rows))

	chats := make([]list.Item, 0, len(rows))
	messages := make(map[int][]ui.Message, len(rows))
	entries := make(map[string]rosterEntry, len(rows))
	for i, r := range rows {
		name := r.Name
		if name == "" {
			name = r.Jid
		}
		entries[r.Jid] = rosterEntry{Name: r.Name, Subs: r.Subs}
		mode, err := queries.GetChatEncryptionMode(ctx, storage.GetChatEncryptionModeParams{AccountJid: acct.JID, RosterJid: r.Jid})
		if err != nil {
			mode = "omemo"
		}
		chats = append(chats, ui.Chat{Name: name, Address: r.Jid, EncryptionMode: mode})
		histStart := time.Now()
		hist := loadHistory(ctx, sess, r.Jid, name)
		debugf("account %s: loadHistory(%s) done in %s (%d messages)", acct.JID, r.Jid, time.Since(histStart), len(hist))
		if len(hist) > 0 {
			messages[i] = hist
		}
	}
	sess.roster.Store(&entries)

	return sess, ui.Account{Name: acct.JID, Chats: chats, Messages: messages, Connecting: true}, nil
}

// connectAccountLive dials sess's account, publishes our GPG key, and fetches
// the live roster, merging it into sess's already-loaded (by
// connectAccountLocal) roster cache. Existing contacts' chat/history entries
// are left completely untouched — they're already showing — only contacts
// the local snapshot didn't know about (added from another device) get a
// fresh history load here. The returned chats/messages are new
// entries only, with message indices relative to a Chats slice that starts
// right after existingChatCount, so the caller can append rather than
// replace what's already displayed.
func connectAccountLive(ctx context.Context, sess *accountSession, existingChatCount int) ([]list.Item, map[int][]ui.Message, error) {
	debugf("account %s: resolving password", sess.account.JID)
	start := time.Now()
	password, err := sess.account.ResolvePassword()
	if err != nil {
		return nil, nil, fmt.Errorf("account %s: %w", sess.account.JID, err)
	}
	debugf("account %s: password resolved in %s", sess.account.JID, time.Since(start))

	var tlsConfig *tls.Config // nil: Dial's default verified config; future config.toml option could set a custom RootCAs pool here
	debugf("account %s: dialing", sess.account.JID)
	start = time.Now()
	client, err := xmpp.Dial(ctx, sess.account.JID, password, tlsConfig)
	if err != nil {
		debugf("account %s: dial failed after %s: %v", sess.account.JID, time.Since(start), err)
		return nil, nil, fmt.Errorf("account %s: %w", sess.account.JID, err)
	}
	debugf("account %s: dialed in %s", sess.account.JID, time.Since(start))
	sess.tlsConfig = tlsConfig
	sess.client.Store(client)

	if sess.account.GPGKeyID != "" {
		debugf("account %s: publishOwnGPGKey starting", sess.account.JID)
		start = time.Now()
		publishOwnGPGKey(ctx, sess)
		debugf("account %s: publishOwnGPGKey done in %s", sess.account.JID, time.Since(start))
	}

	debugf("account %s: setupOmemo starting", sess.account.JID)
	start = time.Now()
	setupOmemo(ctx, sess)
	debugf("account %s: setupOmemo done in %s", sess.account.JID, time.Since(start))

	debugf("account %s: fetching live roster", sess.account.JID)
	start = time.Now()
	contacts, err := client.Roster(ctx)
	if err != nil {
		debugf("account %s: roster fetch failed after %s: %v", sess.account.JID, time.Since(start), err)
		client.Close()
		return nil, nil, fmt.Errorf("account %s: fetching roster: %w", sess.account.JID, err)
	}
	debugf("account %s: live roster fetched in %s (%d contacts)", sess.account.JID, time.Since(start), len(contacts))

	existing := sess.roster.Load()
	merged := make(map[string]rosterEntry, len(contacts))
	if existing != nil {
		for k, v := range *existing {
			merged[k] = v
		}
	}

	var newChats []list.Item
	newMessages := make(map[int][]ui.Message)
	for _, c := range contacts {
		name := c.Name
		if name == "" {
			name = c.JID
		}
		_, known := merged[c.JID]
		merged[c.JID] = rosterEntry{Name: c.Name, Subs: c.Subscription}
		if err := sess.db.UpsertRoster(ctx, storage.UpsertRosterParams{
			AccountJid: sess.account.JID, Jid: c.JID, Name: c.Name, Subs: c.Subscription,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "warning: persisting roster entry %s: %v\n", c.JID, err)
		}
		if known {
			continue
		}
		idx := existingChatCount + len(newChats)
		newChats = append(newChats, ui.Chat{Name: name, Address: c.JID})
		if hist := loadHistory(ctx, sess, c.JID, name); len(hist) > 0 {
			newMessages[idx] = hist
		}
	}
	sess.roster.Store(&merged)

	return newChats, newMessages, nil
}

// connectAccount runs connectAccountLocal then connectAccountLive back to
// back and merges the result into one ready-to-show ui.Account — used by
// AddAccount, where the account is brand new (no local history to show
// instantly) and the whole connect already runs off the Bubble Tea event
// loop via a tea.Cmd, so there's nothing to gain from doing it in two steps.
func connectAccount(ctx context.Context, acct config.Account, queries *storage.Queries, localKey []byte) (*accountSession, ui.Account, error) {
	sess, uiAcct, err := connectAccountLocal(ctx, acct, queries, localKey)
	if err != nil {
		return nil, ui.Account{}, err
	}

	newChats, newMessages, err := connectAccountLive(ctx, sess, len(uiAcct.Chats))
	if err != nil {
		return nil, ui.Account{}, err
	}

	uiAcct.Chats = append(uiAcct.Chats, newChats...)
	if uiAcct.Messages == nil {
		uiAcct.Messages = make(map[int][]ui.Message)
	}
	for idx, msgs := range newMessages {
		uiAcct.Messages[idx] = msgs
	}
	uiAcct.Connecting = false

	return sess, uiAcct, nil
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
	queries  *storage.Queries
	localKey []byte
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
	sess, uiAcct, err := connectAccount(ctx, acct, a.queries, a.localKey)
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

// encryptForStorage seals plaintext under the shared local storage key
// (independent of any peer encryption used on the wire). Message content
// must never be written to disk unencrypted: if sealing fails the body is
// dropped (NULL) rather than stored in the clear.
// encryptForStorage seals plaintext under the shared local storage key if
// one is configured; with no key (no local storage password set) the body
// is stored as-is, and encrypted comes back false.
func encryptForStorage(s *accountSession, plaintext string) (body sql.NullString, encrypted bool) {
	if s.localKey == nil {
		return sql.NullString{String: plaintext, Valid: true}, false
	}
	ct, err := localstore.Seal(s.localKey, plaintext)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: encrypting message for storage: %v\n", err)
		return sql.NullString{String: plaintext, Valid: true}, false
	}
	return sql.NullString{String: ct, Valid: true}, true
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

// setupOmemo loads (or, on first run, generates) this account's OMEMO
// identity, builds its Manager against the just-dialed client's Transport
// (XEP-0384 PEP bundle/device-list exchange), and publishes our bundle and
// device ID so contacts can start sessions with us. Trust is TOFU: any
// device we haven't seen before is trusted on first use, matching gpg's
// --trust-model always. Best-effort like publishOwnGPGKey — a failure here
// just means omemo-mode chats fall back to sending unencrypted (see
// resolveEncryptionMode/send).
func setupOmemo(ctx context.Context, s *accountSession) {
	store := kageomemo.NewStore(s.db, s.account.JID)

	if _, err := store.IdentityKeyPair(ctx); err != nil {
		var deviceID [4]byte
		if _, err := rand.Read(deviceID[:]); err != nil {
			fmt.Fprintf(os.Stderr, "warning: generating omemo device id for %s: %v\n", s.account.JID, err)
			return
		}
		id := omemolib.DeviceID(binary.BigEndian.Uint32(deviceID[:]))
		if err := omemolib.InitIdentity(ctx, store, s.account.JID, id); err != nil {
			fmt.Fprintf(os.Stderr, "warning: initializing omemo identity for %s: %v\n", s.account.JID, err)
			return
		}
	}

	client := s.client.Load()
	mgr, err := omemolib.NewManager(ctx, store, client.OmemoTransport(),
		omemolib.WithTrustResolver(func(context.Context, omemolib.Device, ed25519.PublicKey) error { return nil }))
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: setting up omemo for %s: %v\n", s.account.JID, err)
		return
	}
	s.omemoMgr = mgr

	if err := mgr.PublishBundle(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "warning: publishing omemo bundle for %s: %v\n", s.account.JID, err)
	}

	devices, err := client.FetchOmemoDeviceList(ctx, s.account.JID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: fetching own omemo device list for %s: %v\n", s.account.JID, err)
		return
	}
	local := mgr.LocalDevice().ID
	debugf("omemo setup: local device ID for %s: %d, current device list: %v", s.account.JID, local, devices.Devices)
	for _, id := range devices.Devices {
		if id == local {
			debugf("omemo setup: device %d already in list for %s", local, s.account.JID)
			return // already listed
		}
	}
	devices.JID = s.account.JID
	devices.Devices = append(devices.Devices, local)
	debugf("omemo setup: publishing device list for %s with devices: %v", s.account.JID, devices.Devices)
	if err := client.PublishOmemoDeviceList(ctx, devices); err != nil {
		fmt.Fprintf(os.Stderr, "warning: publishing omemo device list for %s: %v\n", s.account.JID, err)
	}
}

// resolveEncryptionMode returns the outgoing message encryption mode for
// peerJID ("omemo" the default, "gpg", or "none" if the user explicitly
// disabled encryption for this chat — see ui.ChatEncryptionSetter).
func resolveEncryptionMode(ctx context.Context, s *accountSession, peerJID string) string {
	mode, err := s.db.GetChatEncryptionMode(ctx, storage.GetChatEncryptionModeParams{
		AccountJid: s.account.JID,
		RosterJid:  peerJID,
	})
	if err != nil {
		return "omemo"
	}
	return mode
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

	if fpr, err := s.db.GetPGPPeerKey(ctx, storage.GetPGPPeerKeyParams{AccountJid: s.account.JID, Jid: peerJID}); err == nil && fpr != "" {
		return fpr
	}

	fpr, err := discoverPeerKey(ctx, s, peerJID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "note: no gpg key found for %s (%v); sending unencrypted\n", peerJID, err)
		return ""
	}
	if err := s.db.UpsertPGPPeerKey(ctx, storage.UpsertPGPPeerKeyParams{AccountJid: s.account.JID, Jid: peerJID, Fingerprint: fpr}); err != nil {
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

// loadHistory reads chatAddr's persisted history back from storage,
// decrypting each body with the local storage key (crypto/localstore).
// Rows with no body (encryption failed at write time) are skipped since
// there is nothing to recover.
// readStoredBody returns row's plaintext body, decrypting it if row.Encrypted
// is set — that fails if no local storage password is configured. A
// plaintext row read while a password *is* now available gets
// opportunistically re-sealed and written back, so it only sits unencrypted
// until the next time it's read.
func readStoredBody(ctx context.Context, s *accountSession, chatAddr string, row storage.ListMessagesByRosterRow) (string, error) {
	if !row.Encrypted {
		if s.localKey != nil {
			sealedBody, encrypted := encryptForStorage(s, row.Body.String)
			if _, err := s.db.UpdateMessageBodyByID(ctx, storage.UpdateMessageBodyByIDParams{
				AccountJid: s.account.JID,
				Body:       sealedBody,
				Encrypted:  encrypted,
				IDAttr:     row.Idattr,
				RosterJid:  nullString(chatAddr),
			}); err != nil {
				fmt.Fprintf(os.Stderr, "warning: encrypting stored message: %v\n", err)
			}
		}
		return row.Body.String, nil
	}
	if s.localKey == nil {
		return "", fmt.Errorf("message is encrypted but no local storage password is available")
	}
	return localstore.Open(s.localKey, row.Body.String)
}

func loadHistory(ctx context.Context, s *accountSession, chatAddr, chatName string) []ui.Message {
	rows, err := s.db.ListMessagesByRoster(ctx, storage.ListMessagesByRosterParams{
		AccountJid: s.account.JID,
		RosterJid:  nullString(chatAddr),
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
		pt, err := readStoredBody(ctx, s, chatAddr, row)
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
		AccountJid: s.account.JID, IDAttr: msgID, FromJid: reactorJID,
	}); err != nil {
		return err
	}
	for _, e := range emojis {
		if err := s.db.InsertReaction(ctx, storage.InsertReactionParams{
			AccountJid: s.account.JID, IDAttr: msgID, FromJid: reactorJID, Emoji: e,
		}); err != nil {
			return err
		}
	}
	return nil
}

// loadReactionsForMessage aggregates all reactors' current sets on msgID into
// per-emoji counts, flagging whether our own account is among the reactors.
func loadReactionsForMessage(ctx context.Context, s *accountSession, msgID string) []ui.Reaction {
	rows, err := s.db.ListReactionsForMessage(ctx, storage.ListReactionsForMessageParams{
		AccountJid: s.account.JID, IDAttr: msgID,
	})
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

// session returns the accountSession at accountIdx, guarded by mu since
// AddAccount appends to a.sessions concurrently with reads from here.
// SetDefaultAccount implements ui.DefaultAccountSetter: persists jid as the
// account selected on startup.
func (a *adapter) SetDefaultAccount(jid string) error {
	return config.SetDefaultAccount(a.cfgPath, jid)
}

func (a *adapter) session(accountIdx int) (*accountSession, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if accountIdx < 0 || accountIdx >= len(a.sessions) || a.sessions[accountIdx] == nil {
		return nil, false
	}
	return a.sessions[accountIdx], true
}

// SetTyping implements ui.MessageSender: sends a XEP-0085 chat state
// notification to "to" — no persistence, no encryption, it's ephemeral.
func (a *adapter) SetTyping(accountIdx int, to string, composing bool) error {
	s, ok := a.session(accountIdx)
	if !ok {
		return fmt.Errorf("unknown account %d", accountIdx)
	}
	state := xmpp.ChatStateActive
	if composing {
		state = xmpp.ChatStateComposing
	}
	return s.client.Load().SendChatState(context.Background(), to, state)
}

// RenameContact implements ui.ContactRenamer: pushes name as a roster set
// (RFC 6121) to the server, and mirrors it into local storage and the
// in-memory roster cache used by rosterName. An empty name clears the
// contact's nickname there too, matching what a fresh roster fetch would
// show after the server applies the same change.
func (a *adapter) RenameContact(accountIdx int, address, name string) error {
	s, ok := a.session(accountIdx)
	if !ok {
		return fmt.Errorf("unknown account %d", accountIdx)
	}

	if err := s.client.Load().SetRosterName(context.Background(), address, name); err != nil {
		return err
	}

	subs := ""
	if entries := s.roster.Load(); entries != nil {
		subs = (*entries)[address].Subs
	}
	if err := s.db.UpsertRoster(context.Background(), storage.UpsertRosterParams{
		AccountJid: s.account.JID, Jid: address, Name: name, Subs: subs,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: persisting renamed roster entry %s: %v\n", address, err)
	}

	updated := make(map[string]rosterEntry)
	if entries := s.roster.Load(); entries != nil {
		for k, v := range *entries {
			updated[k] = v
		}
	}
	updated[address] = rosterEntry{Name: name, Subs: subs}
	s.roster.Store(&updated)

	return nil
}

// SetChatEncryption implements ui.ChatEncryptionSetter: persists the chosen
// outgoing message encryption ("omemo", "gpg", or "none") for one chat.
func (a *adapter) SetChatEncryption(accountIdx int, peerJID, mode string) error {
	s, ok := a.session(accountIdx)
	if !ok {
		return fmt.Errorf("unknown account %d", accountIdx)
	}
	return s.db.SetChatEncryptionMode(context.Background(), storage.SetChatEncryptionModeParams{
		AccountJid: s.account.JID,
		RosterJid:  peerJID,
		Mode:       mode,
	})
}

func (a *adapter) Send(accountIdx int, to, body string, opts ui.SendOptions) (string, error) {
	return a.send(context.Background(), accountIdx, to, body, opts)
}

// send is the context-aware implementation behind Send. File uploads use it
// with their deadline so a subsequent peer-key lookup or stanza send cannot
// outlive the operation that initiated it.
func (a *adapter) send(ctx context.Context, accountIdx int, to, body string, opts ui.SendOptions) (string, error) {
	s, valid := a.session(accountIdx)
	if !valid {
		return "", fmt.Errorf("unknown account %d", accountIdx)
	}

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
			AccountJid: s.account.JID,
			IDAttr:     nullString(opts.RetractID),
			RosterJid:  nullString(to),
		}); err != nil {
			fmt.Fprintf(os.Stderr, "warning: deleting retracted message from storage: %v\n", err)
		}
		return id, nil
	}

	plaintext := body
	if opts.ReplyToID != "" {
		// Build quoted text for encrypted replies - the quoted text is inside the encrypted payload
		// Don't include author name in encrypted quotes (just the quoted content)
		lines := strings.Split(opts.QuotedBody, "\n")
		for i, l := range lines {
			lines[i] = "> " + l
		}
		quote := strings.Join(lines, "\n") + "\n"
		plaintext = quote + body
	}

	wireBody := plaintext
	sendOpts := xmpp.SendOptions{
		ReplaceID:    opts.ReplaceID,
		ReplyToID:    opts.ReplyToID,
		QuotedAuthor: opts.QuotedAuthor,
		QuotedBody:   opts.QuotedBody,
	}

	switch resolveEncryptionMode(ctx, s, to) {
	case "omemo":
		debugf("send: using omemo encryption for %s to %s", s.account.JID, to)
		if s.omemoMgr != nil {
			enc, deviceErrs, err := s.omemoMgr.EncryptMessage(ctx, to, []byte(plaintext))
			if err != nil {
				debugf("send: omemo encrypt failed for %s to %s: %v (device errors: %v)", s.account.JID, to, err, deviceErrs)
				return "", fmt.Errorf("omemo-encrypting to %s: %w", to, err)
			}
			debugf("send: omemo encrypt succeeded for %s to %s, %d keys", s.account.JID, to, len(enc.Keys))
			sendOpts.Encrypted = xmpp.EncodeOmemoMessage(enc)
			wireBody = ""
		} else {
			fmt.Fprintf(os.Stderr, "note: omemo not ready for %s; sending unencrypted\n", s.account.JID)
		}
	case "gpg":
		if peerKey := resolvePeerKey(ctx, s, to); peerKey != "" {
			ct, err := s.gpg.Encrypt(plaintext, peerKey)
			if err != nil {
				return "", fmt.Errorf("encrypting to %s: %w", to, err)
			}
			wireBody = ct
		}
	}

	id, err := s.client.Load().Send(ctx, to, wireBody, sendOpts)
	if err != nil {
		return "", err
	}

	if opts.ReplaceID != "" {
		// A correction amends the original message in place; it isn't a new
		// row in history.
		sealedBody, encrypted := encryptForStorage(s, body)
		if _, err := s.db.UpdateMessageBodyByID(ctx, storage.UpdateMessageBodyByIDParams{
			AccountJid: s.account.JID,
			Body:       sealedBody,
			Encrypted:  encrypted,
			IDAttr:     nullString(opts.ReplaceID),
			RosterJid:  nullString(to),
		}); err != nil {
			fmt.Fprintf(os.Stderr, "warning: persisting correction: %v\n", err)
		}
		return id, nil
	}

	sealedBody, encrypted := encryptForStorage(s, body)
	if _, err := s.db.InsertMessage(ctx, storage.InsertMessageParams{
		AccountJid:    s.account.JID,
		Sent:          true,
		ToAttr:        nullString(to),
		IDAttr:        nullString(id),
		Body:          sealedBody,
		Encrypted:     encrypted,
		StanzaType:    "chat",
		RosterJid:     nullString(to),
		ReplyToIDAttr: nullString(opts.ReplyToID),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: persisting sent message: %v\n", err)
	}
	return id, nil
}

// SendFile implements ui.FileSender. Uploading and the follow-up message send
// run as a Bubble Tea command, so the terminal remains responsive while a
// slot is discovered and the file is transferred.
func (a *adapter) SendFile(accountIdx int, to, path string) tea.Msg {
	result := ui.FileSendResultMsg{AccountIdx: accountIdx, To: to, Path: path}
	s, ok := a.session(accountIdx)
	if !ok {
		result.Err = fmt.Errorf("unknown account %d", accountIdx)
		return result
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	url, err := s.client.Load().UploadFile(ctx, path)
	if err != nil {
		result.Err = err
		return result
	}
	id, err := a.send(ctx, accountIdx, to, url, ui.SendOptions{})
	if err != nil {
		result.Err = err
		return result
	}
	result.URL = url
	result.ID = id
	return result
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
		case xmpp.ChatStateEvent:
			p.Send(ui.TypingMsg{
				AccountIdx: accountIdx,
				From:       bareJID(ev.From),
				Typing:     ev.State == xmpp.ChatStateComposing,
			})
			continue
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
			AccountJid: s.account.JID,
			IDAttr:     nullString(msgEv.RetractID),
			RosterJid:  nullString(from),
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
	if msgEv.Encrypted != nil {
		debugf("received omemo message from %s for %s", msgEv.From, s.account.JID)
		if s.omemoMgr == nil {
			fmt.Fprintf(os.Stderr, "warning: received omemo message from %s but omemo isn't ready for %s\n", msgEv.From, s.account.JID)
			return
		}
		enc, err := xmpp.DecodeOmemoMessage(msgEv.Encrypted, bareJID(msgEv.From))
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: decoding omemo message from %s: %v\n", msgEv.From, err)
			return
		}
		pt, err := s.omemoMgr.DecryptMessage(ctx, enc)
		if err != nil {
			debugf("decrypting omemo message from %s failed: %v", msgEv.From, err)
			fmt.Fprintf(os.Stderr, "warning: decrypting omemo message from %s: %v\n", msgEv.From, err)
			return
		}
		if pt == nil {
			debugf("omemo message from %s was key-transport only (no content)", msgEv.From)
			return // key-transport message: session established/refreshed, no content to show
		}
		debugf("omemo message from %s decrypted successfully", msgEv.From)
		body = string(pt)
	}
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
		sealedBody, encrypted := encryptForStorage(s, body)
		if _, err := s.db.UpdateMessageBodyByID(ctx, storage.UpdateMessageBodyByIDParams{
			AccountJid: s.account.JID,
			Body:       sealedBody,
			Encrypted:  encrypted,
			IDAttr:     nullString(msgEv.ReplaceID),
			RosterJid:  nullString(from),
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

	sealedBody, encrypted := encryptForStorage(s, body)
	if _, err := s.db.InsertMessage(ctx, storage.InsertMessageParams{
		AccountJid:    s.account.JID,
		Sent:          false,
		FromAttr:      nullString(msgEv.From),
		IDAttr:        nullString(msgEv.ID),
		Body:          sealedBody,
		Encrypted:     encrypted,
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
			ID:          msgEv.ID,
			Author:      s.rosterName(from),
			Content:     body,
			SentAt:      msgEv.SentAt,
			IsMe:        false,
			Attachments: attachmentURLs(body),
		},
	})
}

// attachmentURLs recognizes the URL-only message produced by SendFile. A
// plain link body remains visible as fallback text for every XMPP client,
// while Kage additionally exposes it as a downloadable attachment.
func attachmentURLs(body string) []string {
	body = strings.TrimSpace(body)
	if strings.HasPrefix(body, "https://") || strings.HasPrefix(body, "http://") {
		return []string{body}
	}
	return nil
}

// dataFilePath returns the single database file every configured account's
// data lives in, distinguished by an accountJID column on each table.
func dataFilePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "kage")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "kage.db"), nil
}
