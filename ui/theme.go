package ui

type Theme struct {
	AppBg       string
	PanelBg     string
	PanelAltBg  string
	PanelEdge   string
	LogBg       string
	ThemFg      string
	TextMuted   string
	Time        string
	BorderD     string
	BorderA     string
	AccentCyan  string
	ReplyFg     string
	PopupBg     string
	PopupDanger string
	FilterMatch string
	NickMe      string
	NickThem    string
	StatusFg    string
	NoticeBg    string
	NoticeFg    string
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
