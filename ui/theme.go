package ui

type Theme struct {
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

// Tokyo Night
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
