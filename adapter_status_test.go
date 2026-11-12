package main

import (
	"testing"

	"github.com/jim-ww/kage/ui"
)

// TestStatusConfigValueRoundTrip verifies presenceNames (persisted
// config.toml value) is the exact inverse of accountStatus (config value ->
// ui.Presence) for every Presence a user can pick via the status menu -
// a mismatch here means a chosen status doesn't survive a daemon restart.
func TestStatusConfigValueRoundTrip(t *testing.T) {
	statuses := []ui.Presence{
		ui.PresenceOnline, ui.PresenceChat, ui.PresenceAway, ui.PresenceXA,
		ui.PresenceDND, ui.PresenceInvisible, ui.PresenceOffline,
	}
	for _, status := range statuses {
		configValue := presenceNames[status]
		if got := accountStatus(configValue); got != status {
			t.Errorf("presenceNames[%v] = %q, but accountStatus(%q) = %v, want %v", status, configValue, configValue, got, status)
		}
	}
}

func TestStatusConfigValueInvisible(t *testing.T) {
	if got, want := presenceNames[ui.PresenceInvisible], "invisible"; got != want {
		t.Errorf("presenceNames[PresenceInvisible] = %q, want %q", got, want)
	}
}
