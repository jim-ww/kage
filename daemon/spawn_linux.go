// Package daemon implements kage's background service: the one process per
// user session that owns every configured account's XMPP connection,
// storage, and decryption (GPG/OMEMO). The TUI is a thin client that talks
// to it over a Unix socket (see the ipc package), auto-spawning it if it
// isn't already running. It also drives the tray icon and fires desktop
// notifications on decrypted incoming messages.
//
// Linux-only: the tray icon (fyne.io/systray) and the flock-based
// single-instance check both use APIs this package only implements for
// linux (see the _linux.go files); other platforms get the no-op stub in
// spawn_other.go so kage still builds elsewhere, just without the daemon.
package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jim-ww/kage/ipc"
)

// detachedSysProcAttr puts the spawned daemon in its own session (Setsid),
// so it isn't in kage's process group and a terminal hangup / kage exiting
// doesn't send it a signal along the way.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// lockFilePath returns the flock-guarded file that marks "a daemon is
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
	return filepath.Join(dir, "daemon.lock"), nil
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
	return filepath.Join(dir, "daemon.log"), nil
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

// SignalReload sends SIGHUP to the currently running daemon (identified by
// the PID Run recorded in its lock file when it started), asking it to
// re-read config.toml and adjust which accounts it's watching accordingly -
// see daemon_linux.go's sighup handling. A no-op (nil error) if no daemon
// is running, or its recorded PID is stale.
func SignalReload() error {
	lockPath, err := lockFilePath()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGHUP); err != nil {
		if err == syscall.ESRCH {
			return nil // stale pid, process is gone
		}
		return err
	}
	return nil
}

// probeSocket reports whether something is actually listening (and
// accepting) on the daemon's ipc socket right now.
func probeSocket(sockPath string) bool {
	c, err := ipc.DialTimeout(sockPath, 200*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// EnsureRunning makes sure a kage background service is listening on the
// ipc socket for cfgPath's config, spawning one (detached, own session,
// stdio redirected to daemon.log) if none answers yet, and returns once
// it's confirmed reachable or a short retry budget is exhausted.
// Best-effort: any failure here is logged by the caller, never fatal to
// starting the TUI.
func EnsureRunning(cfgPath string) error {
	sockPath, err := ipc.SocketPath()
	if err != nil {
		return fmt.Errorf("locating kage service socket: %w", err)
	}
	if probeSocket(sockPath) {
		return nil // already running
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating kage binary: %w", err)
	}

	logPath, err := logFilePath()
	if err != nil {
		return fmt.Errorf("locating daemon log file: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("opening %s: %w", logPath, err)
	}
	defer logFile.Close()

	args := []string{"-background"}
	if cfgPath != "" {
		args = append(args, "-c", cfgPath)
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = detachedSysProcAttr()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting kage background service: %w", err)
	}
	// Detach fully: don't hold onto the child, and don't leave it as a
	// zombie waiting on us — once started it's independent.
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("detaching kage background service: %w", err)
	}

	backoff := 25 * time.Millisecond
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if probeSocket(sockPath) {
			return nil
		}
		time.Sleep(backoff)
		if backoff < 500*time.Millisecond {
			backoff *= 2
		}
	}

	if pid, ok := stalePID(); ok {
		return fmt.Errorf("a kage background service (pid %d) appears to hold the startup lock but isn't answering on %s — it may be a stale pre-upgrade instance; you may need to stop it manually (kill %d) and try again", pid, sockPath, pid)
	}
	return fmt.Errorf("kage background service did not come up on %s within 5s", sockPath)
}

// stalePID reads the PID the (possibly stale) lock file's holder recorded
// when it started, for EnsureRunning's error message.
func stalePID() (int, bool) {
	lockPath, err := lockFilePath()
	if err != nil {
		return 0, false
	}
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}
