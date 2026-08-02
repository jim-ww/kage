// Package gpg implements crypto.Encrypter by shelling out to the system gpg
// binary, reusing the user's existing keyring and gpg-agent for passphrases.
package gpg

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
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
