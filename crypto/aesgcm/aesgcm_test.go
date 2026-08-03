package aesgcm

import (
	"bytes"
	"strings"
	"testing"
)

func TestEncryptDecrypt(t *testing.T) {
	plaintext := []byte("Hello, World!")

	ciphertext, iv, key, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	if len(iv) != IVSize {
		t.Errorf("IV size: expected %d, got %d", IVSize, len(iv))
	}
	if len(key) != KeySize {
		t.Errorf("Key size: expected %d, got %d", KeySize, len(key))
	}

	decrypted, err := Decrypt(ciphertext, iv, key)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}

	if !bytes.Equal(plaintext, decrypted) {
		t.Errorf("Decrypted data doesn't match original")
	}
}

func TestEncryptDecryptEmpty(t *testing.T) {
	plaintext := []byte("")

	ciphertext, iv, key, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	decrypted, err := Decrypt(ciphertext, iv, key)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}

	if !bytes.Equal(plaintext, decrypted) {
		t.Errorf("Decrypted data doesn't match original")
	}
}

func TestBuildParseAESGCMURL(t *testing.T) {
	iv := make([]byte, IVSize)
	key := make([]byte, KeySize)
	for i := range iv {
		iv[i] = byte(i)
	}
	for i := range key {
		key[i] = byte(i + IVSize)
	}

	httpsURL := "https://example.com/file.bin"
	aesgcmURL, err := BuildAESGCMURL(httpsURL, iv, key)
	if err != nil {
		t.Fatalf("BuildAESGCMURL failed: %v", err)
	}

	t.Logf("Generated aesgcm URL: %s", aesgcmURL)

	if !strings.HasPrefix(aesgcmURL, "aesgcm://") {
		t.Errorf("URL doesn't start with aesgcm://")
	}

	parsedHTTPS, parsedIV, parsedKey, err := ParseAESGCMURL(aesgcmURL)
	if err != nil {
		t.Fatalf("ParseAESGCMURL failed: %v", err)
	}

	if parsedHTTPS != httpsURL {
		t.Errorf("HTTPS URL mismatch: expected %s, got %s", httpsURL, parsedHTTPS)
	}
	if !bytes.Equal(iv, parsedIV) {
		t.Errorf("IV mismatch")
	}
	if !bytes.Equal(key, parsedKey) {
		t.Errorf("Key mismatch")
	}
}

func TestParseAESGCMURLInvalid(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"not aesgcm", "https://example.com/file"},
		{"missing anchor", "aesgcm://https://example.com/file"},
		{"invalid anchor length", "aesgcm://https://example.com/file#abc123"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := ParseAESGCMURL(tt.url)
			if err == nil {
				t.Errorf("Expected error for invalid URL")
			}
		})
	}
}

func TestDecryptInvalidIV(t *testing.T) {
	plaintext := []byte("test")
	ciphertext, _, key, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	badIV := make([]byte, IVSize-1)
	_, err = Decrypt(ciphertext, badIV, key)
	if err == nil {
		t.Errorf("Expected error for invalid IV size")
	}
}

func TestDecryptInvalidKey(t *testing.T) {
	plaintext := []byte("test")
	ciphertext, iv, _, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	badKey := make([]byte, KeySize-1)
	_, err = Decrypt(ciphertext, iv, badKey)
	if err == nil {
		t.Errorf("Expected error for invalid key size")
	}
}
