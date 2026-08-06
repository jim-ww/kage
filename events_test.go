package main

import "testing"

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

func TestResolveAttachmentsPrefersOOB(t *testing.T) {
	// A plain shared link (not a real upload) still ends its own line, which
	// would otherwise trip the body-text heuristic; an explicit OOB signal
	// from the sender must win regardless.
	body := "check this out\nhttps://example.com/not-a-file"
	got := resolveAttachments(body, nil)
	if len(got) != 1 || got[0] != "https://example.com/not-a-file" {
		t.Fatalf("resolveAttachments(%q, nil) = %v, want heuristic fallback to match the trailing URL", body, got)
	}

	oob := []string{"https://example.com/real-file.jpg"}
	got = resolveAttachments(body, oob)
	if len(got) != 1 || got[0] != oob[0] {
		t.Fatalf("resolveAttachments(%q, %v) = %v, want OOB URLs verbatim", body, oob, got)
	}
}

func TestAttachmentURLsAfterStrippingReplyQuote(t *testing.T) {
	body := "> Bob\n> photo.jpg\naesgcm://host/photo.jpg#key"
	stripped := stripReplyQuote(body)
	got := attachmentURLs(stripped)
	if len(got) != 1 || got[0] != "aesgcm://host/photo.jpg#key" {
		t.Fatalf("attachmentURLs(stripReplyQuote(%q)) = %v, want single aesgcm URL", body, got)
	}
}

func TestAttachmentURLsTrailingLinesWithTextPrefix(t *testing.T) {
	// A message body may carry free text before one or more trailing URL
	// lines (e.g. "here you go\n<url1>\n<url2>"); attachmentURLs picks up
	// only the trailing URL run.
	body := "here you go\nhttps://host/a.jpg\nhttps://host/b.png"
	got := attachmentURLs(body)
	want := []string{"https://host/a.jpg", "https://host/b.png"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("attachmentURLs(%q) = %v, want %v", body, got, want)
	}
}
