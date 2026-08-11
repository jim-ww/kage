package ui

// Theme holds the color palette used throughout the UI.
type Theme struct {
	AppBg       string `yaml:"app_bg,omitempty"`
	PanelBg     string `yaml:"panel_bg,omitempty"`
	PanelAltBg  string `yaml:"panel_alt_bg,omitempty"`
	PanelEdge   string `yaml:"panel_edge,omitempty"`
	LogBg       string `yaml:"log_bg,omitempty"`
	ThemFg      string `yaml:"them_fg,omitempty"`
	TextMuted   string `yaml:"text_muted,omitempty"`
	Time        string `yaml:"time,omitempty"`
	BorderD     string `yaml:"border_d,omitempty"`
	BorderA     string `yaml:"border_a,omitempty"`
	AccentCyan  string `yaml:"accent_cyan,omitempty"`
	ReplyFg     string `yaml:"reply_fg,omitempty"`
	PopupBg     string `yaml:"popup_bg,omitempty"`
	PopupDanger string `yaml:"popup_danger,omitempty"`
	FilterMatch string `yaml:"filter_match,omitempty"`
	NickMe      string `yaml:"nick_me,omitempty"`
	NickThem    string `yaml:"nick_them,omitempty"`
	StatusFg    string `yaml:"status_fg,omitempty"`
	NoticeBg    string `yaml:"notice_bg,omitempty"`
	NoticeFg    string `yaml:"notice_fg,omitempty"`
}

// DefaultTheme returns the built-in Tokyo Night theme.
func DefaultTheme() Theme {
	return Theme{
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
