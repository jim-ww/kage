package notifyd

import (
	"context"
	"crypto/tls"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"fyne.io/systray"
	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/crypto/gpg"
	"github.com/jim-ww/kage/xmpp"
)

// Run is the notifyd entry point: acquires the single-instance lock, brings
// up a tray icon (with a Quit item), and connects every configured account
// read-only, purely to fire a desktop notification on each new incoming
// message. Blocks until the tray is quit (via the menu, or the OS signaling
// the session is ending); returns nil if another notifyd already holds the
// lock, so callers can always invoke this unconditionally in -notifyd mode.
func Run(cfg config.Config) error {
	lockPath, err := lockFilePath()
	if err != nil {
		return err
	}
	lockFile, ok, err := acquireLock(lockPath)
	if err != nil {
		return err
	}
	if !ok {
		log.Println("notifyd: another instance is already running, exiting")
		return nil
	}
	defer func() {
		syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		lockFile.Close()
	}()

	// Record our PID in the (still-held) lock file so kage's SignalReload
	// can find us later - the flock above is what actually enforces
	// single-instance, this is just a lookup aid piggybacking on the same
	// file.
	if err := lockFile.Truncate(0); err == nil {
		lockFile.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0)
	}

	log.Printf("notifyd: starting for %d account(s)", len(cfg.Accounts))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := &daemon{ctx: ctx, cfgPath: cfg.Path, watchers: make(map[string]context.CancelFunc)}

	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)
	defer signal.Stop(sighup)
	go func() {
		for {
			select {
			case <-sighup:
				d.reload()
			case <-ctx.Done():
				return
			}
		}
	}()

	onReady := func() {
		systray.SetIcon(iconPNG)
		systray.SetTitle("")
		systray.SetTooltip("Kage — watching for new messages")
		quit := systray.AddMenuItem("Stop Kage Notifications Service", "Stop the kage notification daemon")
		go func() {
			<-quit.ClickedCh
			log.Println("notifyd: quit requested from tray")
			cancel()
			systray.Quit()
		}()

		d.start(cfg)
	}
	onExit := func() {
		log.Println("notifyd: exiting")
	}

	systray.Run(onReady, onExit)
	return nil
}

// daemon tracks which accounts are currently being watched, so a SIGHUP
// (sent by kage's adapter whenever an account's status changes - see
// SignalReload) can start/stop individual accounts without restarting the
// whole process or disturbing accounts nothing changed for.
type daemon struct {
	ctx     context.Context
	cfgPath string

	mu       sync.Mutex
	watchers map[string]context.CancelFunc // JID -> cancel for its watchAccount goroutine
}

// start begins watching every non-offline account in cfg that isn't already
// being watched.
func (d *daemon) start(cfg config.Config) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, acct := range cfg.Accounts {
		d.startLocked(acct, cfg.UseKeyring)
	}
}

// startLocked is start's non-locking core - callers must hold d.mu.
func (d *daemon) startLocked(acct config.Account, useKeyring bool) {
	if acct.Status == "offline" {
		return
	}
	if _, ok := d.watchers[acct.JID]; ok {
		return
	}
	acctCtx, cancel := context.WithCancel(d.ctx)
	d.watchers[acct.JID] = cancel
	go watchAccount(acctCtx, acct, useKeyring)
}

// reload re-reads config.toml and reconciles the running watchers against
// it: accounts that became offline (or were removed) are stopped, accounts
// that became online (or were added) are started. Accounts whose status
// didn't change are left running untouched - no reconnect glitch just from
// an unrelated account's status flipping.
func (d *daemon) reload() {
	cfg, err := config.Load(d.cfgPath)
	if err != nil {
		log.Printf("notifyd: reload: loading config: %v", err)
		return
	}
	log.Printf("notifyd: reload: re-read config (%d accounts)", len(cfg.Accounts))

	d.mu.Lock()
	defer d.mu.Unlock()

	wanted := make(map[string]bool, len(cfg.Accounts))
	for _, acct := range cfg.Accounts {
		if acct.Status != "offline" {
			wanted[acct.JID] = true
		}
	}
	for jid, cancel := range d.watchers {
		if !wanted[jid] {
			log.Printf("notifyd: reload: stopping %s (now offline or removed)", jid)
			cancel()
			delete(d.watchers, jid)
		}
	}
	for _, acct := range cfg.Accounts {
		d.startLocked(acct, cfg.UseKeyring)
	}
}

// watchAccount dials acct and forwards every live incoming message to
// notify, reconnecting with backoff on disconnect, until ctx is cancelled.
// It never touches the shared database and never decrypts anything —
// GPG/OMEMO content shows only as "encrypted message" in the notification,
// both to keep this process genuinely read-only and because running OMEMO
// decryption concurrently with the main TUI process would corrupt the
// double-ratchet state they'd otherwise share.
func watchAccount(ctx context.Context, acct config.Account, useKeyring bool) {
	ownBare := bareJID(acct.JID)

	for {
		if ctx.Err() != nil {
			return
		}

		password, err := acct.ResolvePassword(useKeyring)
		if err != nil {
			log.Printf("notifyd: %s: resolving password: %v", acct.JID, err)
			if !sleepOrDone(ctx, 30*time.Second) {
				return
			}
			continue
		}

		var tlsConfig *tls.Config
		client, err := xmpp.Dial(ctx, acct.JID, password, tlsConfig)
		if err != nil {
			log.Printf("notifyd: %s: dial failed: %v", acct.JID, err)
			if !sleepOrDone(ctx, 30*time.Second) {
				return
			}
			continue
		}
		log.Printf("notifyd: %s: connected", acct.JID)

		names := rosterNames(ctx, client)

		go func() {
			<-ctx.Done()
			client.Close()
		}()

		for ev := range client.Events() {
			msgEv, isMsg := ev.(xmpp.MessageEvent)
			if !isMsg {
				continue
			}
			handleMessageEvent(msgEv, ownBare, names)
		}

		if ctx.Err() != nil {
			return
		}
		log.Printf("notifyd: %s: disconnected, reconnecting...", acct.JID)
		if !sleepOrDone(ctx, 5*time.Second) {
			return
		}
	}
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

// rosterNames is a best-effort, read-only display-name lookup for incoming
// notifications — a failure here just means notifications fall back to
// showing the bare JID.
func rosterNames(ctx context.Context, client *xmpp.Client) map[string]string {
	contacts, err := client.Roster(ctx)
	if err != nil {
		return nil
	}
	names := make(map[string]string, len(contacts))
	for _, c := range contacts {
		if c.Name != "" {
			names[c.JID] = c.Name
		}
	}
	return names
}

// handleMessageEvent decides whether msgEv is worth a desktop notification
// and, if so, fires one. It skips our own carboned-in messages,
// reaction/retraction/correction updates (nothing new for a human to read),
// and never decrypts GPG or OMEMO content — see watchAccount's doc comment.
func handleMessageEvent(msgEv xmpp.MessageEvent, ownBare string, names map[string]string) {
	if msgEv.ReactionTargetID != "" || msgEv.RetractID != "" || msgEv.ReplaceID != "" {
		return
	}
	if msgEv.Body == "" && msgEv.Encrypted == nil {
		return
	}

	from := bareJID(msgEv.From)
	if from == ownBare {
		return
	}

	title := names[from]
	if title == "" {
		title = from
	}

	body := "New message"
	switch {
	case msgEv.Encrypted != nil:
		body = "🔒 New encrypted message"
	case gpg.Looks(msgEv.Body):
		body = "🔒 New encrypted message"
	default:
		body = truncate(msgEv.Body, 120)
	}

	notify(title, body)
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// notify shows a desktop notification via notify-send (org.freedesktop.
// Notifications under the hood on every common Linux desktop). Best-effort:
// a missing notify-send binary just means no popup, not a daemon crash.
func notify(title, body string) {
	cmd := exec.Command("notify-send", "-a", "kage", title, body)
	if err := cmd.Run(); err != nil {
		log.Printf("notifyd: notify-send failed: %v", err)
	}
}

func bareJID(addr string) string {
	bare, _, _ := strings.Cut(addr, "/")
	return bare
}
