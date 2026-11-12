package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

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

func TestTintFollowsTheBackground(t *testing.T) {
	dark := TerminalTheme()
	dark.AppBg = "#101010"
	if got := tint(dark, 0.5); !isDarkHex(got) || got == dark.AppBg {
		t.Fatalf("tint on a dark background = %q, want a slightly lighter dark shade", got)
	}
	r, _, _, _ := parseHexRGB(tint(dark, 0.5))
	if r <= 0x10 {
		t.Fatalf("tint on a dark background = %q, want lighter than it", tint(dark, 0.5))
	}

	light := TerminalTheme()
	light.AppBg = "#f0f0f0"
	if got := tint(light, 0.5); isDarkHex(got) || got >= light.AppBg {
		t.Fatalf("tint on a light background = %q, want a slightly darker light shade", got)
	}

	if got := tint(TerminalTheme(), 0.5); got != "" {
		t.Fatalf("tint without a known background = %q, want none", got)
	}
	if got, want := tint(TokyoNightTheme(), 0.22), blendHex("#1a1b26", "#3b4261", 0.22); got != want {
		t.Fatalf("Tokyo Night tint = %q, want the blend it always was, %q", got, want)
	}
}

// TestTerminalThemeNeverStacksPaletteColors: with the terminal's colors, a
// palette text color on a palette background is whatever pair the
// terminal's theme has in those slots - bright gray under neon green, say.
// Accent blocks are reverse video instead (the text is then the terminal's
// own background), and without a known background nothing gets a block.
func TestTerminalThemeNeverStacksPaletteColors(t *testing.T) {
	s := newUIStyles(TerminalTheme()) // AppBg unknown
	for name, out := range map[string]string{
		"send button":    s.sendButton.Render("Send"),
		"menu highlight": s.contextMenuItemHover.Render("Copy"),
		"attach button":  s.attachButton.Render("+"),
		"dim button":     s.dimButton.Render("▾"),
		"account button": s.renderAccountMenuButton(true, lipgloss.NewStyle().Background(s.colors.panelEdge), false),
	} {
		if strings.Contains(out, "[4") || strings.Contains(out, ";4") || strings.Contains(out, "48;") || strings.Contains(out, "[10") {
			t.Errorf("%s has a background color: %q", name, out)
		}
	}
	if out := s.sendButton.Render("Send"); !strings.Contains(out, "7;") && !strings.Contains(out, ";7") {
		t.Errorf("send button isn't reverse video: %q", out)
	}
}
