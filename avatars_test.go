package main

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/xmpp"
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

func TestStoreAvatarReplacesOtherExtension(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)

	const bare = "me@example.com"
	if err := storeAvatar(bare, []byte("png bytes"), "image/png"); err != nil {
		t.Fatalf("storeAvatar png: %v", err)
	}
	path, ok := cachedAvatarPath(bare)
	if !ok || filepath.Ext(path) != ".png" {
		t.Fatalf("cachedAvatarPath = %q, %v, want a .png", path, ok)
	}

	// Switching format must not leave both files behind — LoadAvatarDir
	// would then load whichever it happened to reach last.
	if err := storeAvatar(bare, []byte("jpeg bytes"), "image/jpeg"); err != nil {
		t.Fatalf("storeAvatar jpeg: %v", err)
	}
	path, ok = cachedAvatarPath(bare)
	if !ok || filepath.Ext(path) != ".jpg" {
		t.Fatalf("cachedAvatarPath = %q, %v, want a .jpg", path, ok)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), bare+".png")); !os.IsNotExist(err) {
		t.Error("the old .png was left behind")
	}
}

func TestRemoveCachedAvatar(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)

	const bare = "me@example.com"
	if err := storeAvatar(bare, []byte("png bytes"), "image/png"); err != nil {
		t.Fatal(err)
	}
	removeCachedAvatar(bare)
	if path, ok := cachedAvatarPath(bare); ok {
		t.Errorf("cachedAvatarPath = %q after removal", path)
	}
}

func TestStoreAvatarRejectsUnsupportedType(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := storeAvatar("me@example.com", []byte("gif bytes"), "image/gif"); err == nil {
		t.Error("storeAvatar accepted an unsupported media type")
	}
}

func TestHumanBytes(t *testing.T) {
	tests := []struct {
		in   int
		want string
	}{
		{0, "0 bytes"},
		{512, "512 bytes"},
		{2048, "2 KB"},
		{1 << 20, "1.0 MB"},
		{3 << 19, "1.5 MB"},
	}
	for _, tt := range tests {
		if got := humanBytes(tt.in); got != tt.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// ui can't import xmpp, so the limit the avatar preview refuses oversized
// files against is a mirror of the wire limit. This is the only place that
// sees both.
func TestAvatarMaxBytesMatchesTheWireLimit(t *testing.T) {
	if ui.AvatarMaxBytes != xmpp.AvatarMaxBytes {
		t.Errorf("ui.AvatarMaxBytes = %d, want xmpp.AvatarMaxBytes (%d)", ui.AvatarMaxBytes, xmpp.AvatarMaxBytes)
	}
}
