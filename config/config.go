package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"codeberg.org/jim-ww/kage/ui"
)

type fileConfig struct {
	Keybinds map[string]any `toml:"keybinds"`
	Theme    ui.Theme       `toml:"theme"`
}

type UIConfig struct {
	KeyMap ui.KeyMap
	Theme  ui.Theme
}

func Load() (UIConfig, error) {
	cfgOut := UIConfig{
		KeyMap: ui.DefaultKeyMap,
		Theme:  ui.DefaultTheme(),
	}
	for _, path := range candidatePaths() {
		cfg, err := loadFile(path)
		if err != nil {
			return cfgOut, err
		}
		if cfg != nil {
			keys, err := applyKeybinds(ui.DefaultKeyMap, cfg.Keybinds)
			if err != nil {
				return cfgOut, err
			}
			cfgOut.KeyMap = keys
			cfgOut.Theme = mergeTheme(ui.DefaultTheme(), cfg.Theme)
			return cfgOut, nil
		}
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
		case "confirm_yes":
			keys.ConfirmYes = ui.NewBinding(override, "yes")
		case "confirm_no":
			keys.ConfirmNo = ui.NewBinding(override, "no")
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
