package main

import (
	"context"
	"database/sql"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/storage"
)

// notifyEnabled mirrors cfg.Notifications for handleIncomingMessage's
// notify-or-not check (events.go) — only meaningful in --background mode;
// the TUI process never reads it.
var notifyEnabled atomic.Bool

// tuiFocused and tuiActiveChat mirror the attached TUI client's window
// focus and currently-open chat (see ui.FocusReporter / adapter.SetFocusState),
// read by handleIncomingMessage (events.go) to suppress a desktop
// notification for a message that's already visible on screen. Default to
// "focused, no chat open" so a client that never reports (or hasn't
// attached yet) doesn't suppress notifications it never actually saw.
var (
	tuiFocused    atomic.Bool
	tuiActiveChat atomic.Value // string: "accountJID\x00chatAddress", or "" if none
)

func init() {
	tuiFocused.Store(true)
	tuiActiveChat.Store("")
}

// focusedChatKey packs an account JID and chat address into tuiActiveChat's
// comparison key.
func focusedChatKey(accountJID, chatAddress string) string {
	if chatAddress == "" {
		return ""
	}
	return accountJID + "\x00" + chatAddress
}

// backend implements daemon.Backend: the daemon's real business logic —
// owning storage, every account's xmpp.Client (via adapter), and the ipc
// socket thin TUI clients attach to. daemon.Run constructs one, brings up
// the lock/tray/SIGHUP plumbing, then calls Start.
type backend struct {
	mu      sync.Mutex
	dbConn  *sql.DB
	adapter *adapter
	srv     *ipc.Server
	ln      net.Listener
}

func newBackend() *backend { return &backend{} }

func (b *backend) Start(ctx context.Context, cfg config.Config) {
	notifyEnabled.Store(cfg.Notifications)

	if cfg.UseGPG {
		ensureGPGKeys(&cfg)
	}

	dbPath, err := dataFilePath()
	if err != nil {
		slog.Error("background: resolving db path", "err", err)
		return
	}
	dbConn, queries, err := storage.Open(dbPath)
	if err != nil {
		slog.Error("background: opening storage", "err", err)
		return
	}

	if cfg.UseGPG {
		primeGPGAgent(ctx, queries, cfg.Accounts)
	}

	localKey, err := loadLocalKey(cfg.Storage, cfg.UseKeyring, queries)
	if err != nil {
		slog.Error("background: deriving local key", "err", err)
		dbConn.Close()
		return
	}

	srv := ipc.NewServer()
	a := &adapter{
		sessions:    make([]*accountSession, len(cfg.Accounts)),
		cfgAccounts: append([]config.Account(nil), cfg.Accounts...),
		cfgPath:     cfg.Path,
		db:          dbConn,
		queries:     queries,
		localKey:    localKey,
		useGPG:      cfg.UseGPG,
		useKeyring:  cfg.UseKeyring,
		srv:         srv,
	}
	ds := &daemonServer{a: a, srv: srv}

	sockPath, err := ipc.SocketPath()
	if err != nil {
		slog.Error("background: resolving socket path", "err", err)
		dbConn.Close()
		return
	}
	ln, err := ipc.Listen(sockPath)
	if err != nil {
		slog.Error("background: listening on socket", "err", err)
		dbConn.Close()
		return
	}

	b.mu.Lock()
	b.dbConn = dbConn
	b.adapter = a
	b.srv = srv
	b.ln = ln
	b.mu.Unlock()

	go func() {
		if err := srv.Accept(ln, ds.handle); err != nil {
			slog.Debug("background: socket accept loop ended", "err", err)
		}
	}()

	for i, acct := range cfg.Accounts {
		go connectAndSuperviseAccount(ctx, srv, a, i, acct, queries, localKey)
	}
}

// Reload handles a SIGHUP: account add/remove/status changes are already
// applied live by the adapter methods that triggered them (AddAccount,
// RemoveAccount, SetAccountStatus all mutate sessions directly, in-process,
// since the RPC that changed them and the connections themselves now live
// in the same daemon) — this only needs to pick up config-level toggles
// like cfg.Notifications.
func (b *backend) Reload(cfg config.Config) {
	notifyEnabled.Store(cfg.Notifications)
}

func (b *backend) Shutdown() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ln != nil {
		b.ln.Close()
	}
	if b.srv != nil {
		b.srv.Close()
	}
	if b.adapter != nil {
		b.adapter.mu.Lock()
		for _, s := range b.adapter.sessions {
			if s == nil {
				continue
			}
			if c := s.client.Load(); c != nil {
				c.Close()
			}
		}
		b.adapter.mu.Unlock()
	}
	if b.dbConn != nil {
		b.dbConn.Close()
	}
}
