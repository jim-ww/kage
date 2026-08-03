package aesgcm

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptFileIntegration(t *testing.T) {
	// Create a temporary test file
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	testContent := []byte("Hello, this is a test file for encrypted upload!")

	err := os.WriteFile(testFile, testContent, 0644)
	if err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	// Read and encrypt the file
	f, err := os.Open(testFile)
	if err != nil {
		t.Fatalf("opening test file: %v", err)
	}
	defer f.Close()

	ciphertext, iv, key, err := EncryptReader(f)
	if err != nil {
		t.Fatalf("encrypting file: %v", err)
	}

	// Verify IV and key sizes
	if len(iv) != IVSize {
		t.Errorf("IV size: expected %d, got %d", IVSize, len(iv))
	}
	if len(key) != KeySize {
		t.Errorf("Key size: expected %d, got %d", KeySize, len(key))
	}

	// Decrypt and verify
	decrypted, err := Decrypt(ciphertext, iv, key)
	if err != nil {
		t.Fatalf("decrypting: %v", err)
	}

	if !bytes.Equal(testContent, decrypted) {
		t.Errorf("decrypted content doesn't match original")
	}

	// Test URL building
	httpsURL := "https://example.com/uploads/test.txt"
	aesgcmURL, err := BuildAESGCMURL(httpsURL, iv, key)
	if err != nil {
		t.Fatalf("building aesgcm URL: %v", err)
	}

	// Parse and verify
	parsedHTTPS, parsedIV, parsedKey, err := ParseAESGCMURL(aesgcmURL)
	if err != nil {
		t.Fatalf("parsing aesgcm URL: %v", err)
	}

	if parsedHTTPS != httpsURL {
		t.Errorf("HTTPS URL mismatch: expected %s, got %s", httpsURL, parsedHTTPS)
	}
	if !bytes.Equal(iv, parsedIV) {
		t.Errorf("IV mismatch after round-trip")
	}
	if !bytes.Equal(key, parsedKey) {
		t.Errorf("Key mismatch after round-trip")
	}
}
