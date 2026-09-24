package main

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// A bare JID arrives off the network and becomes a file name, so anything
// that could escape the cache directory has to be neutralized.
func TestAvatarFileName(t *testing.T) {
	tests := []struct {
		bare string
		want string
	}{
		{"alice@localhost", "alice@localhost.png"},
		{"a.b@example.com", "a.b@example.com.png"},
		{"../../etc/passwd", ".._.._etc_passwd.png"},
		{"a/b@example.com", "a_b@example.com.png"},
		{"", ""},
		{".", ""},
		{"..", ""},
	}
	for _, tt := range tests {
		if got := avatarFileName(tt.bare, ".png"); got != tt.want {
			t.Errorf("avatarFileName(%q) = %q, want %q", tt.bare, got, tt.want)
		}
	}
}

func TestAvatarFileNameStaysInDir(t *testing.T) {
	dir := t.TempDir()
	for _, bare := range []string{"../escape@x", "a/b@x", "..", "sub/../../x@y"} {
		name := avatarFileName(bare, ".png")
		if name == "" {
			continue
		}
		full := filepath.Clean(filepath.Join(dir, name))
		if filepath.Dir(full) != filepath.Clean(dir) {
			t.Errorf("avatarFileName(%q) = %q escapes to %q", bare, name, full)
		}
	}
}

func TestAvatarExt(t *testing.T) {
	tests := []struct {
		mediaType string
		want      string
		wantOK    bool
	}{
		{"image/png", ".png", true},
		{"IMAGE/PNG", ".png", true},
		{" image/jpeg ", ".jpg", true},
		{"image/jpg", ".jpg", true},
		{"", ".png", true}, // unset in the wild; the decoder sniffs anyway
		{"image/gif", "", false},
		{"image/webp", "", false},
		{"text/html", "", false},
	}
	for _, tt := range tests {
		got, ok := avatarExt(tt.mediaType)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("avatarExt(%q) = (%q, %v), want (%q, %v)", tt.mediaType, got, ok, tt.want, tt.wantOK)
		}
	}
}

// The cached file's own SHA-1 is the whole freshness check — it's compared
// straight against the id XEP-0084 publishes.
func TestCachedAvatarHash(t *testing.T) {
	dir := t.TempDir()
	if got := cachedAvatarHash(dir, "nobody@example.com"); got != "" {
		t.Errorf("cachedAvatarHash = %q for an uncached contact, want empty", got)
	}

	data := []byte("pretend png bytes")
	sum := sha1.Sum(data)
	want := hex.EncodeToString(sum[:])
	if err := os.WriteFile(filepath.Join(dir, "alice@localhost.png"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := cachedAvatarHash(dir, "alice@localhost"); got != want {
		t.Errorf("cachedAvatarHash = %q, want %q", got, want)
	}

	// Found under either extension, since a contact may publish JPEG.
	if err := os.WriteFile(filepath.Join(dir, "bob@localhost.jpg"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := cachedAvatarHash(dir, "bob@localhost"); got != want {
		t.Errorf("cachedAvatarHash for a jpg = %q, want %q", got, want)
	}
}

// The TUI reads this directory while the daemon writes it, so a reader must
// never catch a half-written image.
func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alice@localhost.png")

	if err := writeFileAtomic(path, []byte("first")); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "first" {
		t.Fatalf("read back %q, %v", got, err)
	}

	if err := writeFileAtomic(path, []byte("second")); err != nil {
		t.Fatalf("overwriting: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "second" {
		t.Errorf("read back %q, want %q", got, "second")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}

	// No temp files left behind on success.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want just the avatar", names)
	}
}
