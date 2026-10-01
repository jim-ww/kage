package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Every Set* helper is a load-edit-write of the whole file, and the daemon
// runs each RPC on its own goroutine for any number of attached TUIs - so
// concurrent edits must not drop each other's writes.
func TestConcurrentStateWritesKeepEveryEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	const n = 50
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := RecordReactionEmojiUsage(path, []string{"👍"}); err != nil {
				t.Errorf("RecordReactionEmojiUsage: %v", err)
			}
		}()
	}
	wg.Wait()

	st, err := loadState(path)
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if got := st.ReactionEmojiUsage["👍"]; got != n {
		t.Errorf("usage count = %d after %d concurrent increments, want %d", got, n, n)
	}
}

// Same thing across different fields and both files: a concurrent config.toml
// edit must not lose a state.toml one, or either of two state fields.
func TestConcurrentWritesAcrossFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	var wg sync.WaitGroup
	edits := []func() error{
		func() error { return SetSidebarWidth(path, 42) },
		func() error { return SetLastChat(path, "alice@localhost", "bob@localhost") },
		func() error { return SetChatPinned(path, "alice@localhost", "carol@localhost", true) },
		func() error { return WriteAccount(path, Account{JID: "alice@localhost"}) },
	}
	for _, edit := range edits {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := edit(); err != nil {
				t.Errorf("edit: %v", err)
			}
		}()
	}
	wg.Wait()

	st, err := loadState(path)
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if st.SidebarWidth != 42 {
		t.Errorf("SidebarWidth = %d, want 42", st.SidebarWidth)
	}
	if st.LastChatAddress != "bob@localhost" {
		t.Errorf("LastChatAddress = %q, want %q", st.LastChatAddress, "bob@localhost")
	}
	if got := st.PinnedChats["alice@localhost"]; len(got) != 1 || got[0] != "carol@localhost" {
		t.Errorf("PinnedChats = %v, want [carol@localhost]", got)
	}
	cfg, err := loadOrEmpty(path)
	if err != nil {
		t.Fatalf("loadOrEmpty: %v", err)
	}
	if len(cfg.Accounts) != 1 || cfg.Accounts[0].JID != "alice@localhost" {
		t.Errorf("Accounts = %v, want one alice@localhost", cfg.Accounts)
	}
}

// A write must never leave a temp file behind, and must land with the same
// 0600 permissions os.WriteFile used to give it.
func TestWriteFileAtomicLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := SetSidebarWidth(path, 7); err != nil {
		t.Fatalf("SetSidebarWidth: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.toml" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("directory holds %v, want just state.toml", names)
	}

	info, err := os.Stat(filepath.Join(dir, "state.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("state.toml mode = %v, want 0600", got)
	}
}
