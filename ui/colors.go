package ui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// Presence colors are fixed semantics, independent of the theme — "online"
// should read as green and "do not disturb" as red in every theme.
var (
	presenceOnlineStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))  // green
	presenceChatStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))  // bright cyan
	presenceAwayStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("136")) // amber/brown
	presenceXAStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("208")) // orange
	presenceDNDStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))   // red
	presenceOfflineStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))   // gray
)

// presenceGlyphs maps each Presence to its (style, symbol) pair, shared by
// presenceGlyph/presenceGlyphOn so the two never drift out of sync.
var presenceGlyphs = map[Presence]struct {
	style  lipgloss.Style
	symbol string
}{
	PresenceOnline:  {presenceOnlineStyle, "●"},
	PresenceChat:    {presenceChatStyle, "●"},
	PresenceAway:    {presenceAwayStyle, "◐"},
	PresenceXA:      {presenceXAStyle, "◔"},
	PresenceDND:     {presenceDNDStyle, "⊘"},
	PresenceOffline: {presenceOfflineStyle, "○"},
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

type uiColors struct {
	// appBg       color.Color
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
	// noticeBg    color.Color
	noticeFg color.Color
}

func newUIColors(theme Theme) uiColors {
	return uiColors{
		// appBg:      lipgloss.Color(theme.AppBg),
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
		// noticeBg:    lipgloss.Color(theme.NoticeBg),
		noticeFg: lipgloss.Color(theme.NoticeFg),
	}
}
