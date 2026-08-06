package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// fakeBin creates an executable file named name in dir, returning dir so
// callers can prepend it to $PATH.
func fakeBin(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestResolveTerminalPrefersConfigured(t *testing.T) {
	got, err := resolveTerminal("my-term")
	if err != nil {
		t.Fatal(err)
	}
	if got != "my-term" {
		t.Fatalf("got %q, want %q", got, "my-term")
	}
}

func TestResolveTerminalPrefersEnvOverFallbacks(t *testing.T) {
	dir := t.TempDir()
	fakeBin(t, dir, "foot")
	t.Setenv("PATH", dir)
	t.Setenv("TERMINAL", "my-configured-term")

	got, err := resolveTerminal("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "my-configured-term" {
		t.Fatalf("got %q, want $TERMINAL to win over fallback list", got)
	}
}

func TestResolveTerminalXdgTerminalExecOverFallbacks(t *testing.T) {
	dir := t.TempDir()
	fakeBin(t, dir, "xdg-terminal-exec")
	fakeBin(t, dir, "foot")
	t.Setenv("PATH", dir)
	t.Setenv("TERMINAL", "")

	got, err := resolveTerminal("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "xdg-terminal-exec" {
		t.Fatalf("got %q, want xdg-terminal-exec to win over the hardcoded list", got)
	}
}

func TestResolveTerminalFallbackListInOrder(t *testing.T) {
	dir := t.TempDir()
	// Only later entries in fallbackTerminals exist, to prove order is
	// respected rather than "whichever glob matched first".
	fakeBin(t, dir, "kitty")
	fakeBin(t, dir, "wezterm")
	t.Setenv("PATH", dir)
	t.Setenv("TERMINAL", "")

	got, err := resolveTerminal("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "kitty" {
		t.Fatalf("got %q, want %q (first present entry in fallbackTerminals)", got, "kitty")
	}
}

func TestResolveTerminalNoneFoundErrors(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("resolveTerminal is linux-only")
	}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("TERMINAL", "")

	if _, err := resolveTerminal(""); err == nil {
		t.Fatal("expected an error when nothing resolves")
	}
}
