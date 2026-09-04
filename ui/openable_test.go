package ui

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
