package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/crypto/gpg"
	"github.com/jim-ww/kage/crypto/localstore"
	"github.com/jim-ww/kage/daemon"
	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/version"
	"github.com/spf13/cobra"
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

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

// newRootCmd builds kage's whole command tree: the bare TUI (default, no
// subcommand), export/import, version, and the daemon group (see
// cmd_daemon.go). Built fresh rather than package-level vars so nothing
// leaks state between invocations.
func newRootCmd() *cobra.Command {
	var cfgPath string
	var debug bool
	var debugXML bool

	root := &cobra.Command{
		Use:           "kage",
		Short:         "kage — a TUI XMPP client",
		Version:       version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(cfgPath, debug, debugXML)
		},
	}
	root.PersistentFlags().StringVarP(&cfgPath, "config", "c", "", "path to config")
	root.PersistentFlags().BoolVar(&debug, "debug", false, "log at debug level to <config dir>/kage/debug.log (warn level otherwise); also settable via KAGE_DEBUG env var or config.yaml's debug: true")
	root.PersistentFlags().BoolVar(&debugXML, "debug-xml", false, "log every decoded incoming/outgoing XMPP stanza to <config dir>/kage/xml.log — verbose, includes message content, for diagnosing interop issues with other clients")

	root.AddCommand(newExportCmd(&cfgPath))
	root.AddCommand(newImportCmd(&cfgPath))
	root.AddCommand(newDaemonCmd(&cfgPath, &debug, &debugXML))

	return root
}

// runTUI is what kage does with no subcommand: load config, run the setup
// wizard if no accounts exist yet, make sure the background daemon is up,
// and launch the Bubble Tea program.
func runTUI(cfgPath string, debug bool, debugXML bool) error {
	launchStart := time.Now()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	debug = debug || os.Getenv("KAGE_DEBUG") != "" || cfg.Debug
	setupLog(debug)
	slog.Debug("runTUI: config loaded", "elapsed", time.Since(launchStart))

	// No CLI setup wizard: with zero accounts configured, the TUI itself
	// opens straight into the add-account modal (login or register) so a
	// first-time user can set one up without ever leaving the app — see
	// startAddAccount below.

	// The background daemon always runs now — cfg.NotificationsDisabled only
	// gates whether it fires a desktop notification, not whether it starts at
	// all (see events.go's handleIncomingMessage).
	start := time.Now()
	if err := daemon.EnsureRunning(cfg.Path, debug, debugXML); err != nil {
		fmt.Fprintf(os.Stderr, "warning: starting kage's background service: %v\n", err)
	}
	slog.Debug("runTUI: daemon.EnsureRunning done", "elapsed", time.Since(start))
	if cfg.HistoryPageSize > 0 {
		historyPageSize = cfg.HistoryPageSize
	}

	sockPath, err := ipc.SocketPath()
	if err != nil {
		return err
	}
	client := newIPCClient()
	start = time.Now()
	conn, err := ipc.Dial(sockPath, client.handleEvent)
	if err != nil {
		return fmt.Errorf("connecting to kage's background service: %w", err)
	}
	slog.Debug("runTUI: ipc.Dial done", "elapsed", time.Since(start))
	client.conn = conn
	defer conn.Close()
	quitting := make(chan struct{})
	defer close(quitting)

	start = time.Now()
	uiAccounts, err := client.listAccounts()
	if err != nil {
		return err
	}
	slog.Debug("runTUI: listAccounts done", "elapsed", time.Since(start))
	// Best-effort: a call already in progress on the daemon just means the
	// status bar shows up a moment later, via the next live transition,
	// rather than not launching at all.
	start = time.Now()
	initialCallState, err := client.getCallState()
	if err != nil {
		initialCallState = nil
	}
	slog.Debug("runTUI: getCallState done", "elapsed", time.Since(start))

	openLastChatAddress := ""
	startAccountIdx := cfg.DefaultAccountIndex()
	lastChatAccountIdx := cfg.LastChatAccountIndex()
	if !cfg.OpenLastChatDisabled && cfg.LastChatAddress != "" && lastChatAccountIdx < len(cfg.Accounts) {
		openLastChatAddress = cfg.LastChatAddress
		startAccountIdx = lastChatAccountIdx
	}
	ui.AttachmentsDir = cfg.AttachmentsDir
	keyMap, err := cfg.ResolvedKeyMap()
	if err != nil {
		return err
	}
	display := ui.DisplayOptions{
		Icons:                   !cfg.IconsDisabled,
		UseGPG:                  !cfg.GPGDisabled,
		ShowEncryptedIcon:       cfg.ShowEncryptedIcon,
		DefaultEncryptionMode:   cfg.DefaultEncryptionMode,
		ShowNames:               cfg.ShowNames,
		TimeLayout:              cfg.TimeLayout,
		TimeOnlyToday:           !cfg.AlwaysShowFullDate,
		MaxMessagesPerChat:      cfg.MaxMessagesPerChat,
		NoticeDuration:          cfg.NoticeDurationValue(),
		FilePickerDirsFirst:     !cfg.FilePickerFilesFirst,
		FilePickerSortField:     cfg.FilePickerSortField,
		FilePickerSortAscending: cfg.FilePickerSortAscending,
	}
	model := ui.New(uiAccounts, startAccountIdx, keyMap, cfg.ResolvedTheme(), client, client, !cfg.MouseDisabled, cfg.SidebarWidth, cfg.SidebarHidden, openLastChatAddress, cfg.InputHeight, display, initialCallState)
	if len(uiAccounts) == 0 {
		// First run, or every account was removed: open straight into the
		// add-account modal (defaults to login mode; ctrl+r switches to
		// register) instead of a dead empty sidebar with no obvious way in.
		model = model.OpenAddAccountForm()
	}
	p := tea.NewProgram(model)
	client.setProgram(p)
	slog.Debug("runTUI: ready to start bubbletea program", "elapsed", time.Since(launchStart))

	// If the daemon goes away mid-session (crash, upgrade), don't leave the
	// TUI sitting on a dead connection — quit cleanly with a message rather
	// than hanging on every subsequent action. A live "reconnecting..."
	// banner is a nicer UX but out of scope for this pass.
	go func() {
		select {
		case <-conn.Done():
			fmt.Fprintln(os.Stderr, "kage's background service disconnected; please restart kage")
			p.Send(tea.Quit())
		case <-quitting:
		}
	}()

	finalModel, err := p.Run()
	// Flush any not-yet-autosaved compose text before exiting - the periodic
	// debounce (see ui.draftSaveDebounce) only fires after a few idle
	// seconds, so quitting right after typing would otherwise lose it.
	if fm, ok := finalModel.(ui.Model); ok {
		fm.FlushDraft()
	}
	return err
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
