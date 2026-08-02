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

// Account is one configured XMPP account.
type Account struct {
	JID         string            `toml:"jid"`
	Password    string            `toml:"password,omitempty"`     // plaintext fallback
	PasswordCmd string            `toml:"password_cmd,omitempty"` // shell command printing the password on stdout
	GPGKeyID    string            `toml:"gpg_key_id,omitempty"`   // own key, used to decrypt/sign
	GPGPeers    map[string]string `toml:"gpg_peers,omitempty"`    // JID -> peer's key fingerprint
}

// ResolvePassword returns the account's password, trying the OS keyring
// first, then PasswordCmd, then the plaintext Password field. Any keyring
// error (not found, no Secret Service running, etc.) just falls through to
// the next method — the keyring is a best-effort first choice, not a hard
// requirement, since plenty of environments (headless boxes, sandboxes)
// don't have one available at all.
func (a Account) ResolvePassword() (string, error) {
	if pass, err := keyring.Get(keyringService, a.JID); err == nil {
		return pass, nil
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
