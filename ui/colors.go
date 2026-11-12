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

// pinnedChatGlyph prefixes a pinned chat's row in the chat list (see
// Chat.Title).
const pinnedChatGlyph = "📌"

// mutedChatGlyph follows a muted chat's name in the chat list (see
// Chat.Title).
const mutedChatGlyph = "🔕"

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
	buttonBg    color.Color
	dimButtonBg color.Color
}

func newUIColors(theme Theme) uiColors {
	// appBg is drawn as text on accent-colored backgrounds (a highlighted
	// button, the calendar cursor), so it needs a real color: the terminal's
	// background, filled in by the caller when it could ask (see
	// TerminalTheme), else black as the likeliest.
	appBg := theme.AppBg
	if appBg == "" {
		appBg = "0"
	}
	noticeBg := theme.NoticeBg
	if noticeBg == "" {
		noticeBg = tint(theme, 0.5)
	}
	panelEdge := theme.PanelEdge
	if panelEdge == "" {
		panelEdge = tint(theme, 0.45)
	}
	return uiColors{
		appBg: lipgloss.Color(appBg),
		// panelBg:    lipgloss.Color(theme.PanelBg),
		// panelAltBg: lipgloss.Color(theme.PanelAltBg),
		panelEdge: lipgloss.Color(panelEdge),
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
		noticeBg:    lipgloss.Color(noticeBg),
		noticeFg:    lipgloss.Color(theme.NoticeFg),
		// rowHoverBg is a hover highlight barely a shade off the app
		// background - blending most of the way toward it rather than using
		// borderD outright, which reads as a jarringly bright box around the
		// row instead of a subtle "this one's highlighted" cue.
		rowHoverBg: lipgloss.Color(tint(theme, 0.22)),
		// dimButtonBg is a subtler button background than attachButton's
		// borderD - just enough to read as "this is a button" without
		// competing with the message text around it.
		dimButtonBg: lipgloss.Color(tint(theme, 0.45)),
		// buttonBg is a button's block: BorderD itself when that's a known
		// color, else the background a step off (see tint) - never a
		// palette color behind palette-colored text.
		buttonBg: lipgloss.Color(tint(theme, 1)),
	}
}

// tint is a background a shade off the app's own, t (0-1) of the way from
// it toward BorderD - or, when BorderD is a palette color whose real value
// isn't known, toward white on a dark background and black on a light one,
// by a similarly small step. Without a known background it's none at all:
// a palette color behind palette-colored text is whatever pair the
// terminal's theme happens to have in those slots, often unreadable.
func tint(theme Theme, t float64) string {
	if _, _, _, ok := parseHexRGB(theme.AppBg); !ok {
		return ""
	}
	if _, _, _, ok := parseHexRGB(theme.BorderD); ok {
		return blendHex(theme.AppBg, theme.BorderD, t)
	}
	toward := "#ffffff"
	if !isDarkHex(theme.AppBg) {
		toward = "#000000"
	}
	return blendHex(theme.AppBg, toward, t*0.2)
}

// isDarkHex reports whether the "#rrggbb" color hex is dark, by perceived
// luminance.
func isDarkHex(hex string) bool {
	r, g, b, _ := parseHexRGB(hex)
	return 0.299*float64(r)+0.587*float64(g)+0.114*float64(b) < 128
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
