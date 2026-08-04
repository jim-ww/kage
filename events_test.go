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

func TestAttachmentURLsStillFailsWithoutStripping(t *testing.T) {
	// Documents why stripReplyQuote must run before attachmentURLs: the raw
	// quoted body doesn't start with a recognized scheme, so without
	// stripping the file would silently stop being identified as an
	// attachment.
	body := "> Bob\n> photo.jpg\naesgcm://host/photo.jpg#key"
	if got := attachmentURLs(body); got != nil {
		t.Fatalf("attachmentURLs(%q) = %v, want nil (unstripped quote hides the URL)", body, got)
	}
}
