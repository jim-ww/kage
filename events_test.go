package main

import (
	"strings"
	"testing"
)

func TestStripReplyQuoteRemovesLeadingQuoteBlock(t *testing.T) {
	body := "> Bob\n> photo.jpg\naesgcm://host/photo.jpg#key"
	got := stripReplyQuote(body)
	want := "aesgcm://host/photo.jpg#key"
	if got != want {
		t.Fatalf("stripReplyQuote(%q) = %q, want %q", body, got, want)
	}
}

func TestStripReplyQuoteLeavesUnquotedBodyUnchanged(t *testing.T) {
	body := "aesgcm://host/photo.jpg#key"
	if got := stripReplyQuote(body); got != body {
		t.Fatalf("stripReplyQuote(%q) = %q, want unchanged", body, got)
	}
}

func TestAesgcmURLsInBodyMatchesTrailingAesgcmLines(t *testing.T) {
	body := "qweqweqwewqewq\naesgcm://host/a.jpg#key1\naesgcm://host/b.png#key2"
	got := aesgcmURLsInBody(body)
	want := []string{"aesgcm://host/a.jpg#key1", "aesgcm://host/b.png#key2"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("aesgcmURLsInBody(%q) = %v, want %v", body, got, want)
	}
}

func TestAesgcmURLsInBodyIgnoresPlainLinks(t *testing.T) {
	// A plain https:// link is ambiguous (could be a real upload or just a
	// shared link) - unlike aesgcm://, which nothing but a file share ever
	// produces, so this must never treat it as an attachment.
	body := "check this out\nhttps://example.com/not-a-file"
	if got := aesgcmURLsInBody(body); got != nil {
		t.Fatalf("aesgcmURLsInBody(%q) = %v, want nil", body, got)
	}
}

func TestNotifyPreviewShowsNormalizedFilenameForAttachment(t *testing.T) {
	iv := strings.Repeat("aa", 12)  // IVSize=12 bytes
	key := strings.Repeat("bb", 32) // KeySize=32 bytes
	body := "aesgcm://host/photo%20booth.jpg#" + iv + key
	got := notifyPreview(body, []string{body})
	want := "📎 photo booth.jpg"
	if got != want {
		t.Fatalf("notifyPreview(%q, attachments) = %q, want %q", body, got, want)
	}
}

func TestNotifyPreviewFallsBackToBodyWhenNotAttachmentOnly(t *testing.T) {
	body := "check this out: aesgcm://host/a.jpg#key"
	got := notifyPreview(body, []string{"aesgcm://host/a.jpg#key"})
	if got != body {
		t.Fatalf("notifyPreview(%q, attachments) = %q, want unchanged body", body, got)
	}
}
