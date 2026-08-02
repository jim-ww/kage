// Package gpg implements crypto.Encrypter by shelling out to the system gpg
// binary, reusing the user's existing keyring and gpg-agent for passphrases.
package gpg

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Armor is the marker gpg puts at the start of an ASCII-armored message,
// used to sniff whether an incoming message body is GPG ciphertext.
const Armor = "-----BEGIN PGP MESSAGE-----"

// Looks reports whether body appears to be an ASCII-armored PGP message.
func Looks(body string) bool {
	return strings.Contains(body, Armor)
}

// Encrypter shells out to gpg for encryption/decryption.
type Encrypter struct {
	// Timeout bounds each gpg invocation. Defaults to 10s if zero.
	Timeout time.Duration
}

func (Encrypter) Name() string { return "gpg" }

func (e Encrypter) timeout() time.Duration {
	if e.Timeout > 0 {
		return e.Timeout
	}
	return 10 * time.Second
}

// Encrypt armors and encrypts plaintext to recipientKeyID using the local
// keyring (gpg --encrypt -r <keyID>).
func (e Encrypter) Encrypt(plaintext string, recipientKeyID string) (string, error) {
	if recipientKeyID == "" {
		return "", fmt.Errorf("gpg: no recipient key id configured")
	}
	out, err := e.run(plaintext, "--batch", "--yes", "--armor", "--trust-model", "always", "--encrypt", "-r", recipientKeyID)
	if err != nil {
		return "", fmt.Errorf("gpg encrypt: %w", err)
	}
	return out, nil
}

// Decrypt decrypts an ASCII-armored ciphertext with the local keyring/agent.
// senderKeyID is currently unused (gpg determines the right secret key from
// the message's recipient list itself) but kept for interface symmetry with
// future Encrypter implementations that need it.
func (e Encrypter) Decrypt(ciphertext string, _ string) (string, error) {
	out, err := e.run(ciphertext, "--batch", "--yes", "--decrypt")
	if err != nil {
		return "", fmt.Errorf("gpg decrypt: %w", err)
	}
	return out, nil
}

// DefaultSecretKeyID picks a key ID to use when none is configured: the
// keyring's configured default-key (gpg.conf) if set, otherwise the sole
// secret key if there's exactly one. Returns an error (never guesses) if
// there are zero or multiple candidates and no default-key is configured —
// callers should treat that as "couldn't auto-detect", not a hard failure.
func DefaultSecretKeyID() (string, error) {
	if fpr := defaultKeyFromConf(); fpr != "" {
		return fpr, nil
	}

	fprs, err := listSecretKeyFingerprints()
	if err != nil {
		return "", err
	}
	switch len(fprs) {
	case 0:
		return "", fmt.Errorf("no secret keys found in the local gpg keyring")
	case 1:
		return fprs[0], nil
	default:
		return "", fmt.Errorf("%d secret keys found; set gpg_key_id explicitly to pick one", len(fprs))
	}
}

// defaultKeyFromConf reads gpg.conf's "default-key" directive, if set.
// Returns "" if there's no gpg.conf or no such directive — not an error,
// since most keyrings don't set one.
func defaultKeyFromConf() string {
	home := os.Getenv("GNUPGHOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		home = filepath.Join(h, ".gnupg")
	}
	data, err := os.ReadFile(filepath.Join(home, "gpg.conf"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 2 && fields[0] == "default-key" {
			return fields[1]
		}
	}
	return ""
}

// listSecretKeyFingerprints returns the primary-key fingerprint of every
// secret key in the local keyring, parsed from gpg's stable --with-colons
// machine-readable output (each "sec" record is immediately followed by an
// "fpr" record giving its fingerprint; subkeys use "ssb"/their own "fpr",
// which are skipped here since only the primary key ID is a valid -r target).
func listSecretKeyFingerprints() ([]string, error) {
	out, err := exec.Command("gpg", "--batch", "--list-secret-keys", "--with-colons").Output()
	if err != nil {
		return nil, fmt.Errorf("listing secret keys: %w", err)
	}

	var fprs []string
	expectFpr := false
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "sec":
			expectFpr = true
		case "fpr":
			if expectFpr && len(fields) > 9 {
				fprs = append(fprs, fields[9])
			}
			expectFpr = false
		default:
			expectFpr = false
		}
	}
	return fprs, nil
}

func (e Encrypter) run(stdin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), e.timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, "gpg", args...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	return stdout.String(), nil
}
