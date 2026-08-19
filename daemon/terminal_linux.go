package daemon

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"
)

// fallbackTerminals is tried, in order, only once configured, $TERMINAL, and
// xdg-terminal-exec have all come up empty.
var fallbackTerminals = []string{"foot", "alacritty", "kitty", "wezterm", "xterm"}

// launchDebounce is how long launchTerminal ignores repeat calls after
// spawning a terminal. Tray tap events can fire more than once per physical
// click (seen on sway/waybar); without this, a double- or triple-tap spawns
// that many terminal+TUI processes at once, and a tiling WM racing to lay
// out several new windows in the same instant can leave one of them with a
// stale WindowSizeMsg — fullscreen surface, content drawn for a much
// smaller size.
const launchDebounce = 2 * time.Second

var (
	launchMu   sync.Mutex
	launchedAt time.Time
)

// resolveTerminal picks the terminal emulator to launch a new kage TUI in:
// the configured terminal_cmd, then $TERMINAL, then xdg-terminal-exec (the
// portland/xdg-terminal-exec draft standard, not universally installed), then
// a hardcoded list of common terminals — first one found on $PATH wins.
func resolveTerminal(configured string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	if t := os.Getenv("TERMINAL"); t != "" {
		return t, nil
	}
	if _, err := exec.LookPath("xdg-terminal-exec"); err == nil {
		return "xdg-terminal-exec", nil
	}
	for _, t := range fallbackTerminals {
		if _, err := exec.LookPath(t); err == nil {
			return t, nil
		}
	}
	return "", fmt.Errorf("no terminal found: set terminal_cmd in config.yaml, $TERMINAL, or install one of %v", fallbackTerminals)
}

// launchTerminal spawns a detached terminal running the kage TUI binary
// (itself, since this process is the daemon's own binary re-exec'd) — the
// new TUI instance finds the already-running daemon via EnsureRunning, so it
// never dials its own XMPP connections. Best-effort: any failure is logged,
// never fatal to the daemon.
func launchTerminal(configured string) {
	launchMu.Lock()
	if since := time.Since(launchedAt); since < launchDebounce {
		launchMu.Unlock()
		log.Printf("daemon: ignoring terminal launch, last one was %s ago", since.Round(time.Millisecond))
		return
	}
	launchedAt = time.Now()
	launchMu.Unlock()

	term, err := resolveTerminal(configured)
	if err != nil {
		log.Printf("daemon: launching terminal: %v", err)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		log.Printf("daemon: locating kage binary to launch: %v", err)
		return
	}

	cmd := exec.Command(term, "-e", exe)
	cmd.SysProcAttr = detachedSysProcAttr()
	if err := cmd.Start(); err != nil {
		log.Printf("daemon: starting terminal %s: %v", term, err)
		return
	}
	cmd.Process.Release()
}
