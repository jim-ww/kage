// Package localstore provides fast, purely local at-rest encryption for
// message history: AES-256-GCM under a key derived from a password via
// Argon2id. This is distinct from the OpenPGP layer used for actual
// peer-to-peer wire encryption (crypto/gpg), which has to stay asymmetric
// and interoperate with contacts' own PGP clients — the local key here
// never leaves this process and gates nothing but our own copy of the data.
package localstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"

	"golang.org/x/crypto/argon2"
)

// KeySize is the AES-256 key length, in bytes.
const KeySize = 32

// SaltSize is the recommended salt length for Argon2id.
const SaltSize = 16

// NewSalt generates a random salt for DeriveKey. The salt isn't secret — it
// just needs to be unique and persisted alongside (not derived from) the
// password.
func NewSalt() ([]byte, error) {
	salt := make([]byte, SaltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generating salt: %w", err)
	}
	return salt, nil
}

// DeriveKey derives an AES-256 key from password and salt using Argon2id.
func DeriveKey(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, 1, 64*1024, 4, KeySize)
}

// Seal encrypts plaintext under key (AES-256-GCM, random nonce prepended to
// the ciphertext), returning a base64 string safe to store as TEXT.
func Seal(key []byte, plaintext string) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generating nonce: %w", err)
	}
	ct := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

// Open decrypts a blob produced by Seal.
func Open(key []byte, sealed string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", fmt.Errorf("decoding sealed blob: %w", err)
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("sealed blob too short")
	}
	nonce, ct := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("opening sealed blob: %w", err)
	}
	return string(pt), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("building AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("building GCM mode: %w", err)
	}
	return gcm, nil
}
