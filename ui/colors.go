package ui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// Presence colors are fixed traffic-light semantics (green/amber/gray),
// independent of the theme — "online" should read as green in every theme.
var (
	presenceOnlineStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))  // green
	presenceAwayStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("136")) // amber/brown
	presenceOfflineStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))   // gray
)

// presenceGlyph renders a contact's presence as a single colored symbol:
// ● online, ◐ away, ○ offline/unknown.
func presenceGlyph(p Presence) string {
	switch p {
	case PresenceOnline:
		return presenceOnlineStyle.Render("●")
	case PresenceAway:
		return presenceAwayStyle.Render("◐")
	default:
		return presenceOfflineStyle.Render("○")
	}
}

// presenceGlyphOn is presenceGlyph rendered against bg — used when the
// glyph is going to sit inside a row that itself has a background (e.g. a
// hover highlight), so the dot's own style needs to carry that same
// background rather than leaving a gap where the row highlight would
// otherwise show through unstyled.
func presenceGlyphOn(p Presence, bg color.Color) string {
	switch p {
	case PresenceOnline:
		return presenceOnlineStyle.Background(bg).Render("●")
	case PresenceAway:
		return presenceAwayStyle.Background(bg).Render("◐")
	default:
		return presenceOfflineStyle.Background(bg).Render("○")
	}
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
