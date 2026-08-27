package main

import (
	"testing"

	"github.com/jim-ww/kage/ui"
)

func TestAccountStatusRoundTrip(t *testing.T) {
	cases := []struct {
		config string
		want   ui.Presence
	}{
		{"", ui.PresenceOnline},
		{"chat", ui.PresenceChat},
		{"away", ui.PresenceAway},
		{"xa", ui.PresenceXA},
		{"dnd", ui.PresenceDND},
		{"offline", ui.PresenceOffline},
		{"invisible", ui.PresenceInvisible},
	}
	for _, c := range cases {
		if got := accountStatus(c.config); got != c.want {
			t.Errorf("accountStatus(%q) = %v, want %v", c.config, got, c.want)
		}
	}
}

// TestPresenceShowInvisibleIsEmpty documents that presenceShow deliberately
// has no case for PresenceInvisible: unlike the other statuses, invisible
// isn't sent as a <show/> child at all (see xmpp.Client.SetInvisible, which
// uses a distinct presence type) - SetAccountStatus must never feed this
// value straight to xmpp.Client.SetPresence for PresenceInvisible.
func TestPresenceShowInvisibleIsEmpty(t *testing.T) {
	if got := presenceShow(ui.PresenceInvisible); got != "" {
		t.Errorf("presenceShow(PresenceInvisible) = %q, want empty (invisible is sent via SetInvisible, not a <show/> value)", got)
	}
}
