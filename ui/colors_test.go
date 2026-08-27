package ui

import "testing"

func TestPresenceLabelInvisible(t *testing.T) {
	if got, want := presenceLabel(PresenceInvisible), "invisible"; got != want {
		t.Errorf("presenceLabel(PresenceInvisible) = %q, want %q", got, want)
	}
}

// TestPresenceGlyphInvisibleHasOwnEntry guards against PresenceInvisible
// silently falling back to presenceGlyphs[PresenceOffline] (the map's
// documented behavior for any Presence missing its own entry) - since
// invisible is our own status, not how others see us, it should render
// distinctly from offline in the account status picker.
func TestPresenceGlyphInvisibleHasOwnEntry(t *testing.T) {
	if _, ok := presenceGlyphs[PresenceInvisible]; !ok {
		t.Fatal("presenceGlyphs has no entry for PresenceInvisible; it will silently render as offline")
	}
	if got, offline := presenceGlyph(PresenceInvisible), presenceGlyph(PresenceOffline); got == offline {
		t.Errorf("presenceGlyph(PresenceInvisible) = %q, same as offline %q, want distinct", got, offline)
	}
}
