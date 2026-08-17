package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
	"gopkg.in/yaml.v3"
)

// DefaultWritePath returns where a newly generated config should be written:
// $KAGE_CONFIG if set, otherwise the XDG-style default
// ~/.config/kage/config.yaml (created if the directory doesn't exist).
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
	return filepath.Join(dir, "config.yaml"), nil
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

// RemoveAccount deletes the account matching jid from the [[accounts]] list
// in the config file at path, preserving everything else. A no-op if the
// account isn't found there.
func RemoveAccount(path, jid string) error {
	cfg, err := loadOrEmpty(path)
	if err != nil {
		return err
	}
	idx := -1
	for i, acct := range cfg.Accounts {
		if acct.JID == jid {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("account %s not found in %s", jid, path)
	}
	cfg.Accounts = append(cfg.Accounts[:idx], cfg.Accounts[idx+1:]...)
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

// SetAccountStatus sets (or updates) the status field ("", "chat", "away",
// "xa", "dnd", or "offline") for the account matching jid in the config file at path,
// preserving everything else. A no-op if the account isn't found there.
func SetAccountStatus(path, jid, status string) error {
	cfg, err := loadOrEmpty(path)
	if err != nil {
		return err
	}
	found := false
	for i, acct := range cfg.Accounts {
		if acct.JID == jid {
			cfg.Accounts[i].Status = status
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

// SetSidebarWidth sets (or updates) the sidebar_width setting in the config
// file at path, preserving everything else — called after the user finishes
// dragging the sidebar border (see ui.SidebarWidthSetter).
func SetSidebarWidth(path string, width int) error {
	cfg, err := loadOrEmpty(path)
	if err != nil {
		return err
	}
	cfg.SidebarWidth = width
	return writeFileConfig(path, cfg)
}

// SetInputHeight sets (or updates) the input_height setting in the config
// file at path, preserving everything else — called after the user finishes
// dragging the compose box's top border (see ui.InputHeightSetter).
func SetInputHeight(path string, height int) error {
	cfg, err := loadOrEmpty(path)
	if err != nil {
		return err
	}
	cfg.InputHeight = height
	return writeFileConfig(path, cfg)
}

// SetSidebarHidden sets (or updates) the sidebar_hidden setting in the
// config file at path, preserving everything else — called whenever the
// user toggles the chat list (see ui.SidebarHiddenSetter).
func SetSidebarHidden(path string, hidden bool) error {
	cfg, err := loadOrEmpty(path)
	if err != nil {
		return err
	}
	cfg.SidebarHidden = hidden
	return writeFileConfig(path, cfg)
}

// SetFilePickerSort sets (or updates) the file_picker_sort_field/
// file_picker_sort_ascending settings in the config file at path, preserving
// everything else — called whenever the user cycles the attach-file
// picker's sort order (see ui.FilePickerSortSetter).
func SetFilePickerSort(path string, field string, ascending bool) error {
	cfg, err := loadOrEmpty(path)
	if err != nil {
		return err
	}
	cfg.FilePickerSortField = field
	cfg.FilePickerSortAscending = ascending
	return writeFileConfig(path, cfg)
}

// SetLastChat sets (or updates) the last_chat_account/last_chat_address
// settings in the config file at path, preserving everything else — called
// whenever the user opens a chat, so it can be reopened on startup when
// open_last_chat is set (see ui.LastChatSetter).
func SetLastChat(path, accountJID, chatAddress string) error {
	cfg, err := loadOrEmpty(path)
	if err != nil {
		return err
	}
	cfg.LastChatAccount = accountJID
	cfg.LastChatAddress = chatAddress
	return writeFileConfig(path, cfg)
}

// SetStoragePlaintextPassword sets (or updates) the [storage] password field
// in the config file at path, preserving everything else — the plaintext
// fallback used when the OS keyring is unavailable/disabled (see
// SetStorageKeyringPassword for the keyring path). Clears PasswordCmd: the
// two are mutually exclusive ways of resolving the same setting, and leaving
// a stale password_cmd behind would silently shadow the password just set
// (ResolveStoragePassword tries PasswordCmd before Password).
func SetStoragePlaintextPassword(path, password string) error {
	cfg, err := loadOrEmpty(path)
	if err != nil {
		return err
	}
	cfg.Storage.Password = password
	cfg.Storage.PasswordCmd = ""
	return writeFileConfig(path, cfg)
}

// ClearStoragePassword removes both storage.password and
// storage.password_cmd from the config file at path — used when the local
// storage password is changed to empty, i.e. local storage encryption is
// being turned off. Since both fields are `omitempty`, zeroing them here
// drops the keys from config.yaml entirely on the next write rather than
// leaving a `password: ""` behind.
func ClearStoragePassword(path string) error {
	cfg, err := loadOrEmpty(path)
	if err != nil {
		return err
	}
	cfg.Storage.Password = ""
	cfg.Storage.PasswordCmd = ""
	return writeFileConfig(path, cfg)
}

func loadOrEmpty(path string) (Config, error) {
	cfg, _, err := loadConfigFile(path)
	return cfg, err
}

// writeFileConfig strips any field equal to its default (see stripDefaults)
// before encoding, so config.yaml only ever contains settings that differ
// from default.
func writeFileConfig(path string, cfg Config) error {
	stripDefaults(&cfg, defaultConfig())
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
