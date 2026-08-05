package config

import (
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
	JID         string            `toml:"jid"`
	Password    string            `toml:"password,omitempty"`     // plaintext fallback
	PasswordCmd string            `toml:"password_cmd,omitempty"` // shell command printing the password on stdout
	GPGKeyID    string            `toml:"gpg_key_id,omitempty"`   // own key, used to decrypt/sign
	GPGPeers    map[string]string `toml:"gpg_peers,omitempty"`    // JID -> peer's key fingerprint

	// OmemoPeers pins a specific OMEMO protocol version for specific peers:
	// JID -> "v1" | "v2". Only consulted when resolveOmemoProtocol's
	// auto-detection runs (e.g. for legacy stored chat modes) - a chat
	// pinned directly to "omemo-v1"/"omemo-v2" ignores this.
	OmemoPeers map[string]string `toml:"omemo_peers,omitempty"`

	// Status is the account's configured presence: "" (default, online),
	// "chat", "away", "xa" (extended away), "dnd", or "offline". Read once
	// at startup to decide whether to dial this account at all (offline
	// never touches the network) and what initial <show/> to send;
	// persisted immediately whenever changed from the UI, so a restart
	// always comes back up in the same status.
	Status string `toml:"status,omitempty"`
}

// ResolvePassword returns the account's password, trying the OS keyring
// first (unless useKeyring is false), then PasswordCmd, then the plaintext
// Password field. Any keyring error (not found, no Secret Service running,
// etc.) just falls through to the next method — the keyring is a
// best-effort first choice, not a hard requirement, since plenty of
// environments (headless boxes, sandboxes) don't have one available at all.
func (a Account) ResolvePassword(useKeyring bool) (string, error) {
	if useKeyring {
		if pass, err := keyring.Get(keyringService, a.JID); err == nil {
			return pass, nil
		}
	}

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
