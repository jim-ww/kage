// Package aesgcm implements XEP-0454 (OMEMO Media Sharing) file encryption
// using AES-256-GCM for end-to-end encrypted file sharing via HTTP Upload.
package aesgcm

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

const (
	// IVSize is the initialization vector size in bytes (12 bytes per XEP-0454)
	IVSize = 12
	// KeySize is the encryption key size in bytes (32 bytes for AES-256)
	KeySize = 32
	// TagSize is the GCM authentication tag size in bytes (16 bytes)
	TagSize = 16
)

// Encrypt encrypts data using AES-256-GCM with a random IV and key.
// Returns the encrypted data (with auth tag appended), IV, and key.
func Encrypt(plaintext []byte) (ciphertext []byte, iv []byte, key []byte, err error) {
	iv = make([]byte, IVSize)
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return nil, nil, nil, fmt.Errorf("generating IV: %w", err)
	}

	key = make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, nil, nil, fmt.Errorf("generating key: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("creating cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("creating GCM: %w", err)
	}

	ciphertext = gcm.Seal(nil, iv, plaintext, nil)
	return ciphertext, iv, key, nil
}

// Decrypt decrypts data using AES-256-GCM with the given IV and key.
func Decrypt(ciphertext []byte, iv []byte, key []byte) ([]byte, error) {
	if len(iv) != IVSize {
		return nil, fmt.Errorf("invalid IV size: expected %d, got %d", IVSize, len(iv))
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("invalid key size: expected %d, got %d", KeySize, len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("creating cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}

	plaintext, err := gcm.Open(nil, iv, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypting: %w", err)
	}

	return plaintext, nil
}

// EncryptReader encrypts data from reader using AES-256-GCM with a random IV and key.
// Returns the encrypted data (with auth tag appended), IV, and key.
func EncryptReader(reader io.Reader) (ciphertext []byte, iv []byte, key []byte, err error) {
	plaintext, err := io.ReadAll(reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("reading plaintext: %w", err)
	}
	return Encrypt(plaintext)
}

// BuildAESGCMURL converts an HTTPS URL and IV+key to an aesgcm:// URL per
// XEP-0454. The aesgcm:// scheme *replaces* https://, it doesn't prefix it -
// "aesgcm://host/path", not "aesgcm://https://host/path" (the latter is what
// this used to produce, which every other client - Dino, Conversations,
// etc. - rejects as malformed, showing "File transfer failed"). Both IV and
// key are hex-encoded and concatenated in the anchor (IV first, then key).
func BuildAESGCMURL(httpsURL string, iv []byte, key []byte) (string, error) {
	if len(iv) != IVSize {
		return "", fmt.Errorf("invalid IV size: expected %d, got %d", IVSize, len(iv))
	}
	if len(key) != KeySize {
		return "", fmt.Errorf("invalid key size: expected %d, got %d", KeySize, len(key))
	}

	rest, ok := strings.CutPrefix(httpsURL, "https://")
	if !ok {
		rest, ok = strings.CutPrefix(httpsURL, "http://")
		if !ok {
			return "", fmt.Errorf("not an http(s) URL: %q", httpsURL)
		}
	}

	ivHex := hex.EncodeToString(iv)
	keyHex := hex.EncodeToString(key)
	anchor := ivHex + keyHex

	return "aesgcm://" + rest + "#" + anchor, nil
}

// ParseAESGCMURL extracts the HTTPS URL, IV, and key from an aesgcm:// URL.
func ParseAESGCMURL(aesgcmURL string) (httpsURL string, iv []byte, key []byte, err error) {
	// Parse the URL to extract the scheme and anchor
	// Format: aesgcm://<https_url>#<iv_hex><key_hex>
	prefix := "aesgcm://"
	if len(aesgcmURL) < len(prefix) || aesgcmURL[:len(prefix)] != prefix {
		return "", nil, nil, fmt.Errorf("not an aesgcm URL")
	}

	// Find the anchor separator
	hashIdx := -1
	for i, c := range aesgcmURL {
		if c == '#' {
			hashIdx = i
			break
		}
	}
	if hashIdx == -1 {
		return "", nil, nil, fmt.Errorf("missing anchor in aesgcm URL")
	}

	httpsURL = "https://" + aesgcmURL[len(prefix):hashIdx]
	anchor := aesgcmURL[hashIdx+1:]

	// Anchor should be 88 hex chars (44 bytes: 12 IV + 32 key)
	expectedLen := IVSize*2 + KeySize*2 // 24 + 64 = 88
	if len(anchor) != expectedLen {
		return "", nil, nil, fmt.Errorf("invalid anchor length: expected %d, got %d", expectedLen, len(anchor))
	}

	iv, err = hex.DecodeString(anchor[:IVSize*2])
	if err != nil {
		return "", nil, nil, fmt.Errorf("decoding IV: %w", err)
	}

	key, err = hex.DecodeString(anchor[IVSize*2:])
	if err != nil {
		return "", nil, nil, fmt.Errorf("decoding key: %w", err)
	}

	return httpsURL, iv, key, nil
}
