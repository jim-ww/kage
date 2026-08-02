package ui

import (
	"reflect"
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
