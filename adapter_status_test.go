package main

import (
	"testing"

	"github.com/jim-ww/kage/ui"
)

// TestStatusConfigValueRoundTrip verifies statusConfigValue (persisted
// config.yaml value) is the exact inverse of accountStatus (config value ->
// ui.Presence) for every Presence a user can pick via the status menu -
// a mismatch here means a chosen status doesn't survive a daemon restart.
func TestStatusConfigValueRoundTrip(t *testing.T) {
	statuses := []ui.Presence{
		ui.PresenceOnline, ui.PresenceChat, ui.PresenceAway, ui.PresenceXA,
		ui.PresenceDND, ui.PresenceInvisible, ui.PresenceOffline,
	}
	for _, status := range statuses {
		configValue := statusConfigValue(status)
		if got := accountStatus(configValue); got != status {
			t.Errorf("statusConfigValue(%v) = %q, but accountStatus(%q) = %v, want %v", status, configValue, configValue, got, status)
		}
	}
}

func TestStatusConfigValueInvisible(t *testing.T) {
	if got, want := statusConfigValue(ui.PresenceInvisible), "invisible"; got != want {
		t.Errorf("statusConfigValue(PresenceInvisible) = %q, want %q", got, want)
	}
}
