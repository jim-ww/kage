package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jim-ww/kage/ui"
)

// Config is both the yaml wire shape and the fully-resolved value the rest
// of the app reads directly (cfg.MouseDisabled, cfg.HistoryPageSize, ...) —
// no separate "raw file" struct. Defaults are applied by pre-filling a
// Config with defaultConfig() before unmarshaling on top of it:
// yaml.Unmarshal only touches keys actually present in the file, so an
// absent key leaves the pre-filled default in place. See stripDefaults for
// the write-back side of the same idea.
//
// Every boolean here is named so its Go zero value (false) is the default
// (e.g. MouseDisabled rather than Mouse) — that means yaml.v3's plain
// `omitempty` already omits it correctly (its isZero treats false as
// empty), no separate defaults-prefill needed for bools specifically. It's
// only the handful of fields whose sensible default isn't the zero value
// (FilePickerSortField, HistoryPageSize, ...) that need defaultConfig().
//
// Theme and Keybinds are the exception to all of the above: they're
// partial-override shapes (empty string / absent key = "inherit"), not
// simple defaultable scalars, so they're deliberately left out of
// defaultConfig() and resolved on demand via ResolvedTheme/ResolvedKeyMap.
type Config struct {
	Keybinds map[string]any `yaml:"keybinds,omitempty"`
	Theme    ui.Theme       `yaml:"theme,omitempty"`

	// MouseDisabled disables mouse click/scroll support; off (mouse
	// enabled) by default.
	MouseDisabled bool `yaml:"mouse_disabled,omitempty"`
	// SidebarWidth is persisted from dragging the sidebar border; 0
	// (unset) means the width/4-based default.
	SidebarWidth int `yaml:"sidebar_width,omitempty"`
	// InputHeight is persisted from dragging the compose box border; 0
	// (unset) means the DynamicHeight-based default.
	InputHeight int `yaml:"input_height,omitempty"`
	// SidebarHidden is persisted from toggling the chat list (Ctrl+\ /
	// status-bar button); unset means open.
	SidebarHidden bool `yaml:"sidebar_hidden,omitempty"`
	// IconsDisabled hides icons for attachments/encryption in favor of
	// plain-text tags; off (icons shown) by default.
	IconsDisabled bool `yaml:"icons_disabled,omitempty"`
	// FilePickerFilesFirst shows files before directories in the
	// attach-file picker regardless of sort order; off (dirs first) by
	// default.
	FilePickerFilesFirst bool `yaml:"file_picker_files_first,omitempty"`
	// FilePickerSortField is "created" or "updated"; persisted from
	// cycling sort in the attach-file picker; "updated" by default.
	FilePickerSortField string `yaml:"file_picker_sort_field,omitempty"`
	// FilePickerSortAscending is persisted from cycling sort in the
	// attach-file picker; unset means descending.
	FilePickerSortAscending bool `yaml:"file_picker_sort_ascending,omitempty"`
	// ShowNames shows the sender's name in the message header instead of
	// just a direction glyph; off by default.
	ShowNames bool `yaml:"show_names,omitempty"`
	// TimeLayout is a custom Go time layout for message timestamps;
	// unset means the default ("15:04"/"2006-01-02 15:04").
	TimeLayout string `yaml:"time_layout,omitempty"`
	// AlwaysShowFullDate: with the default time layout, show the full
	// date even for messages sent today; off by default.
	AlwaysShowFullDate bool `yaml:"always_show_full_date,omitempty"`
	// DefaultAccount is the JID selected on startup; unset means the
	// first configured account.
	DefaultAccount string `yaml:"default_account,omitempty"`
	// OpenLastChatDisabled disables reopening LastChatAddress on
	// startup; off by default.
	OpenLastChatDisabled bool `yaml:"open_last_chat_disabled,omitempty"`
	// LastChatAccount is the JID of the account owning the last opened
	// chat.
	LastChatAccount string `yaml:"last_chat_account,omitempty"`
	// LastChatAddress is the peer JID of the last opened chat, reopened
	// on startup unless OpenLastChatDisabled.
	LastChatAddress string `yaml:"last_chat_address,omitempty"`
	// NotificationsDisabled disables desktop notifications for decrypted
	// incoming messages (the background daemon itself always runs); off
	// by default.
	NotificationsDisabled bool `yaml:"notifications_disabled,omitempty"`
	// TerminalCmd is the terminal emulator to launch from the tray icon;
	// unset means fall back to $TERMINAL, then xdg-terminal-exec, then a
	// hardcoded list.
	TerminalCmd string `yaml:"terminal_cmd,omitempty"`
	// AttachmentsDir is the directory decrypted/downloaded attachments
	// are cached in for viewing; unset means
	// $XDG_CACHE_HOME/kage/attachments (see os.UserCacheDir).
	AttachmentsDir string `yaml:"attachments_dir,omitempty"`
	// GPGDisabled disables gpg encryption entirely (never shells out to
	// gpg; "gpg" hidden from the per-chat encryption picker); off by
	// default.
	GPGDisabled bool `yaml:"gpg_disabled,omitempty"`
	// KeyringDisabled disables ever consulting the OS keyring; off by
	// default.
	KeyringDisabled bool `yaml:"keyring_disabled,omitempty"`
	// ShowEncryptedIcon shows a lock icon/tag next to encrypted messages;
	// off by default.
	ShowEncryptedIcon bool `yaml:"show_encrypted_icon,omitempty"`
	// HistoryPageSize is the number of messages loaded per chat at a
	// time (initial load + each "load older"); DefaultHistoryPageSize
	// when unset.
	HistoryPageSize int `yaml:"history_page_size,omitempty"`
	// MaxMessagesPerChat caps how many messages are kept in memory/view
	// per chat; DefaultMaxMessagesPerChat when unset.
	MaxMessagesPerChat int `yaml:"max_messages_per_chat,omitempty"`
	// NoticeDuration is seconds an in-app notification toast stays
	// visible before auto-dismissing; DefaultNoticeDurationSeconds when
	// unset.
	NoticeDuration int           `yaml:"notice_duration,omitempty"`
	Storage        StorageConfig `yaml:"storage,omitempty"`
	Accounts       []Account     `yaml:"accounts,omitempty"`

	// Path is the config file this was actually loaded from, or the
	// default write location if none was found — always non-empty, so
	// callers that need to persist a change (e.g. an auto-detected GPG
	// key) have somewhere to write it back to. Not part of the yaml
	// shape.
	Path string `yaml:"-"`
}

// StorageConfig configures the password local message history is encrypted
// under at rest (see ResolveStoragePassword) — one password for the whole
// database, shared by every configured account.
type StorageConfig struct {
	Password    string `yaml:"password,omitempty"`     // plaintext fallback
	PasswordCmd string `yaml:"password_cmd,omitempty"` // shell command printing the password on stdout
}

// DefaultHistoryPageSize is how many messages are loaded per chat at a time
// when history_page_size isn't set in config.yaml.
const DefaultHistoryPageSize = 200

// DefaultMaxMessagesPerChat is how many messages are kept loaded per chat
// when max_messages_per_chat isn't set in config.yaml.
const DefaultMaxMessagesPerChat = 1000

// DefaultNoticeDurationSeconds is how long (in seconds) an in-app
// notification toast stays visible before auto-dismissing when
// notice_duration isn't set in config.yaml.
const DefaultNoticeDurationSeconds = 3

// defaultConfig returns the Config to pre-fill before unmarshaling a file on
// top of it, so an absent yaml key leaves the default in place. Theme/
// Keybinds are deliberately left zero — see Config's doc.
func defaultConfig() Config {
	return Config{
		FilePickerSortField: "updated",
		HistoryPageSize:     DefaultHistoryPageSize,
		MaxMessagesPerChat:  DefaultMaxMessagesPerChat,
		NoticeDuration:      DefaultNoticeDurationSeconds,
	}
}

// NoticeDurationValue returns NoticeDuration as a time.Duration.
func (c Config) NoticeDurationValue() time.Duration {
	return time.Duration(c.NoticeDuration) * time.Second
}

// ResolvedTheme returns the theme to render with: DefaultTheme with any
// fields c.Theme sets overlaid on top.
func (c Config) ResolvedTheme() ui.Theme {
	return mergeTheme(ui.DefaultTheme(), c.Theme)
}

// ResolvedKeyMap returns the keymap to use: DefaultKeyMap with any bindings
// in c.Keybinds overlaid on top.
func (c Config) ResolvedKeyMap() (ui.KeyMap, error) {
	return applyKeybinds(ui.DefaultKeyMap, c.Keybinds)
}

// DefaultAccountIndex resolves DefaultAccount to an index into Accounts —
// the account selected on startup. 0 (the first account) when unset or when
// the configured JID doesn't match any account.
func (c Config) DefaultAccountIndex() int {
	if c.DefaultAccount == "" {
		return 0
	}
	for i, acct := range c.Accounts {
		if acct.JID == c.DefaultAccount {
			return i
		}
	}
	return 0
}

// LastChatAccountIndex resolves LastChatAccount to an index into Accounts.
// 0 when unset or when the configured JID doesn't match any account — only
// meaningful alongside a non-empty LastChatAddress.
func (c Config) LastChatAccountIndex() int {
	for i, acct := range c.Accounts {
		if acct.JID == c.LastChatAccount {
			return i
		}
	}
	return 0
}

func candidatePaths() []string {
	paths := make([]string, 0, 3)
	if env := strings.TrimSpace(os.Getenv("KAGE_CONFIG")); env != "" {
		paths = append(paths, env)
	}
	paths = append(paths, "config.yaml")
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append(paths, filepath.Join(home, ".config", "kage", "config.yaml"))
	}
	return paths
}

// Load reads the first config file found among path (if non-empty) and the
// usual candidate locations ($KAGE_CONFIG, ./config.yaml,
// ~/.config/kage/config.yaml), applying defaults for anything unset. If none
// exist, returns defaultConfig() with Path set to where a new file would be
// written.
func Load(path string) (Config, error) {
	for _, p := range append([]string{path}, candidatePaths()...) {
		if p == "" {
			continue
		}
		cfg, found, err := loadConfigFile(p)
		if err != nil {
			return defaultConfig(), err
		}
		if found {
			cfg.Path = p
			return cfg, nil
		}
	}
	cfg := defaultConfig()
	if defaultPath, err := DefaultWritePath(); err == nil {
		cfg.Path = defaultPath
	}
	return cfg, nil
}

// loadConfigFile pre-fills defaultConfig() and unmarshals path on top of it.
// found is false (with a zero error) when path doesn't exist.
func loadConfigFile(path string) (cfg Config, found bool, err error) {
	cfg = defaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, false, nil
		}
		return cfg, false, fmt.Errorf("read config %q: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, false, fmt.Errorf("parse config %q: %w", path, err)
	}
	return cfg, true, nil
}

// stripDefaults zeroes any field of cfg that equals the corresponding field
// in def, so that field's `omitempty` tag drops it from the encoded output —
// used before writing the file back so config.yaml only ever contains
// settings that differ from default.
func stripDefaults(cfg *Config, def Config) {
	v := reflect.ValueOf(cfg).Elem()
	d := reflect.ValueOf(def)
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if f.CanSet() && reflect.DeepEqual(f.Interface(), d.Field(i).Interface()) {
			f.Set(reflect.Zero(f.Type()))
		}
	}
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
		case "sort_file_picker":
			keys.SortFilePicker = ui.NewBinding(override, "cycle sort")
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
