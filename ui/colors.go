package ui

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
)

// Presence colors are fixed semantics, independent of the theme — "online"
// should read as green and "do not disturb" as red in every theme.
var (
	presenceOnlineStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))  // green
	presenceChatStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))  // bright cyan
	presenceAwayStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("136")) // amber/brown
	presenceXAStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("208")) // orange
	presenceDNDStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))   // red
	presenceOfflineStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))   // gray
	presenceInvisibleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))  // magenta — distinct from offline's gray, since this is our own status, not how contacts see us
)

// presenceGlyphs maps each Presence to its (style, symbol) pair, shared by
// presenceGlyph/presenceGlyphOn so the two never drift out of sync.
var presenceGlyphs = map[Presence]struct {
	style  lipgloss.Style
	symbol string
}{
	PresenceOnline:    {presenceOnlineStyle, "●"},
	PresenceChat:      {presenceChatStyle, "●"},
	PresenceAway:      {presenceAwayStyle, "◐"},
	PresenceXA:        {presenceXAStyle, "◔"},
	PresenceDND:       {presenceDNDStyle, "⊘"},
	PresenceOffline:   {presenceOfflineStyle, "○"},
	PresenceInvisible: {presenceInvisibleStyle, "◌"},
}

// presenceGlyph renders a presence as a single colored symbol.
func presenceGlyph(p Presence) string {
	g, ok := presenceGlyphs[p]
	if !ok {
		g = presenceGlyphs[PresenceOffline]
	}
	return g.style.Render(g.symbol)
}

// presenceLabel is a Presence's plain-text name, for notifications/menus.
func presenceLabel(p Presence) string {
	switch p {
	case PresenceOnline:
		return "online"
	case PresenceChat:
		return "free to chat"
	case PresenceAway:
		return "away"
	case PresenceXA:
		return "extended away"
	case PresenceDND:
		return "do not disturb"
	case PresenceInvisible:
		return "invisible"
	default:
		return "offline"
	}
}

// presenceGlyphOn is presenceGlyph rendered against bg — used when the
// glyph is going to sit inside a row that itself has a background (e.g. a
// hover highlight), so the dot's own style needs to carry that same
// background rather than leaving a gap where the row highlight would
// otherwise show through unstyled.
func presenceGlyphOn(p Presence, bg color.Color) string {
	g, ok := presenceGlyphs[p]
	if !ok {
		g = presenceGlyphs[PresenceOffline]
	}
	return g.style.Background(bg).Render(g.symbol)
}

// deviceGlyph renders a resource's presence as a colored device icon,
// mirroring presenceGlyph but with a square "device" symbol instead of the
// round per-chat presence dot — visually distinct so the device-list popup
// doesn't read as another presence-dot row.
func deviceGlyph(p Presence) string {
	g, ok := presenceGlyphs[p]
	if !ok {
		g = presenceGlyphs[PresenceOffline]
	}
	return g.style.Render("■")
}

type uiColors struct {
	appBg color.Color
	// panelBg     color.Color
	// panelAltBg  color.Color
	panelEdge color.Color
	// logBg       color.Color
	themFg     color.Color
	textMuted  color.Color
	time       color.Color
	borderD    color.Color
	borderA    color.Color
	accentCyan color.Color
	replyFg    color.Color
	// popupBg     color.Color
	popupDanger color.Color
	filterMatch color.Color
	nickMe      color.Color
	nickThem    color.Color
	statusFg    color.Color
	noticeBg    color.Color
	noticeFg    color.Color
	rowHoverBg  color.Color
	dimButtonBg color.Color
}

func newUIColors(theme Theme) uiColors {
	return uiColors{
		appBg: lipgloss.Color(theme.AppBg),
		// panelBg:    lipgloss.Color(theme.PanelBg),
		// panelAltBg: lipgloss.Color(theme.PanelAltBg),
		panelEdge: lipgloss.Color(theme.PanelEdge),
		// logBg:      lipgloss.Color(theme.LogBg),
		themFg:     lipgloss.Color(theme.ThemFg),
		textMuted:  lipgloss.Color(theme.TextMuted),
		time:       lipgloss.Color(theme.Time),
		borderD:    lipgloss.Color(theme.BorderD),
		borderA:    lipgloss.Color(theme.BorderA),
		accentCyan: lipgloss.Color(theme.AccentCyan),
		replyFg:    lipgloss.Color(theme.ReplyFg),
		// popupBg:     lipgloss.Color(theme.PopupBg),
		popupDanger: lipgloss.Color(theme.PopupDanger),
		filterMatch: lipgloss.Color(theme.FilterMatch),
		nickMe:      lipgloss.Color(theme.NickMe),
		nickThem:    lipgloss.Color(theme.NickThem),
		statusFg:    lipgloss.Color(theme.StatusFg),
		noticeBg:    lipgloss.Color(theme.NoticeBg),
		noticeFg:    lipgloss.Color(theme.NoticeFg),
		// rowHoverBg is a hover highlight barely a shade off the app
		// background - blending most of the way toward it rather than using
		// borderD outright, which reads as a jarringly bright box around the
		// row instead of a subtle "this one's highlighted" cue.
		rowHoverBg: lipgloss.Color(blendHex(theme.AppBg, theme.BorderD, 0.22)),
		// dimButtonBg is a subtler button background than attachButton's
		// borderD - just enough to read as "this is a button" without
		// competing with the message text around it.
		dimButtonBg: lipgloss.Color(blendHex(theme.AppBg, theme.BorderD, 0.45)),
	}
}

// blendHex linearly interpolates between two "#rrggbb" hex colors, t=0
// returning a and t=1 returning b. Falls back to a on any parse failure
// (e.g. a theme override that isn't valid hex) rather than panicking.
func blendHex(a, b string, t float64) string {
	ar, ag, ab, ok1 := parseHexRGB(a)
	br, bg, bb, ok2 := parseHexRGB(b)
	if !ok1 || !ok2 {
		return a
	}
	lerp := func(x, y uint8) uint8 {
		return uint8(float64(x) + t*(float64(y)-float64(x)))
	}
	return fmt.Sprintf("#%02x%02x%02x", lerp(ar, br), lerp(ag, bg), lerp(ab, bb))
}

func parseHexRGB(hex string) (r, g, b uint8, ok bool) {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return uint8(v >> 16), uint8(v >> 8), uint8(v), true
}
