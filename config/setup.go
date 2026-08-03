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
	cfg, err := loadOrEmpty(path)
	if err != nil {
		return err
	}
	cfg.Accounts = append(cfg.Accounts, acct)
	return writeFileConfig(path, cfg)
}

// SetAccountGPGKeyID sets (or updates) the gpg_key_id field for the account
// matching jid in the config file at path, preserving everything else. A
// no-op if the account isn't found there.
func SetAccountGPGKeyID(path, jid, keyID string) error {
	cfg, err := loadOrEmpty(path)
	if err != nil {
		return err
	}
	found := false
	for i, acct := range cfg.Accounts {
		if acct.JID == jid {
			cfg.Accounts[i].GPGKeyID = keyID
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("account %s not found in %s", jid, path)
	}
	return writeFileConfig(path, cfg)
}

// SetDefaultAccount sets (or updates) the default_account setting in the
// config file at path, preserving everything else.
func SetDefaultAccount(path, jid string) error {
	cfg, err := loadOrEmpty(path)
	if err != nil {
		return err
	}
	cfg.DefaultAccount = jid
	return writeFileConfig(path, cfg)
}

func loadOrEmpty(path string) (fileConfig, error) {
	existing, err := loadFile(path)
	if err != nil {
		return fileConfig{}, err
	}
	if existing == nil {
		return fileConfig{}, nil
	}
	return *existing, nil
}

func writeFileConfig(path string, cfg fileConfig) error {
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
