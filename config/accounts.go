package config

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/zalando/go-keyring"
)

// keyringService is the Secret Service / OS keyring entry name under which
// account passwords may be stored (key: JID).
const keyringService = "kage"

// storageKeyringAccount is the keyring key ResolveStoragePassword checks
// under keyringService, distinct from any account JID.
const storageKeyringAccount = "local-storage"

// Account is one configured XMPP account.
type Account struct {
	JID         string            `yaml:"jid"`
	Alias       string            `yaml:"alias,omitempty"`        // display name shown in place of the JID in the UI
	Password    string            `yaml:"password,omitempty"`     // plaintext fallback
	PasswordCmd string            `yaml:"password_cmd,omitempty"` // shell command printing the password on stdout
	GPGKeyID    string            `yaml:"gpg_key_id,omitempty"`   // own key, used to decrypt/sign
	GPGPeers    map[string]string `yaml:"gpg_peers,omitempty"`    // JID -> peer's key fingerprint

	// OmemoPeers pins a specific OMEMO protocol version for specific peers:
	// JID -> "v1" | "v2". Only consulted when resolveOmemoProtocol's
	// auto-detection runs (e.g. for legacy stored chat modes) - a chat
	// pinned directly to "omemo-v1"/"omemo-v2" ignores this.
	OmemoPeers map[string]string `yaml:"omemo_peers,omitempty"`

	// Status is the account's configured presence: "" (default, online),
	// "chat", "away", "xa" (extended away), "dnd", or "offline". Read once
	// at startup to decide whether to dial this account at all (offline
	// never touches the network) and what initial <show/> to send;
	// persisted immediately whenever changed from the UI, so a restart
	// always comes back up in the same status.
	Status string `yaml:"status,omitempty"`
}

// ResolvePassword returns the account's password, trying PasswordCmd first,
// then the plaintext Password field, and only then the OS keyring (unless
// useKeyring is false). PasswordCmd/Password are checked first because
// they're synchronous local reads with no failure mode worth waiting out;
// the keyring goes last since a missing Secret Service (headless boxes,
// sandboxes) can make keyring.Get block for many seconds over D-Bus before
// it gives up — not worth paying that cost when a faster method is already
// configured. Any keyring error (not found, no Secret Service running,
// etc.) just means no password was available.
func (a Account) ResolvePassword(useKeyring bool) (string, error) {
	if a.PasswordCmd != "" {
		out, err := exec.Command("sh", "-c", a.PasswordCmd).Output()
		if err != nil {
			return "", fmt.Errorf("running password_cmd for %s: %w", a.JID, err)
		}
		return strings.TrimSpace(string(out)), nil
	}

	if a.Password != "" {
		return a.Password, nil
	}

	if useKeyring {
		if pass, err := keyring.Get(keyringService, a.JID); err == nil {
			return pass, nil
		}
	}

	return "", fmt.Errorf("no password available for %s (keyring, password_cmd, and password are all unset)", a.JID)
}

// ResolveStoragePassword returns the password local message history is
// encrypted under at rest, trying the OS keyring first (unless useKeyring is
// false), then StorageConfig.PasswordCmd, then the plaintext
// StorageConfig.Password — same precedence as Account.ResolvePassword.
// configured is false (password always "", err always nil) when none of the
// three are set at all — that's a valid choice (local storage falls back to
// plaintext), not an error. configured true with a non-nil err means a
// password_cmd was set but actually failed to run.
func ResolveStoragePassword(cfg StorageConfig, useKeyring bool) (password string, configured bool, err error) {
	if useKeyring {
		if pass, err := keyring.Get(keyringService, storageKeyringAccount); err == nil {
			return pass, true, nil
		}
	}

	if cfg.PasswordCmd != "" {
		out, err := exec.Command("sh", "-c", cfg.PasswordCmd).Output()
		if err != nil {
			return "", true, fmt.Errorf("running storage password_cmd: %w", err)
		}
		return strings.TrimSpace(string(out)), true, nil
	}

	if cfg.Password != "" {
		return cfg.Password, true, nil
	}

	return "", false, nil
}

// SetStorageKeyringPassword stores password in the OS keyring for local
// storage encryption, under the same service/key ResolveStoragePassword
// looks it up from.
func SetStorageKeyringPassword(password string) error {
	return keyring.Set(keyringService, storageKeyringAccount, password)
}

// ClearStorageKeyringPassword removes the local-storage password from the OS
// keyring, if any is set there. A "not found" error is treated as success —
// the end state (nothing in the keyring) is the same either way.
func ClearStorageKeyringPassword() error {
	if err := keyring.Delete(keyringService, storageKeyringAccount); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return nil
}
