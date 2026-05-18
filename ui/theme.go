package ui

type Theme struct {
	AppBg       string `toml:"app_bg"`
	PanelBg     string `toml:"panel_bg"`
	PanelAltBg  string `toml:"panel_alt_bg"`
	PanelEdge   string `toml:"panel_edge"`
	LogBg       string `toml:"log_bg"`
	ThemFg      string `toml:"them_fg"`
	TextMuted   string `toml:"text_muted"`
	Time        string `toml:"time"`
	BorderD     string `toml:"border_d"`
	BorderA     string `toml:"border_a"`
	AccentCyan  string `toml:"accent_cyan"`
	ReplyFg     string `toml:"reply_fg"`
	PopupBg     string `toml:"popup_bg"`
	PopupDanger string `toml:"popup_danger"`
	FilterMatch string `toml:"filter_match"`
	NickMe      string `toml:"nick_me"`
	NickThem    string `toml:"nick_them"`
	StatusFg    string `toml:"status_fg"`
	NoticeBg    string `toml:"notice_bg"`
	NoticeFg    string `toml:"notice_fg"`
}

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
