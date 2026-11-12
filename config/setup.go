package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/zalando/go-keyring"

	"github.com/jim-ww/kage/ui"
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

// ErrAccountExists is WriteAccount's error for a JID that's already
// configured.
var ErrAccountExists = errors.New("account is already added")

// WriteAccount appends acct to the [[accounts]] list in the config file at
// path, preserving any existing keybinds/theme/accounts already there.
// Creates the file if it doesn't exist. Refuses (ErrAccountExists) a JID
// that's already in the list - two accounts with one JID would fight over
// the same session and storage rows.
func WriteAccount(path string, acct Account) error {
	return updateConfigFile(path, func(cfg *Config) error {
		if hasAccount(cfg.Accounts, acct.JID) {
			return ErrAccountExists
		}
		cfg.Accounts = append(cfg.Accounts, acct)
		return nil
	})
}

// AccountExists reports whether jid is already configured in the config
// file at path - for refusing a duplicate before doing anything else about
// it (like registering it on the server).
func AccountExists(path, jid string) (bool, error) {
	cfg, err := loadOrEmpty(path)
	if err != nil {
		return false, err
	}
	return hasAccount(cfg.Accounts, jid), nil
}

// hasAccount compares bare JIDs case-insensitively, the way the server does
// (domains are case-insensitive, and localparts are case-folded by
// nodeprep/PRECIS for practically every account).
func hasAccount(accounts []Account, jid string) bool {
	bare := func(j string) string {
		j, _, _ = strings.Cut(strings.TrimSpace(j), "/")
		return j
	}
	for _, a := range accounts {
		if strings.EqualFold(bare(a.JID), bare(jid)) {
			return true
		}
	}
	return false
}

// RemoveAccount deletes the account matching jid from the [[accounts]] list
// in the config file at path, preserving everything else. A no-op if the
// account isn't found there.
func RemoveAccount(path, jid string) error {
	return updateConfigFile(path, func(cfg *Config) error {
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
		return nil
	})
}

// SetAccountGPGKeyID sets (or updates) the gpg_key_id field for the account
// matching jid in the config file at path, preserving everything else. A
// no-op if the account isn't found there.
func SetAccountGPGKeyID(path, jid, keyID string) error {
	return updateConfigFile(path, func(cfg *Config) error {
		for i, acct := range cfg.Accounts {
			if acct.JID == jid {
				cfg.Accounts[i].GPGKeyID = keyID
				return nil
			}
		}
		return fmt.Errorf("account %s not found in %s", jid, path)
	})
}

// SetAccountStatus sets (or updates) the status ("", "chat", "away", "xa",
// "dnd", or "offline") for jid in the state file next to path, preserving
// everything else - see State.AccountStatuses.
func SetAccountStatus(path, jid, status string) error {
	return updateStateFile(path, func(st *State) error {
		if st.AccountStatuses == nil {
			st.AccountStatuses = make(map[string]string)
		}
		if status == "" {
			delete(st.AccountStatuses, jid)
		} else {
			st.AccountStatuses[jid] = status
		}
		return nil
	})
}

// SetStoragePlaintextPassword sets (or updates) the [storage] password field
// in the config file at path, preserving everything else — the plaintext
// fallback used when the OS keyring is unavailable/disabled (see
// SetStorageKeyringPassword for the keyring path). Clears PasswordCmd: the
// two are mutually exclusive ways of resolving the same setting, and leaving
// a stale password_cmd behind would silently shadow the password just set
// (ResolveStoragePassword tries PasswordCmd before Password).
func SetStoragePlaintextPassword(path, password string) error {
	return updateConfigFile(path, func(cfg *Config) error {
		cfg.Storage.Password = password
		cfg.Storage.PasswordCmd = ""
		return nil
	})
}

// ClearStoragePassword removes both storage.password and
// storage.password_cmd from the config file at path — used when the local
// storage password is changed to empty, i.e. local storage encryption is
// being turned off. Since both fields are `omitempty`, zeroing them here
// drops the keys from config.toml entirely on the next write rather than
// leaving a `password: ""` behind.
func ClearStoragePassword(path string) error {
	return updateConfigFile(path, func(cfg *Config) error {
		cfg.Storage.Password = ""
		cfg.Storage.PasswordCmd = ""
		return nil
	})
}

func loadOrEmpty(path string) (Config, error) {
	cfg, _, err := loadConfigFile(path)
	return cfg, err
}

// writeFileConfig strips any field equal to its default (see stripDefaults)
// before encoding, so config.toml only ever contains settings that differ
// from default.
func writeFileConfig(path string, cfg Config) error {
	stripDefaults(&cfg, defaultConfig())
	data, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	return writeFileAtomic(path, data, 0o600)
}

// SetChatFlag adds or removes one chat from an account's list of chats with
// flag in the state file next to path. Each list is sorted and
// case-insensitively deduped.
func SetChatFlag(path, accountJID, chatAddress string, flag ui.ChatSetting, on bool) error {
	if accountJID == "" || chatAddress == "" {
		return fmt.Errorf("flagging a chat %s needs both an account and a chat address", flag)
	}
	return updateStateFile(path, func(st *State) error {
		lists := map[ui.ChatSetting]*map[string][]string{ui.ChatHidden: &st.HiddenChats, ui.ChatPinned: &st.PinnedChats, ui.ChatMuted: &st.MutedChats}
		list, ok := lists[flag]
		if !ok {
			return fmt.Errorf("unknown chat flag %q", flag)
		}
		existing := *list
		kept := make([]string, 0, len(existing[accountJID])+1)
		for _, addr := range existing[accountJID] {
			if !strings.EqualFold(addr, chatAddress) {
				kept = append(kept, addr)
			}
		}
		if on {
			kept = append(kept, chatAddress)
			sort.Strings(kept)
		}

		if existing == nil {
			existing = map[string][]string{}
			*list = existing
		}
		if len(kept) == 0 {
			// Left as an empty entry, the account would keep a dangling key in
			// state.toml for a chat that no longer carries the flag.
			delete(existing, accountJID)
		} else {
			existing[accountJID] = kept
		}
		return nil
	})
}
