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
