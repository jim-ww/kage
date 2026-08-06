package main

import (
	"bufio"
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/crypto/gpg"
	"github.com/jim-ww/kage/crypto/localstore"
	"github.com/jim-ww/kage/notifyd"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/version"
	"golang.org/x/term"
	"mellium.im/xmpp/jid"
)

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

// joinOOBURLs/splitOOBURLs (de)serialize a message's XEP-0066 attachment
// URLs for the messages.oobURLs column - newline-separated, since a URL
// itself can't contain one.
func joinOOBURLs(urls []string) sql.NullString {
	return nullString(strings.Join(urls, "\n"))
}

func splitOOBURLs(s sql.NullString) []string {
	if !s.Valid || s.String == "" {
		return nil
	}
	return strings.Split(s.String, "\n")
}

// setupLog wires slog's default logger to <config dir>/kage/debug.log —
// always, regardless of -debug — so it survives the TUI owning the terminal
// (stderr isn't visible while bubbletea's alt screen is active). -debug only
// lowers the level from Warn to Debug; the log file itself is always written.
func setupLog(debug bool) {
	level := slog.LevelWarn
	if debug {
		level = slog.LevelDebug
	}

	dir, err := os.UserConfigDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: determining config dir for log file: %v\n", err)
		return
	}
	dir = filepath.Join(dir, "kage")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "warning: creating %s: %v\n", dir, err)
		return
	}
	path := filepath.Join(dir, "debug.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: opening %s: %v\n", path, err)
		return
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: level})))
	slog.Info("kage starting", "version", version.Version, "log_file", path)
}

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
// Only accounts with at least one chat actually set to gpg mode are primed,
// so accounts that never touch gpg (the default is omemo) never trigger a
// keyring/pinentry prompt.
func primeGPGAgent(ctx context.Context, queries *storage.Queries, accounts []config.Account) {
	e := gpg.Encrypter{}
	for _, acct := range accounts {
		if acct.GPGKeyID == "" {
			continue
		}
		if used, err := queries.HasGPGChat(ctx, acct.JID); err != nil || !used {
			continue
		}
		slog.Debug("primeGPGAgent: encrypting probe", "jid", acct.JID, "key", acct.GPGKeyID)
		start := time.Now()
		ct, err := e.Encrypt("kage startup probe", acct.GPGKeyID)
		if err != nil {
			slog.Warn("primeGPGAgent: encrypt failed", "jid", acct.JID, "elapsed", time.Since(start), "err", err)
			continue
		}
		if _, err := e.Decrypt(ct, ""); err != nil {
			slog.Warn("primeGPGAgent: decrypt failed", "jid", acct.JID, "elapsed", time.Since(start), "err", err)
			fmt.Fprintf(os.Stderr, "warning: unlocking gpg key for %s: %v\n", acct.JID, err)
		} else {
			slog.Debug("primeGPGAgent: unlocked", "jid", acct.JID, "elapsed", time.Since(start))
		}
	}
}

// runSetupWizard interactively prompts for a JID and password on the
// terminal and writes a new account into the config file, so a first-time
// user doesn't have to hand-edit TOML. Tries the OS keyring first; if that
// fails (no Secret Service, etc.) it asks whether to fall back to a
// password_cmd or a plaintext password in the config file.
func runSetupWizard(useKeyring bool) error {
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

	keyringErr := fmt.Errorf("use_keyring is disabled")
	if useKeyring {
		keyringErr = config.SetKeyringPassword(addr, password)
	}
	if keyringErr == nil {
		fmt.Println("Password stored in the OS keyring.")
	} else {
		if useKeyring {
			fmt.Printf("Couldn't store the password in the OS keyring (%v).\n", keyringErr)
		}
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
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "export":
			if err := runExport(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		case "import":
			if err := runImport(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		case "version":
			fmt.Println(version.Version)
			return
		}
	}

	cfgPath := flag.String("c", "", "path to config")
	debug := flag.Bool("debug", false, "log at debug level to <config dir>/kage/debug.log (warn level otherwise)")
	runNotifyd := flag.Bool("notifyd", false, "internal: run as the background notification daemon (spawned automatically, not meant to be passed by hand)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage:\n  %s [flags]\n  %s version\n  %s export [-c config] <output.json>\n  %s import [-c config] <input.json>\n\nFlags:\n", os.Args[0], os.Args[0], os.Args[0], os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	if *runNotifyd {
		cfg, err := config.Load(*cfgPath)
		if err != nil {
			log.Fatal(err)
		}
		if err := notifyd.Run(cfg); err != nil {
			log.Fatal(err)
		}
		return
	}

	setupLog(*debug)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if len(cfg.Accounts) == 0 {
		if err := runSetupWizard(cfg.UseKeyring); err != nil {
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

	if cfg.Notifications {
		if err := notifyd.EnsureRunning(cfg.Path); err != nil {
			fmt.Fprintf(os.Stderr, "warning: starting notification daemon: %v\n", err)
		}
	}

	if cfg.UseGPG {
		ensureGPGKeys(&cfg)
	}
	if cfg.HistoryPageSize > 0 {
		historyPageSize = cfg.HistoryPageSize
	}

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

	if cfg.UseGPG {
		primeGPGAgent(context.Background(), queries, cfg.Accounts)
	}

	localKey, err := loadLocalKey(cfg.Storage, cfg.UseKeyring, queries)
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
		uiAccounts[i] = ui.Account{Name: acct.JID, Alias: acct.Alias, Connecting: true, Status: accountStatus(acct.Status)}
	}
	sender := &adapter{sessions: make([]*accountSession, len(cfg.Accounts)), cfgPath: cfg.Path, queries: queries, localKey: localKey, useGPG: cfg.UseGPG, useKeyring: cfg.UseKeyring}
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

	openLastChatAddress := ""
	startAccountIdx := cfg.DefaultAccountIdx
	if cfg.UI.OpenLastChat && cfg.LastChatAddress != "" && cfg.LastChatAccountIdx >= 0 && cfg.LastChatAccountIdx < len(cfg.Accounts) {
		openLastChatAddress = cfg.LastChatAddress
		startAccountIdx = cfg.LastChatAccountIdx
	}
	display := ui.DisplayOptions{
		Icons:              cfg.UI.Icons,
		UseGPG:             cfg.UseGPG,
		ShowNames:          cfg.UI.ShowNames,
		TimeLayout:         cfg.UI.TimeLayout,
		TimeOnlyToday:      cfg.UI.TimeOnlyToday,
		MaxMessagesPerChat: cfg.MaxMessagesPerChat,
		NoticeDuration:     cfg.UI.NoticeDuration,
	}
	model := ui.New(uiAccounts, startAccountIdx, cfg.UI.KeyMap, cfg.UI.Theme, sender, sender, cfg.UI.Mouse, cfg.UI.SidebarWidth, cfg.UI.SidebarHidden, openLastChatAddress, cfg.UI.InputHeight, display)
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
func loadLocalKey(cfg config.StorageConfig, useKeyring bool, queries *storage.Queries) ([]byte, error) {
	password, configured, err := config.ResolveStoragePassword(cfg, useKeyring)
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
