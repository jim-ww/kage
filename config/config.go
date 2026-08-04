package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/jim-ww/kage/ui"
)

type fileConfig struct {
	Keybinds        map[string]any `toml:"keybinds"`
	Theme           ui.Theme       `toml:"theme"`
	Mouse           *bool          `toml:"mouse"`                       // nil (unset) means the default: on
	SidebarWidth    int            `toml:"sidebar_width,omitempty"`     // persisted from dragging the sidebar border; 0 (unset) means the width/4-based default
	InputHeight     int            `toml:"input_height,omitempty"`      // persisted from dragging the compose box border; 0 (unset) means the DynamicHeight-based default
	SidebarHidden   bool           `toml:"sidebar_hidden,omitempty"`    // persisted from toggling the chat list (Ctrl+\ / status-bar button); unset means open
	Icons           *bool          `toml:"icons"`                       // nil (unset) means the default: on; show icons for attachments/encryption instead of plain-text tags
	ShowNames       bool           `toml:"show_names,omitempty"`        // show the sender's name in the message header instead of just a direction glyph; off by default
	TimeLayout      string         `toml:"time_layout,omitempty"`       // custom Go time layout for message timestamps; unset means the default ("15:04"/"2006-01-02 15:04")
	TimeOnlyToday   *bool          `toml:"time_only_today"`             // nil (unset) means the default: on; with the default time layout, show time-only for messages sent today instead of a full date
	DefaultAccount  string         `toml:"default_account"`             // JID selected on startup; unset means the first configured account
	OpenLastChat    *bool          `toml:"open_last_chat"`              // nil (unset) means the default: on; whether to reopen LastChatAddress on startup
	LastChatAccount string         `toml:"last_chat_account,omitempty"` // JID of the account owning the last opened chat
	LastChatAddress string         `toml:"last_chat_address,omitempty"` // peer JID of the last opened chat, reopened on startup if OpenLastChat is set
	Notifications   *bool          `toml:"notifications"`               // nil (unset) means the default: on; whether to spawn the desktop notification daemon
	HistoryPageSize int            `toml:"history_page_size,omitempty"` // number of messages loaded per chat at a time (initial load + each "load older"); 0 (unset) means the default
	Storage         StorageConfig  `toml:"storage"`
	Accounts        []Account      `toml:"accounts"`
}

// StorageConfig configures the password local message history is encrypted
// under at rest (see ResolveStoragePassword) — one password for the whole
// database, shared by every configured account.
type StorageConfig struct {
	Password    string `toml:"password,omitempty"`     // plaintext fallback
	PasswordCmd string `toml:"password_cmd,omitempty"` // shell command printing the password on stdout
}

type UIConfig struct {
	KeyMap        ui.KeyMap
	Theme         ui.Theme
	Mouse         bool   // enables mouse click/scroll support; on by default
	SidebarWidth  int    // 0 means "use the width/4-based default"
	InputHeight   int    // 0 means "use the DynamicHeight-based default"
	SidebarHidden bool   // persisted chat list visibility; false (open) by default
	OpenLastChat  bool   // whether to reopen the last chat on startup; on by default
	Icons         bool   // show icons for attachments/encryption instead of plain-text tags; on by default
	ShowNames     bool   // show the sender's name in the message header instead of just a direction glyph; off by default
	TimeLayout    string // custom Go time layout for message timestamps; empty means the default
	TimeOnlyToday bool   // with the default time layout, show time-only for messages sent today instead of a full date; on by default
}

// Config is the fully resolved application configuration.
type Config struct {
	UI       UIConfig
	Storage  StorageConfig
	Accounts []Account
	// Notifications controls whether the desktop notification daemon is
	// spawned on startup. On by default.
	Notifications bool
	// DefaultAccountIdx is the index into Accounts selected on startup,
	// resolved from the default_account JID setting. 0 (the first account)
	// when unset or when the configured JID doesn't match any account.
	DefaultAccountIdx int
	// LastChatAccountIdx/LastChatAddress identify the chat to reopen on
	// startup when UI.OpenLastChat is set. LastChatAddress is empty when no
	// chat has been opened yet. LastChatAccountIdx is only meaningful
	// alongside a non-empty LastChatAddress.
	LastChatAccountIdx int
	LastChatAddress    string
	// HistoryPageSize is the number of messages loaded per chat at a time,
	// both on startup and for each "load older messages" fetch as the user
	// scrolls up — keeps very long histories from being decrypted/rendered
	// all at once. DefaultHistoryPageSize when unset or non-positive.
	HistoryPageSize int
	// Path is the config file this was actually loaded from, or the
	// default write location if none was found — always non-empty, so
	// callers that need to persist a change (e.g. an auto-detected GPG key)
	// have somewhere to write it back to.
	Path string
}

// DefaultHistoryPageSize is how many messages are loaded per chat at a time
// when history_page_size isn't set in config.toml.
const DefaultHistoryPageSize = 200

func Load(path string) (Config, error) {
	cfgOut := Config{
		UI: UIConfig{
			KeyMap:        ui.DefaultKeyMap,
			Theme:         ui.DefaultTheme(),
			Mouse:         true,
			OpenLastChat:  true,
			TimeOnlyToday: true,
			Icons:         true,
		},
		Notifications:   true,
		HistoryPageSize: DefaultHistoryPageSize,
	}
	paths := append([]string{path}, candidatePaths()...)
	for _, path := range paths {
		cfg, err := loadFile(path)
		if err != nil {
			return cfgOut, err
		}
		if cfg != nil {
			keys, err := applyKeybinds(ui.DefaultKeyMap, cfg.Keybinds)
			if err != nil {
				return cfgOut, err
			}
			cfgOut.UI.KeyMap = keys
			cfgOut.UI.Theme = mergeTheme(ui.DefaultTheme(), cfg.Theme)
			if cfg.Mouse != nil {
				cfgOut.UI.Mouse = *cfg.Mouse
			}
			cfgOut.UI.SidebarWidth = cfg.SidebarWidth
			cfgOut.UI.SidebarHidden = cfg.SidebarHidden
			if cfg.Icons != nil {
				cfgOut.UI.Icons = *cfg.Icons
			}
			cfgOut.UI.ShowNames = cfg.ShowNames
			cfgOut.UI.TimeLayout = cfg.TimeLayout
			cfgOut.UI.InputHeight = cfg.InputHeight
			if cfg.OpenLastChat != nil {
				cfgOut.UI.OpenLastChat = *cfg.OpenLastChat
			}
			if cfg.TimeOnlyToday != nil {
				cfgOut.UI.TimeOnlyToday = *cfg.TimeOnlyToday
			}
			if cfg.Notifications != nil {
				cfgOut.Notifications = *cfg.Notifications
			}
			if cfg.HistoryPageSize > 0 {
				cfgOut.HistoryPageSize = cfg.HistoryPageSize
			}
			cfgOut.Storage = cfg.Storage
			cfgOut.Accounts = cfg.Accounts
			cfgOut.Path = path
			if cfg.DefaultAccount != "" {
				for i, acct := range cfg.Accounts {
					if acct.JID == cfg.DefaultAccount {
						cfgOut.DefaultAccountIdx = i
						break
					}
				}
			}
			if cfg.LastChatAddress != "" {
				cfgOut.LastChatAddress = cfg.LastChatAddress
				for i, acct := range cfg.Accounts {
					if acct.JID == cfg.LastChatAccount {
						cfgOut.LastChatAccountIdx = i
						break
					}
				}
			}
			return cfgOut, nil
		}
	}
	if defaultPath, err := DefaultWritePath(); err == nil {
		cfgOut.Path = defaultPath
	}
	return cfgOut, nil
}

func candidatePaths() []string {
	paths := make([]string, 0, 3)
	if env := strings.TrimSpace(os.Getenv("KAGE_CONFIG")); env != "" {
		paths = append(paths, env)
	}
	paths = append(paths, "config.toml")
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append(paths, filepath.Join(home, ".config", "kage", "config.toml"))
	}
	return paths
}

func loadFile(path string) (*fileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}

	var cfg fileConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}
	return &cfg, nil
}

func applyKeybinds(keys ui.KeyMap, binds map[string]any) (ui.KeyMap, error) {
	for name, raw := range binds {
		override, err := normalizeKeys(raw)
		if err != nil {
			return ui.DefaultKeyMap, fmt.Errorf("invalid keybind %q: %w", name, err)
		}
		if len(override) == 0 {
			continue
		}

		switch name {
		case "quit":
			keys.Quit = ui.NewBinding(override, "quit")
		case "back":
			keys.Back = ui.NewBinding(override, "back to chats")
		case "switch":
			keys.Switch = ui.NewBinding(override, "switch focus")
		case "focus_chats":
			keys.FocusChats = ui.NewBinding(override, "focus chats")
		case "chat_open":
			keys.ChatOpen = ui.NewBinding(override, "open chat")
		case "select_send":
			keys.SelectSend = ui.NewBinding(override, "select/send")
		case "message_up":
			keys.MsgUp = ui.NewBinding(override, "prev msg")
		case "message_down":
			keys.MsgDown = ui.NewBinding(override, "next msg")
		case "delete":
			keys.DeleteMsg = ui.NewBinding(override, "delete")
		case "yank":
			keys.YankMsg = ui.NewBinding(override, "yank")
		case "edit":
			keys.EditMsg = ui.NewBinding(override, "edit (own last)")
		case "reply":
			keys.ReplyMsg = ui.NewBinding(override, "reply")
		case "info":
			keys.InfoMsg = ui.NewBinding(override, "message info")
		case "open":
			keys.OpenMsg = ui.NewBinding(override, "open links/attachments")
		case "save":
			keys.SaveMsg = ui.NewBinding(override, "save links/attachments")
		case "attach_file":
			keys.AttachFile = ui.NewBinding(override, "attach file")
		case "react":
			keys.ReactMsg = ui.NewBinding(override, "react")
		case "confirm_yes":
			keys.ConfirmYes = ui.NewBinding(override, "yes")
		case "confirm_no":
			keys.ConfirmNo = ui.NewBinding(override, "no")
		case "add_account":
			keys.AddAccount = ui.NewBinding(override, "add account")
		}
	}
	return keys, nil
}

func normalizeKeys(raw any) ([]string, error) {
	switch v := raw.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("binding cannot be empty")
		}
		return []string{v}, nil
	case []any:
		keys := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("expected string array")
			}
			if strings.TrimSpace(s) == "" {
				return nil, fmt.Errorf("binding cannot be empty")
			}
			keys = append(keys, s)
		}
		return keys, nil
	default:
		return nil, fmt.Errorf("expected string or string array")
	}
}

func mergeTheme(base, override ui.Theme) ui.Theme {
	if override.AppBg != "" {
		base.AppBg = override.AppBg
	}
	if override.PanelBg != "" {
		base.PanelBg = override.PanelBg
	}
	if override.PanelAltBg != "" {
		base.PanelAltBg = override.PanelAltBg
	}
	if override.PanelEdge != "" {
		base.PanelEdge = override.PanelEdge
	}
	if override.LogBg != "" {
		base.LogBg = override.LogBg
	}
	if override.ThemFg != "" {
		base.ThemFg = override.ThemFg
	}
	if override.TextMuted != "" {
		base.TextMuted = override.TextMuted
	}
	if override.Time != "" {
		base.Time = override.Time
	}
	if override.BorderD != "" {
		base.BorderD = override.BorderD
	}
	if override.BorderA != "" {
		base.BorderA = override.BorderA
	}
	if override.AccentCyan != "" {
		base.AccentCyan = override.AccentCyan
	}
	if override.ReplyFg != "" {
		base.ReplyFg = override.ReplyFg
	}
	if override.PopupBg != "" {
		base.PopupBg = override.PopupBg
	}
	if override.PopupDanger != "" {
		base.PopupDanger = override.PopupDanger
	}
	if override.FilterMatch != "" {
		base.FilterMatch = override.FilterMatch
	}
	if override.NickMe != "" {
		base.NickMe = override.NickMe
	}
	if override.NickThem != "" {
		base.NickThem = override.NickThem
	}
	if override.StatusFg != "" {
		base.StatusFg = override.StatusFg
	}
	if override.NoticeBg != "" {
		base.NoticeBg = override.NoticeBg
	}
	if override.NoticeFg != "" {
		base.NoticeFg = override.NoticeFg
	}
	return base
}
