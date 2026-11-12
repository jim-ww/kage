package ui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jim-ww/kage/crypto/aesgcm"
)

// TestOpenableItemsDedupesOwnUpload guards against a real bug: SendFile puts
// the uploaded file's URL in both Message.Attachments and (since the whole
// message body is just that URL) Message.Content, so urlPattern matches it a
// second time — without deduping, the open/save picker showed the same
// upload twice as separate numbered entries.
func TestOpenableItemsDedupesOwnUpload(t *testing.T) {
	msg := Message{
		Content:     "https://upload.example.com/abc/file.txt",
		Attachments: []string{"https://upload.example.com/abc/file.txt"},
	}
	got := openableItems(msg)
	want := []string{"https://upload.example.com/abc/file.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("openableItems = %v, want %v", got, want)
	}
}

// TestOpenableItemsKeepsDistinctLinks checks the dedup logic doesn't
// over-collapse: an attachment plus a genuinely different link in the body
// (e.g. someone shares a file and comments with a URL) must both survive.
func TestOpenableItemsKeepsDistinctLinks(t *testing.T) {
	msg := Message{
		Content:     "check this out too: https://example.com/other",
		Attachments: []string{"https://upload.example.com/abc/file.txt"},
	}
	got := openableItems(msg)
	want := []string{"https://upload.example.com/abc/file.txt", "https://example.com/other"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("openableItems = %v, want %v", got, want)
	}
}

// TestAttachmentLocalPathUsesCacheDirRegardlessOfEncryption guards the
// invariant that Ctrl+O always lands a real attachment in AttachmentsDir
// (see downloadAndOpen) whether it's an aesgcm:// (OMEMO) upload or a plain
// http(s):// one — attachmentLocalPath must agree, or the "(downloaded)"
// indicator and Ctrl+O's own re-download check disagree with reality.
func TestAttachmentLocalPathUsesCacheDirRegardlessOfEncryption(t *testing.T) {
	dir := t.TempDir()
	old := AttachmentsDir
	AttachmentsDir = dir
	defer func() { AttachmentsDir = old }()

	plain := attachmentLocalPath("https://upload.example.com/abc/file.txt", "alice@example.com")
	if plain == "" {
		t.Fatal("attachmentLocalPath returned empty for a plain http attachment")
	}
	if !strings.HasPrefix(plain, filepath.Join(dir, "alice@example.com")+string(filepath.Separator)) {
		t.Fatalf("plain http attachment path = %q, want it under %q (AttachmentsDir), not the downloads dir", plain, filepath.Join(dir, "alice@example.com"))
	}

	anchor := strings.Repeat("ab", 12) + strings.Repeat("cd", 32) // 12-byte IV + 32-byte key, hex-encoded
	encrypted := attachmentLocalPath("aesgcm://upload.example.com/abc/file.txt#"+anchor, "alice@example.com")
	if !strings.HasPrefix(encrypted, filepath.Join(dir, "alice@example.com")+string(filepath.Separator)) {
		t.Fatalf("aesgcm attachment path = %q, want it under %q (AttachmentsDir)", encrypted, filepath.Join(dir, "alice@example.com"))
	}
}

// TestDownloadToDest checks a save writes the file - decrypted, for an
// aesgcm:// one - and caches it for opening later.
func TestDownloadToDest(t *testing.T) {
	plain := []byte("attachment body")
	ct, iv, key, err := aesgcm.Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/enc/") {
			w.Write(ct)
			return
		}
		w.Write(plain)
	}))
	defer srv.Close()
	prevClient, prevDir := http.DefaultClient, AttachmentsDir
	http.DefaultClient, AttachmentsDir = srv.Client(), t.TempDir()
	t.Cleanup(func() { http.DefaultClient, AttachmentsDir = prevClient, prevDir })

	encURL, err := aesgcm.BuildAESGCMURL(srv.URL+"/enc/file.txt", iv, key)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{srv.URL + "/plain/file.txt", encURL} {
		dest := filepath.Join(t.TempDir(), "saved.txt")
		msg := downloadToDest(target, dest, "bob@example.test", make(chan tea.Msg, 64)).(saveResultMsg)
		if msg.err != nil || msg.path != dest {
			t.Fatalf("%s: path %q err %v", target, msg.path, msg.err)
		}
		for _, path := range []string{dest, attachmentLocalPath(target, "bob@example.test")} {
			if got, err := os.ReadFile(path); err != nil || string(got) != string(plain) {
				t.Fatalf("%s: %s has %q (%v), want %q", target, path, got, err, plain)
			}
		}
	}
}
