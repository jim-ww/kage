package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/crypto/gpg"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/xmpp"
)

// genTestGPGKey generates a throwaway EdDSA/Curve25519 keypair in a fresh,
// isolated keyring under t.TempDir(), returning its fingerprint. No
// passphrase, since these keys only exist for the duration of the test.
func genTestGPGKey(t *testing.T, homeDir, name, email string) string {
	t.Helper()
	script := filepath.Join(homeDir, "keygen")
	if err := os.WriteFile(script, []byte(`%no-protection
Key-Type: EDDSA
Key-Curve: ed25519
Subkey-Type: ECDH
Subkey-Curve: cv25519
Name-Real: `+name+`
Name-Email: `+email+`
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

	listCmd := exec.Command("gpg", "--batch", "--list-keys", "--with-colons", email)
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

// devtestTLSConfig loads the local Prosody test instance's self-signed cert,
// skipping the test if devtest/prosody hasn't been set up (see
// devtest/prosody/README.md) or its server isn't actually reachable right
// now (the cert file persists on disk even after the server's stopped, so
// checking only for the file's existence isn't enough).
func devtestTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	certPEM, err := os.ReadFile("devtest/prosody/certs/localhost.crt")
	if err != nil {
		t.Skipf("devtest/prosody not set up (run setup.sh + serve.sh): %v", err)
	}
	conn, err := net.DialTimeout("tcp", "localhost:5222", 500*time.Millisecond)
	if err != nil {
		t.Skipf("devtest/prosody not running (run serve.sh): %v", err)
	}
	conn.Close()
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	return &tls.Config{ServerName: "localhost", RootCAs: pool}
}

// TestPEPKeyDiscoveryFlow simulates alice and bob on separate machines (two
// separate, from-scratch gpg keyrings) to verify the full XEP-0373 flow --
// publish -> discover -> import -> cache -> encrypt -- end to end, with no
// manually configured gpg_peers entry anywhere.
func TestPEPKeyDiscoveryFlow(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()

	aliceKeyring := t.TempDir()
	aliceFpr := genTestGPGKey(t, aliceKeyring, "Alice Test", "alice-pep-test@example.com")
	bobKeyring := t.TempDir() // bob starts with a completely empty keyring

	origHome := os.Getenv("GNUPGHOME")
	defer os.Setenv("GNUPGHOME", origHome)

	aliceClient, err := xmpp.Dial(ctx, "alice@localhost", "alicepw", tlsConfig)
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}
	defer aliceClient.Close()
	bobClient, err := xmpp.Dial(ctx, "bob@localhost", "bobpw", tlsConfig)
	if err != nil {
		t.Fatalf("dial bob: %v", err)
	}
	defer bobClient.Close()

	aliceSess := &accountSession{account: config.Account{JID: "alice@localhost", GPGKeyID: aliceFpr}, gpg: gpg.Encrypter{}}
	aliceSess.client.Store(aliceClient)

	_, bobQ, err := storage.Open(filepath.Join(t.TempDir(), "bob.db"))
	if err != nil {
		t.Fatalf("open bob storage: %v", err)
	}
	bobSess := &accountSession{account: config.Account{JID: "bob@localhost"}, db: bobQ, gpg: gpg.Encrypter{}}
	bobSess.client.Store(bobClient)

	// Alice publishes her key under her own keyring.
	os.Setenv("GNUPGHOME", aliceKeyring)
	publishOwnGPGKey(ctx, aliceSess)

	// Bob, starting with a totally empty keyring, resolves alice's key
	// purely via PEP discovery.
	os.Setenv("GNUPGHOME", bobKeyring)
	fpr := resolvePeerKey(ctx, bobSess, "alice@localhost")
	if fpr != aliceFpr {
		t.Fatalf("resolvePeerKey = %q, want %q", fpr, aliceFpr)
	}

	// Confirm it actually landed in bob's (previously empty) keyring.
	if out, err := exec.Command("gpg", "--batch", "--list-keys", aliceFpr).CombinedOutput(); err != nil {
		t.Fatalf("key not found in bob's keyring after discovery: %v\n%s", err, out)
	}

	// Confirm it was cached, so a second call doesn't need the network.
	cached, err := bobQ.GetPGPPeerKey(ctx, storage.GetPGPPeerKeyParams{AccountJid: "bob@localhost", Jid: "alice@localhost"})
	if err != nil || cached != aliceFpr {
		t.Fatalf("expected cached fingerprint %q, got %q (err %v)", aliceFpr, cached, err)
	}

	// And bob can now actually encrypt to alice using the discovered key.
	ct, err := bobSess.gpg.Encrypt("hello via auto-discovered key", fpr)
	if err != nil {
		t.Fatalf("encrypt with discovered key: %v", err)
	}
	if !gpg.Looks(ct) {
		t.Fatalf("doesn't look like PGP ciphertext: %q", ct)
	}
}
