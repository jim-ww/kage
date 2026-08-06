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
