package main

import (
	"testing"
)

func TestStaleRosterEntries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cached map[string]rosterEntry
		live   map[string]string
		want   []string
	}{
		{
			name:   "contact the server no longer lists is stale",
			cached: map[string]rosterEntry{"gone@x": {Subs: "both"}, "kept@x": {Subs: "both"}},
			live:   map[string]string{"kept@x": ""},
			want:   []string{"gone@x"},
		},
		{
			// ensureChat's local-only rows for a stranger who messaged us were
			// never in the server's roster, so its silence says nothing.
			name:   "local-only chat row is never stale",
			cached: map[string]rosterEntry{"stranger@x": {}, "kept@x": {Subs: "both"}},
			live:   map[string]string{"kept@x": ""},
			want:   nil,
		},
		{
			// The guard that matters: an empty fetch must not be read as
			// licence to delete the entire roster.
			name:   "empty fetch removes nothing",
			cached: map[string]rosterEntry{"a@x": {Subs: "both"}, "b@x": {Subs: "to"}},
			live:   map[string]string{},
			want:   nil,
		},
		{
			name:   "nothing cached, nothing stale",
			cached: map[string]rosterEntry{},
			live:   map[string]string{"a@x": ""},
			want:   nil,
		},
		{
			name:   "several stale entries come back sorted",
			cached: map[string]rosterEntry{"c@x": {Subs: "from"}, "a@x": {Subs: "both"}, "b@x": {Subs: "to"}},
			live:   map[string]string{"keep@x": ""},
			want:   []string{"a@x", "b@x", "c@x"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := staleRosterEntries(tc.cached, tc.live)
			if len(got) != len(tc.want) {
				t.Fatalf("stale = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("stale = %v, want %v", got, tc.want)
				}
			}
		})
	}
}
