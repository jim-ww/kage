package main

import "testing"

func TestOmemoPayloadRoundTrip(t *testing.T) {
	body := "hello check these out"
	urls := []string{"https://host/a.jpg", "https://host/b.png"}
	pt := encodeOmemoPayload(body, urls)
	gotBody, gotURLs := decodeOmemoPayload(pt)
	if gotBody != body {
		t.Fatalf("decodeOmemoPayload body = %q, want %q", gotBody, body)
	}
	if len(gotURLs) != len(urls) || gotURLs[0] != urls[0] || gotURLs[1] != urls[1] {
		t.Fatalf("decodeOmemoPayload urls = %v, want %v", gotURLs, urls)
	}
}

func TestOmemoPayloadRoundTripNoAttachments(t *testing.T) {
	body := "just text, no files"
	pt := encodeOmemoPayload(body, nil)
	gotBody, gotURLs := decodeOmemoPayload(pt)
	if gotBody != body {
		t.Fatalf("decodeOmemoPayload body = %q, want %q", gotBody, body)
	}
	if len(gotURLs) != 0 {
		t.Fatalf("decodeOmemoPayload urls = %v, want none", gotURLs)
	}
}

// TestDecodeOmemoPayloadFallsBackOnPlainText verifies a plaintext OMEMO
// message from a real (non-Kage) client - which never wraps its plaintext in
// this envelope - is treated as body-only with no attachments, rather than
// erroring or misparsing.
func TestDecodeOmemoPayloadFallsBackOnPlainText(t *testing.T) {
	body := "hi there, not an envelope"
	gotBody, gotURLs := decodeOmemoPayload([]byte(body))
	if gotBody != body {
		t.Fatalf("decodeOmemoPayload body = %q, want %q", gotBody, body)
	}
	if gotURLs != nil {
		t.Fatalf("decodeOmemoPayload urls = %v, want nil", gotURLs)
	}
}
