package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jim-ww/kage/ui"
)

func loadTOML(t *testing.T, toml string) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := loadConfigFile(path)
	return cfg, err
}

func TestThemePresets(t *testing.T) {
	cfg, err := loadTOML(t, "")
	if err != nil || cfg.ResolvedTheme() != ui.TerminalTheme() {
		t.Fatalf("no theme: got %+v, err %v, want the terminal theme", cfg.ResolvedTheme(), err)
	}

	cfg, err = loadTOML(t, "[theme]\npreset = \"tokyonight\"\naccent_cyan = \"#00ffff\"\n")
	if err != nil {
		t.Fatal(err)
	}
	want := ui.TokyoNightTheme()
	want.AccentCyan = "#00ffff"
	if got := cfg.ResolvedTheme(); got != want {
		t.Fatalf("tokyonight with an override: got %+v, want %+v", got, want)
	}

	if _, err := loadTOML(t, "[theme]\npreset = \"solarized\"\n"); err == nil {
		t.Fatal("an unknown preset loaded without an error")
	}
}
