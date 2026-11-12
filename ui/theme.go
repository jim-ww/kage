package ui

// Theme holds the color palette used throughout the UI. Each color is a
// "#rrggbb" hex value, a terminal palette number ("0"-"15" being the
// terminal's own 16 colors, which a terminal theme - Stylix, base16 -
// sets), or empty for the terminal's own foreground or background.
type Theme struct {
	// Preset is the theme the other fields override: "terminal" (the
	// default, the terminal's own colors) or "tokyonight".
	Preset string `toml:"preset,omitempty"`

	AppBg       string `toml:"app_bg,omitempty"`
	PanelBg     string `toml:"panel_bg,omitempty"`
	PanelAltBg  string `toml:"panel_alt_bg,omitempty"`
	PanelEdge   string `toml:"panel_edge,omitempty"`
	LogBg       string `toml:"log_bg,omitempty"`
	ThemFg      string `toml:"them_fg,omitempty"`
	TextMuted   string `toml:"text_muted,omitempty"`
	Time        string `toml:"time,omitempty"`
	BorderD     string `toml:"border_d,omitempty"`
	BorderA     string `toml:"border_a,omitempty"`
	AccentCyan  string `toml:"accent_cyan,omitempty"`
	ReplyFg     string `toml:"reply_fg,omitempty"`
	PopupBg     string `toml:"popup_bg,omitempty"`
	PopupDanger string `toml:"popup_danger,omitempty"`
	FilterMatch string `toml:"filter_match,omitempty"`
	NickMe      string `toml:"nick_me,omitempty"`
	NickThem    string `toml:"nick_them,omitempty"`
	StatusFg    string `toml:"status_fg,omitempty"`
	NoticeBg    string `toml:"notice_bg,omitempty"`
	NoticeFg    string `toml:"notice_fg,omitempty"`
}

// Theme presets, for Theme.Preset.
const (
	ThemeTerminal   = "terminal"
	ThemeTokyoNight = "tokyonight"
)

// ThemePreset returns the preset named name ("" is the default, terminal),
// or false for an unknown name.
func ThemePreset(name string) (Theme, bool) {
	switch name {
	case "", ThemeTerminal:
		return TerminalTheme(), true
	case ThemeTokyoNight:
		return TokyoNightTheme(), true
	}
	return Theme{}, false
}

// DefaultTheme is the theme used when the config picks none: the
// terminal's own colors.
func DefaultTheme() Theme { return TerminalTheme() }

// TerminalTheme takes every color from the terminal: its foreground and
// background, and its 16-color palette - so kage follows whatever theme the
// terminal has (Stylix, base16, a hand-picked one). The numbers are those
// at which Tokyo Night's terminal palette has the colors TokyoNightTheme
// uses, so a Tokyo Night terminal looks as it does with that preset. AppBg
// is left empty for the caller to fill with the terminal's real background
// (see newUIColors for what it's needed for), and PanelEdge and NoticeBg -
// shades just off that background - are worked out from it.
func TerminalTheme() Theme {
	return Theme{
		Preset:      ThemeTerminal,
		TextMuted:   "7",
		Time:        "8",
		BorderD:     "8",
		BorderA:     "1",
		AccentCyan:  "6",
		ReplyFg:     "6",
		PopupDanger: "1",
		FilterMatch: "3",
		NickMe:      "6",
		NickThem:    "5",
	}
}

// TokyoNightTheme is the Tokyo Night theme in fixed hex colors, whatever
// the terminal's own are.
func TokyoNightTheme() Theme {
	return Theme{
		Preset:      ThemeTokyoNight,
		AppBg:       "#1a1b26",
		PanelBg:     "#1f2335",
		PanelAltBg:  "#24283b",
		PanelEdge:   "#292e42",
		LogBg:       "#1b1f2f",
		ThemFg:      "#c0caf5",
		TextMuted:   "#a9b1d6",
		Time:        "#565f89",
		BorderD:     "#3b4261",
		BorderA:     "#f7768e",
		AccentCyan:  "#7dcfff",
		ReplyFg:     "#73daca",
		PopupBg:     "#1f2335",
		PopupDanger: "#f7768e",
		FilterMatch: "#e0af68",
		NickMe:      "#7dcfff",
		NickThem:    "#bb9af7",
		StatusFg:    "#9ece6a",
		NoticeBg:    "#292e42",
		NoticeFg:    "#c0caf5",
	}
}
