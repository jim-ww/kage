package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestManagedConfigNotWritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := ManagedHeader + ": edits here are overwritten on activation.\n[[accounts]]\njid = \"a@b\"\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteAccount(path, Account{JID: "c@d"}); !errors.Is(err, ErrManaged) {
		t.Fatalf("WriteAccount on a managed config: err = %v, want ErrManaged", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != content {
		t.Fatalf("managed config changed:\n%s", got)
	}
}

func TestUnmanagedConfigWritable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := CheckWritable(path); err != nil {
		t.Fatalf("missing config: %v", err)
	}
	if err := WriteAccount(path, Account{JID: "c@d"}); err != nil {
		t.Fatal(err)
	}
	if err := CheckWritable(path); err != nil {
		t.Fatalf("config kage wrote itself: %v", err)
	}
}
