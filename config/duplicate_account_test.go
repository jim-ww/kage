package config

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestWriteAccountRefusesDuplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := WriteAccount(path, Account{JID: "alice@example.org"}); err != nil {
		t.Fatal(err)
	}
	for _, jid := range []string{"alice@example.org", "Alice@Example.ORG", " alice@example.org/phone"} {
		if exists, err := AccountExists(path, jid); err != nil || !exists {
			t.Errorf("AccountExists(%q) = %v, %v", jid, exists, err)
		}
		if err := WriteAccount(path, Account{JID: jid}); !errors.Is(err, ErrAccountExists) {
			t.Errorf("WriteAccount(%q) = %v, want ErrAccountExists", jid, err)
		}
	}
	if err := WriteAccount(path, Account{JID: "bob@example.org"}); err != nil {
		t.Fatalf("a different account: %v", err)
	}
	cfg, err := loadOrEmpty(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Accounts) != 2 {
		t.Fatalf("%d accounts saved, want 2", len(cfg.Accounts))
	}

	// Removing one frees its JID to be added again.
	if err := RemoveAccount(path, "alice@example.org"); err != nil {
		t.Fatal(err)
	}
	if err := WriteAccount(path, Account{JID: "alice@example.org"}); err != nil {
		t.Fatalf("re-adding a removed account: %v", err)
	}
}
