package main

import (
	"testing"

	"github.com/jim-ww/kage/config"
)

// TestReceiptsAllowed checks delivery receipts go only to whoever may see
// our presence anyway (XEP-0184 §8) - and our own account - once the
// roster's known, and to anyone before.
func TestReceiptsAllowed(t *testing.T) {
	s := &accountSession{account: config.Account{JID: "me@example.com"}}
	if !s.receiptsAllowed("stranger@example.com") {
		t.Error("refused before the roster's loaded, want answered as before")
	}
	s.roster.Store(&map[string]rosterEntry{
		"both@example.com": {Subs: "both"},
		"from@example.com": {Subs: "from"},
		"to@example.com":   {Subs: "to"},
		"none@example.com": {Subs: "none"},
	})
	for jid, want := range map[string]bool{
		"me@example.com":       true,
		"both@example.com":     true,
		"from@example.com":     true,
		"to@example.com":       false,
		"none@example.com":     false,
		"stranger@example.com": false,
	} {
		if got := s.receiptsAllowed(jid); got != want {
			t.Errorf("receiptsAllowed(%s) = %v, want %v", jid, got, want)
		}
	}
}
