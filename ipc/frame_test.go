package ipc

import (
	"bytes"
	"strings"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		tag  tag
		v    any
	}{
		{"request", tagRequest, Request{ID: 1, Method: "Send", Params: []byte(`{"to":"a@b"}`)}},
		{"response", tagResponse, Response{ID: 1, Result: []byte(`{"id":"msg1"}`)}},
		{"event", tagEvent, Event{Kind: "IncomingMessage", Data: []byte(`{"body":"hi"}`)}},
		{"empty params", tagRequest, Request{ID: 2, Method: "ListAccounts"}},
		{"large payload", tagEvent, Event{Kind: "Big", Data: []byte(`"` + strings.Repeat("x", 1<<20) + `"`)}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := writeFrame(&buf, tc.tag, tc.v); err != nil {
				t.Fatalf("writeFrame: %v", err)
			}
			gotTag, body, err := readFrame(&buf)
			if err != nil {
				t.Fatalf("readFrame: %v", err)
			}
			if gotTag != tc.tag {
				t.Fatalf("tag = %v, want %v", gotTag, tc.tag)
			}
			if len(body) == 0 {
				t.Fatalf("empty body")
			}
		})
	}
}

func TestFrameMultipleInOneStream(t *testing.T) {
	var buf bytes.Buffer
	if err := writeFrame(&buf, tagRequest, Request{ID: 1, Method: "A"}); err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(&buf, tagResponse, Response{ID: 1}); err != nil {
		t.Fatal(err)
	}

	tag1, _, err := readFrame(&buf)
	if err != nil || tag1 != tagRequest {
		t.Fatalf("first frame: tag=%v err=%v", tag1, err)
	}
	tag2, _, err := readFrame(&buf)
	if err != nil || tag2 != tagResponse {
		t.Fatalf("second frame: tag=%v err=%v", tag2, err)
	}
}

func TestReadFrameRejectsOversized(t *testing.T) {
	var buf bytes.Buffer
	header := []byte{byte(tagRequest), 0xFF, 0xFF, 0xFF, 0xFF}
	buf.Write(header)
	if _, _, err := readFrame(&buf); err == nil {
		t.Fatal("expected error for oversized frame length")
	}
}
