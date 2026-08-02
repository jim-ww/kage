package gpg

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// genTestKey generates a throwaway EdDSA/Curve25519 keypair in homeDir
// (a fresh keyring), returning its fingerprint.
func genTestKey(t *testing.T, homeDir string) string {
	t.Helper()
	script := filepath.Join(homeDir, "keygen")
	if err := os.WriteFile(script, []byte(`%no-protection
Key-Type: EDDSA
Key-Curve: ed25519
Subkey-Type: ECDH
Subkey-Curve: cv25519
Name-Real: Test User
Name-Email: test@example.com
Expire-Date: 0
%commit
`), 0o600); err != nil {
		t.Fatalf("writing keygen script: %v", err)
	}

	cmd := exec.Command("gpg", "--batch", "--gen-key", script)
	cmd.Env = append(os.Environ(), "GNUPGHOME="+homeDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("gpg --gen-key: %v\n%s", err, out)
	}

	listCmd := exec.Command("gpg", "--batch", "--list-keys", "--with-colons")
	listCmd.Env = append(os.Environ(), "GNUPGHOME="+homeDir)
	out, err := listCmd.Output()
	if err != nil {
		t.Fatalf("gpg --list-keys: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) > 9 && fields[0] == "fpr" {
			return fields[9]
		}
	}
	t.Fatal("couldn't find generated key's fingerprint")
	return ""
}

func withGNUPGHOME(t *testing.T, dir string) {
	t.Helper()
	orig := os.Getenv("GNUPGHOME")
	os.Setenv("GNUPGHOME", dir)
	t.Cleanup(func() { os.Setenv("GNUPGHOME", orig) })
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	withGNUPGHOME(t, t.TempDir())
	fpr := genTestKey(t, os.Getenv("GNUPGHOME"))

	e := Encrypter{}
	ct, err := e.Encrypt("hello world", fpr)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !Looks(ct) {
		t.Fatalf("ciphertext doesn't look armored: %q", ct)
	}
	pt, err := e.Decrypt(ct, fpr)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if pt != "hello world" {
		t.Fatalf("got %q", pt)
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	srcHome := t.TempDir()
	withGNUPGHOME(t, srcHome)
	fpr := genTestKey(t, srcHome)

	e := Encrypter{}
	data, err := e.Export(fpr)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("exported no data")
	}

	// Import into a second, empty keyring — simulating a peer receiving it.
	dstHome := t.TempDir()
	withGNUPGHOME(t, dstHome)
	if err := e.Import(data, fpr); err != nil {
		t.Fatalf("import: %v", err)
	}
	if out, err := exec.Command("gpg", "--batch", "--list-keys", fpr).CombinedOutput(); err != nil {
		t.Fatalf("key not found after import: %v\n%s", err, out)
	}

	// A mismatched expected fingerprint must be rejected.
	if err := e.Import(data, "0000000000000000000000000000000000000A"); err == nil {
		t.Fatal("expected fingerprint mismatch to be rejected")
	}
}

func TestDefaultSecretKeyID(t *testing.T) {
	withGNUPGHOME(t, t.TempDir())

	if _, err := DefaultSecretKeyID(); err == nil {
		t.Fatal("expected an error with zero secret keys")
	}

	fpr := genTestKey(t, os.Getenv("GNUPGHOME"))
	got, err := DefaultSecretKeyID()
	if err != nil {
		t.Fatalf("DefaultSecretKeyID with exactly one key: %v", err)
	}
	if got != fpr {
		t.Fatalf("got %q, want %q", got, fpr)
	}

	genTestKey(t, os.Getenv("GNUPGHOME")) // second key -> now ambiguous
	if _, err := DefaultSecretKeyID(); err == nil {
		t.Fatal("expected an error with multiple secret keys and no default-key configured")
	}
}
