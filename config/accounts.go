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

// Account is one configured XMPP account.
type Account struct {
	JID         string            `toml:"jid"`
	Password    string            `toml:"password"`     // plaintext fallback
	PasswordCmd string            `toml:"password_cmd"` // shell command printing the password on stdout
	GPGKeyID    string            `toml:"gpg_key_id"`   // own key, used to decrypt/sign
	GPGPeers    map[string]string `toml:"gpg_peers"`    // JID -> peer's key fingerprint
}

// ResolvePassword returns the account's password, trying the OS keyring
// first, then PasswordCmd, then the plaintext Password field.
func (a Account) ResolvePassword() (string, error) {
	if pass, err := keyring.Get(keyringService, a.JID); err == nil {
		return pass, nil
	} else if !errors.Is(err, keyring.ErrNotFound) {
		return "", fmt.Errorf("reading keyring for %s: %w", a.JID, err)
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
