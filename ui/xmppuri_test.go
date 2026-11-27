package ui

import "testing"

func TestParseAddress(t *testing.T) {
	for in, want := range map[string]xmppURI{
		"room@conference.example.org":                          {addr: "room@conference.example.org"},
		"  xmpp:room@conference.example.org?join ":             {addr: "room@conference.example.org", action: "join"},
		"xmpp:room@conference.example.org?join;password=s%3Bp": {addr: "room@conference.example.org", action: "join", params: map[string]string{"password": "s;p"}},
		"XMPP:bob@example.org":                                 {addr: "bob@example.org"},
		"xmpp:bob@example.org?message;body=hi":                 {addr: "bob@example.org", action: "message", params: map[string]string{"body": "hi"}},
		"xmpp://me@example.org/bob@example.org?roster":         {addr: "bob@example.org", action: "roster"},
		"xmpp:caf%C3%A9@example.org":                           {addr: "café@example.org"},
	} {
		got := parseAddress(in)
		if got.addr != want.addr || got.action != want.action || len(got.params) != len(want.params) {
			t.Errorf("parseAddress(%q) = %+v, want %+v", in, got, want)
			continue
		}
		for k, v := range want.params {
			if got.params[k] != v {
				t.Errorf("parseAddress(%q) param %s = %q, want %q", in, k, got.params[k], v)
			}
		}
	}
}
