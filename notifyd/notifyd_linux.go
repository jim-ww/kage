// Package notifyd implements kage's background notification daemon: one
// read-only process per user session that stays connected to every
// configured account purely to fire a desktop notification on new incoming
// messages, independent of whether the TUI itself is running. It never
// decrypts message content (no GPG, no OMEMO) and never writes to the
// shared database — "read-only" both in the XMPP sense (it only observes)
// and the storage sense.
//
// Linux-only: the tray icon (fyne.io/systray) and the flock-based
// single-instance check both use APIs this package only implements for
// linux (see the _linux.go files); other platforms get the no-op stub in
// notifyd_other.go so kage still builds elsewhere, just without the daemon.
package notifyd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// detachedSysProcAttr puts the spawned notifyd in its own session (Setsid),
// so it isn't in kage's process group and a terminal hangup / kage exiting
// doesn't send it a signal along the way.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// lockFilePath returns the flock-guarded file that marks "a notifyd is
// running for this user" — same directory kage's own data/debug files live
// in, so it survives XDG_RUNTIME_DIR getting cleared between logins the
// same way the rest of kage's state does.
func lockFilePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "kage")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "notifyd.lock"), nil
}

func logFilePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "kage")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "notifyd.log"), nil
}

// acquireLock takes a non-blocking exclusive flock on path. ok is false
// (with a nil error) if some other process already holds it — that's the
// expected "already running" case, not a failure.
func acquireLock(path string) (f *os.File, ok bool, err error) {
	f, err = os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, false, nil
	}
	return f, true, nil
}

// EnsureRunning starts a detached notifyd for cfgPath's config unless one is
// already running for this user (checked via a non-blocking flock, not a
// pidfile — no stale-pid handling needed, the OS releases the lock the
// moment a dead process's file descriptors close). The spawned process is
// fully detached (own session, stdio redirected to notifyd.log) so it
// outlives the calling process — killing kage's TUI must never take the
// daemon down with it. Best-effort: any failure here is logged by the
// caller, never fatal to starting the TUI.
func EnsureRunning(cfgPath string) error {
	lockPath, err := lockFilePath()
	if err != nil {
		return fmt.Errorf("locating notifyd lock file: %w", err)
	}

	f, ok, err := acquireLock(lockPath)
	if err != nil {
		return fmt.Errorf("checking notifyd lock: %w", err)
	}
	if !ok {
		return nil // already running
	}
	// We only wanted to probe; release immediately so the real daemon
	// process can take the lock itself once it starts.
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	f.Close()

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating kage binary: %w", err)
	}

	logPath, err := logFilePath()
	if err != nil {
		return fmt.Errorf("locating notifyd log file: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("opening %s: %w", logPath, err)
	}
	defer logFile.Close()

	args := []string{"-notifyd"}
	if cfgPath != "" {
		args = append(args, "-c", cfgPath)
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = detachedSysProcAttr()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting notifyd: %w", err)
	}
	// Detach fully: don't hold onto the child, and don't leave it as a
	// zombie waiting on us — once started it's independent.
	return cmd.Process.Release()
}
