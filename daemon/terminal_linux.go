package daemon

import (
	"fmt"
	"log"
	"os"
	"os/exec"
)

// fallbackTerminals is tried, in order, only once configured, $TERMINAL, and
// xdg-terminal-exec have all come up empty.
var fallbackTerminals = []string{"foot", "alacritty", "kitty", "wezterm", "xterm"}

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
	return "", fmt.Errorf("no terminal found: set terminal_cmd in config.toml, $TERMINAL, or install one of %v", fallbackTerminals)
}

// launchTerminal spawns a detached terminal running the kage TUI binary
// (itself, since this process is the daemon's own binary re-exec'd) — the
// new TUI instance finds the already-running daemon via EnsureRunning, so it
// never dials its own XMPP connections. Best-effort: any failure is logged,
// never fatal to the daemon.
func launchTerminal(configured string) {
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
