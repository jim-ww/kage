package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/zalando/go-keyring"
)

// DefaultWritePath returns where a newly generated config should be written:
// $KAGE_CONFIG if set, otherwise the XDG-style default
// ~/.config/kage/config.toml (created if the directory doesn't exist).
func DefaultWritePath() (string, error) {
	if env := os.Getenv("KAGE_CONFIG"); env != "" {
		return env, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding home directory: %w", err)
	}
	dir := filepath.Join(home, ".config", "kage")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	return filepath.Join(dir, "config.toml"), nil
}

// SetKeyringPassword stores password in the OS keyring for jid, under the
// same service/key ResolvePassword looks it up from.
func SetKeyringPassword(jid, password string) error {
	return keyring.Set(keyringService, jid, password)
}

// WriteAccount appends acct to the [[accounts]] list in the config file at
// path, preserving any existing keybinds/theme/accounts already there.
// Creates the file if it doesn't exist.
func WriteAccount(path string, acct Account) error {
	var cfg fileConfig
	if existing, err := loadFile(path); err != nil {
		return err
	} else if existing != nil {
		cfg = *existing
	}
	cfg.Accounts = append(cfg.Accounts, acct)

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
