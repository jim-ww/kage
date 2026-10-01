package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jim-ww/kage/call"
	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/storage"
)

// notifyEnabled mirrors !cfg.NotificationsDisabled for handleIncomingMessage's
// notify-or-not check (events.go) — only meaningful in --background mode;
// the TUI process never reads it.
var notifyEnabled atomic.Bool

// avatarsEnabled mirrors !cfg.AvatarsDisabled for the avatar sync
// (avatars.go), so the daemon doesn't fetch what nothing will render —
// same reasoning as notifyEnabled above.
var avatarsEnabled atomic.Bool

// hiddenChats/pinnedChats mirror cfg.State.HiddenChats/PinnedChats for the
// chat-list build (account.go), which has no other path back to the loaded
// state — same reasoning as notifyEnabled above. Both are local, reversible
// and invisible to the peer: the contact stays in the roster and messages
// keep arriving, the chat is just flagged so the TUI can keep it out of the
// list (hidden) or sort it to the top (pinned).
var (
	hiddenChats chatFlags
	pinnedChats chatFlags
)

// chatFlags is a per-account set of flagged chat addresses, keyed by account
// JID then lowercased bare JID.
type chatFlags struct {
	mu  sync.RWMutex
	set map[string]map[string]bool
}

// replace swaps the whole set in from the loaded state.
func (f *chatFlags) replace(state map[string][]string) {
	next := make(map[string]map[string]bool, len(state))
	for accountJID, addrs := range state {
		set := make(map[string]bool, len(addrs))
		for _, addr := range addrs {
			set[strings.ToLower(addr)] = true
		}
		next[accountJID] = set
	}
	f.mu.Lock()
	f.set = next
	f.mu.Unlock()
}

// flag records one chat's state, so a chat flagged this session keeps the
// flag for an account that reconnects without the daemon restarting.
func (f *chatFlags) flag(accountJID, chatAddress string, on bool) {
	key := strings.ToLower(chatAddress)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.set == nil {
		f.set = map[string]map[string]bool{}
	}
	if f.set[accountJID] == nil {
		f.set[accountJID] = map[string]bool{}
	}
	if on {
		f.set[accountJID][key] = true
		return
	}
	delete(f.set[accountJID], key)
}

func (f *chatFlags) has(accountJID, chatAddress string) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.set[accountJID][strings.ToLower(chatAddress)]
}

// videoQuality mirrors cfg.VideoQuality for beginScreenShareCapture
// (callsession.go), which has no other path back to the loaded config —
// same reasoning as notifyEnabled above.
var videoQuality atomic.Int32

func currentVideoQuality() call.VideoQuality {
	return call.VideoQuality(videoQuality.Load())
}

// defaultEncryptionMode mirrors cfg.DefaultEncryptionMode for
// resolveEncryptionMode (crypto_helpers.go) and account.go's chat-list
// fallback, which have no other path back to the loaded config — same
// reasoning as notifyEnabled above.
var defaultEncryptionMode atomic.Value // string

func currentDefaultEncryptionMode() string {
	mode, _ := defaultEncryptionMode.Load().(string)
	if mode == "" {
		return config.DefaultEncryptionMode
	}
	return mode
}

// clientFocus mirrors each attached TUI client's window focus and
// currently-open chat (see ui.FocusReporter / adapter.SetFocusState), read by
// handleIncomingMessage (events.go) to suppress a desktop notification for a
// message that's already visible on screen. Per client, not one global pair:
// with several TUIs attached, whichever reported last would otherwise speak
// for all of them, and a client quitting would leave its chat looking open
// (silently swallowing that chat's notifications) until the last one detached.
var clientFocus = newFocusRegistry()

// focusState is one client's last reported focus: whether its terminal has OS
// focus, and the focusedChatKey of the chat it has open ("" if none).
type focusState struct {
	focused bool
	chatKey string
}

type focusRegistry struct {
	mu     sync.Mutex
	states map[ipc.ClientID]focusState
}

func newFocusRegistry() *focusRegistry {
	return &focusRegistry{states: make(map[ipc.ClientID]focusState)}
}

func (r *focusRegistry) set(client ipc.ClientID, st focusState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states[client] = st
}

// forget drops a client's reported state, called as its connection goes away.
func (r *focusRegistry) forget(client ipc.ClientID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.states, client)
}

// chatVisible reports whether some attached, focused client has the chat
// identified by key on screen right now. An unreported or detached client
// counts for nothing: with nobody watching that chat, a notification is
// exactly what's wanted.
func (r *focusRegistry) chatVisible(key string) bool {
	if key == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, st := range r.states {
		if st.focused && st.chatKey == key {
			return true
		}
	}
	return false
}

// describe renders the registry for logging.
func (r *focusRegistry) describe() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.states) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(r.states))
	for client, st := range r.states {
		parts = append(parts, fmt.Sprintf("%d:focused=%t,chat=%q", client, st.focused, st.chatKey))
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

// focusedChatKey packs an account JID and chat address into focusState's
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
	startupStart := time.Now()
	notifyEnabled.Store(!cfg.NotificationsDisabled)
	avatarsEnabled.Store(!cfg.AvatarsDisabled)
	hiddenChats.replace(cfg.State.HiddenChats)
	pinnedChats.replace(cfg.State.PinnedChats)
	videoQuality.Store(int32(call.VideoQualityFromString(cfg.VideoQuality)))
	defaultEncryptionMode.Store(cfg.DefaultEncryptionMode)

	if !cfg.GPGDisabled {
		start := time.Now()
		ensureGPGKeys(&cfg)
		slog.Debug("background: ensureGPGKeys done", "elapsed", time.Since(start))
	}

	dbPath, err := dataFilePath()
	if err != nil {
		slog.Error("background: resolving db path", "err", err)
		return
	}
	start := time.Now()
	dbConn, queries, err := storage.Open(dbPath)
	if err != nil {
		slog.Error("background: opening storage", "err", err)
		return
	}
	slog.Debug("background: storage opened", "elapsed", time.Since(start))

	if !cfg.GPGDisabled {
		start = time.Now()
		primeGPGAgent(ctx, queries, cfg.Accounts)
		slog.Debug("background: primeGPGAgent done", "elapsed", time.Since(start))
	}

	start = time.Now()
	localKey, err := loadLocalKey(cfg.Storage, !cfg.KeyringDisabled, queries)
	if err != nil {
		slog.Error("background: deriving local key", "err", err)
		dbConn.Close()
		return
	}
	slog.Debug("background: local key derived", "elapsed", time.Since(start))

	srv := ipc.NewServer()
	a := &adapter{
		sessions:    make([]*accountSession, len(cfg.Accounts)),
		cfgAccounts: append([]config.Account(nil), cfg.Accounts...),
		cfgPath:     cfg.Path,
		db:          dbConn,
		queries:     queries,
		localKey:    localKey,
		useGPG:      !cfg.GPGDisabled,
		useKeyring:  !cfg.KeyringDisabled,
		srv:         srv,
	}
	srv.OnDisconnect = func(client ipc.ClientID) {
		// This TUI is gone: don't leave notifications suppressed for whatever
		// chat it had open/focused, and don't leave peers seeing a typing
		// indicator it never got around to clearing.
		clientFocus.forget(client)
		a.clearTyping(client)
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

	slog.Debug("background: ready to accept connections", "elapsed", time.Since(startupStart))

	for i, acct := range cfg.Accounts {
		go connectAndSuperviseAccount(ctx, srv, a, i, acct, queries, localKey)
	}
}

// Reload handles a SIGHUP: account add/remove/status changes are already
// applied live by the adapter methods that triggered them (AddAccount,
// RemoveAccount, SetAccountStatus all mutate sessions directly, in-process,
// since the RPC that changed them and the connections themselves now live
// in the same daemon) — this only needs to pick up config-level toggles
// like cfg.NotificationsDisabled.
func (b *backend) Reload(cfg config.Config) {
	notifyEnabled.Store(!cfg.NotificationsDisabled)
	avatarsEnabled.Store(!cfg.AvatarsDisabled)
	hiddenChats.replace(cfg.State.HiddenChats)
	pinnedChats.replace(cfg.State.PinnedChats)
	videoQuality.Store(int32(call.VideoQualityFromString(cfg.VideoQuality)))
	defaultEncryptionMode.Store(cfg.DefaultEncryptionMode)
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
