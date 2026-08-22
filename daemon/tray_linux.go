package daemon

import (
	"bytes"
	"context"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"

	"fyne.io/systray"
	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/version"
)

// Backend is implemented by package main: it owns every XMPP connection,
// storage, and the ipc socket clients attach to, once this package's
// process-level plumbing (single-instance lock, SIGHUP, tray) is ready to
// hand off control.
type Backend interface {
	Start(ctx context.Context, cfg config.Config)
	Reload(cfg config.Config)
	Shutdown()
}

// Run is the --background entry point: acquires the single-instance lock,
// brings up a tray icon, and hands off to backend for the real work (XMPP
// connections, storage, the ipc socket). Blocks until the tray is quit (via
// the menu, or the OS signaling the session is ending); returns nil if
// another instance already holds the lock, so callers can always invoke
// this unconditionally in --background mode.
func Run(cfg config.Config, backend Backend) error {
	lockPath, err := lockFilePath()
	if err != nil {
		return err
	}
	lockFile, ok, err := acquireLock(lockPath)
	if err != nil {
		return err
	}
	if !ok {
		log.Println("kage background service: another instance is already running, exiting")
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

	log.Printf("kage background service: starting version=%s for %d account(s)", version.Version, len(cfg.Accounts))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)
	defer signal.Stop(sighup)
	sigterm := make(chan os.Signal, 1)
	signal.Notify(sigterm, syscall.SIGTERM)
	defer signal.Stop(sigterm)
	go func() {
		for {
			select {
			case <-sighup:
				if c, err := config.Load(cfg.Path); err != nil {
					log.Printf("kage background service: reload: loading config: %v", err)
				} else {
					log.Printf("kage background service: reload: re-read config (%d accounts)", len(c.Accounts))
					backend.Reload(c)
				}
			case <-sigterm:
				log.Println("kage background service: SIGTERM received, shutting down")
				backend.Shutdown()
				cancel()
				systray.Quit()
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	onReady := func() {
		systray.SetIcon(iconPNG)
		systray.SetTitle("")
		systray.SetTooltip("Kage")
		quit := systray.AddMenuItem("Quit Kage", "Stop the kage background service")
		go func() {
			<-quit.ClickedCh
			log.Println("kage background service: quit requested from tray")
			backend.Shutdown()
			cancel()
			systray.Quit()
		}()
		// LMB opens a new kage TUI, like any other messenger's tray icon;
		// RMB still opens the menu above (SetOnSecondaryTapped, unused —
		// that's systray's default behavior for the context menu).
		systray.SetOnTapped(func() {
			launchTerminal(cfg.TerminalCmd)
		})

		backend.Start(ctx, cfg)
	}
	onExit := func() {
		log.Println("kage background service: exiting")
	}

	systray.Run(onReady, onExit)
	return nil
}

// Notify shows a desktop notification via notify-send (org.freedesktop.
// Notifications under the hood on every common Linux desktop). Best-effort:
// a missing notify-send binary just means no popup, not a daemon crash.
//
// The daemon is long-lived and detached (Setsid, spawn_linux.go) — it can
// outlive the login session whose DBUS_SESSION_BUS_ADDRESS it inherited at
// spawn time (relogin, session restart, a NixOS rebuild that respawns the
// bus). Rather than trust that possibly-stale inherited env, always point
// at the current user's session bus socket directly, which systemd/dbus
// place at this fixed path independent of any particular login session.
func Notify(title, body string) {
	cmd := exec.Command("notify-send", "-a", "kage", title, body)
	cmd.Env = append(os.Environ(), "DBUS_SESSION_BUS_ADDRESS="+sessionBusAddress())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		log.Printf("kage background service: notify-send failed: %v: %s", err, bytes.TrimSpace(stderr.Bytes()))
	}
}

func sessionBusAddress() string {
	return "unix:path=/run/user/" + strconv.Itoa(os.Getuid()) + "/bus"
}
