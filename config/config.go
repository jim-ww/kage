package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/jim-ww/kage/ui"
)

// Config is config.toml's shape and what the app reads. Defaults are
// pre-filled (defaultConfig) before unmarshaling over them, and stripped on
// write (stripDefaults). Bools are named so false is the default.
// Theme and Keybinds override parts of the defaults, resolved by
// ResolvedTheme/ResolvedKeyMap.
type Config struct {
	Keybinds map[string]any `toml:"keybinds,omitempty"`
	Theme    ui.Theme       `toml:"theme,omitempty"`

	// MouseDisabled disables mouse click/scroll support; off (mouse
	// enabled) by default.
	MouseDisabled bool `toml:"mouse_disabled,omitempty"`
	// IconsDisabled hides icons for attachments/encryption in favor of
	// plain-text tags; off (icons shown) by default.
	IconsDisabled bool `toml:"icons_disabled,omitempty"`
	// FilePickerFilesFirst shows files before directories in the
	// attach-file picker regardless of sort order; off (dirs first) by
	// default.
	FilePickerFilesFirst bool `toml:"file_picker_files_first,omitempty"`
	// TimeLayout is a custom Go time layout for message timestamps;
	// unset means the default bare "15:04" (dates are shown via a
	// once-per-day divider line, not repeated on every message).
	TimeLayout string `toml:"time_layout,omitempty"`
	// OpenLastChatDisabled disables reopening the last opened chat
	// (State.LastChatAddress) on startup; off by default.
	OpenLastChatDisabled bool `toml:"open_last_chat_disabled,omitempty"`
	// NotificationsDisabled disables desktop notifications for decrypted
	// incoming messages (the background daemon itself always runs); off
	// by default.
	NotificationsDisabled bool `toml:"notifications_disabled,omitempty"`
	// TerminalCmd is the terminal emulator to launch from the tray icon;
	// unset means fall back to $TERMINAL, then xdg-terminal-exec, then a
	// hardcoded list.
	TerminalCmd string `toml:"terminal_cmd,omitempty"`
	// TrayMono uses the white tray icon, for panels that tint it; there's
	// no telling from the desktop.
	TrayMono bool `toml:"tray_mono,omitempty"`
	// AttachmentsDir is the directory decrypted/downloaded attachments
	// are cached in for viewing; unset means
	// $XDG_CACHE_HOME/kage/attachments (see os.UserCacheDir).
	AttachmentsDir string `toml:"attachments_dir,omitempty"`
	// GPGDisabled disables gpg encryption entirely (never shells out to
	// gpg; "gpg" hidden from the per-chat encryption picker); off by
	// default.
	GPGDisabled bool `toml:"gpg_disabled,omitempty"`
	// KeyringDisabled disables ever consulting the OS keyring; off by
	// default.
	KeyringDisabled bool `toml:"keyring_disabled,omitempty"`
	// ContactColors tints contacts' names in the chat list, by bare JID, as
	// "#rgb" or "#rrggbb".
	ContactColors map[string]string `toml:"contact_colors,omitempty"`
	// AutoUnhideDisabled keeps a hidden chat hidden when a message arrives.
	AutoUnhideDisabled bool `toml:"auto_unhide_disabled,omitempty"`
	// AvatarsDisabled hides the avatar panel and stops fetching avatars.
	AvatarsDisabled bool `toml:"avatars_disabled,omitempty"`
	// HoverSelectDisabled stops hovering a message from selecting it, so
	// only a click (or the keyboard) does; off (hover selects) by default.
	HoverSelectDisabled bool `toml:"hover_select_disabled,omitempty"`
	// Autofocus selects and scrolls to new messages in the open chat as
	// they arrive: "own" (yours), "others" (everyone else's) or "all".
	// Unset by default: the chat only follows along if it's already
	// scrolled to the bottom.
	Autofocus ui.Autofocus `toml:"autofocus,omitempty"`
	// ShowEncryptedIcon shows a lock icon/tag next to encrypted messages;
	// off by default.
	ShowEncryptedIcon bool `toml:"show_encrypted_icon,omitempty"`
	// DefaultEncryptionMode is a chat's encryption until one is picked:
	// "omemo-v1" (the default), "omemo-v2", "gpg" or "none".
	DefaultEncryptionMode string `toml:"default_encryption_mode,omitempty"`
	// Debug logs at debug level to <config dir>/kage/debug.log instead of
	// warn; overridden by the -debug flag or KAGE_DEBUG env var if either
	// is set. Off by default.
	Debug bool `toml:"debug,omitempty"`
	// HistoryPageSize is the number of messages loaded per chat at a
	// time (initial load + each "load older"); DefaultHistoryPageSize
	// when unset.
	HistoryPageSize int `toml:"history_page_size,omitempty"`
	// MaxMessagesPerChat caps how many messages are kept in memory/view
	// per chat; DefaultMaxMessagesPerChat when unset.
	MaxMessagesPerChat int `toml:"max_messages_per_chat,omitempty"`
	// NoticeDuration is seconds an in-app notification toast stays
	// visible before auto-dismissing; DefaultNoticeDurationSeconds when
	// unset.
	NoticeDuration int `toml:"notice_duration,omitempty"`
	// VideoQuality is our video's quality: "very_low", "low", "medium"
	// (default) or "high".
	VideoQuality string `toml:"video_quality,omitempty"`
	// ScreenRecorder captures the screen: "gpu-screen-recorder" (default,
	// hardware) or "wf-recorder" (CPU).
	ScreenRecorder string        `toml:"screen_recorder,omitempty"`
	Storage        StorageConfig `toml:"storage,omitempty"`
	Accounts       []Account     `toml:"accounts,omitempty"`

	// State is state.toml, what the app saves itself (see State).
	State State `toml:"-"`

	// Path is the file loaded, or where one would be written.
	Path string `toml:"-"`
}

// StorageConfig configures the password local message history is encrypted
// under at rest (see ResolveStoragePassword) — one password for the whole
// database, shared by every configured account.
type StorageConfig struct {
	Password    string `toml:"password,omitempty"`     // plaintext fallback
	PasswordCmd string `toml:"password_cmd,omitempty"` // shell command printing the password on stdout
}

// DefaultHistoryPageSize is how many messages are loaded per chat at a time
// when history_page_size isn't set in config.toml.
const DefaultHistoryPageSize = 60

// DefaultMaxMessagesPerChat is how many messages are kept loaded per chat
// when max_messages_per_chat isn't set in config.toml.
const DefaultMaxMessagesPerChat = 120

// DefaultNoticeDurationSeconds is how long (in seconds) an in-app
// notification toast stays visible before auto-dismissing when
// notice_duration isn't set in config.toml.
const DefaultNoticeDurationSeconds = 3

// DefaultVideoQuality is the capture profile used when video_quality isn't
// set in config.toml.
const DefaultVideoQuality = "medium"

// DefaultScreenRecorder is the screen-share capture program used when
// screen_recorder isn't set in config.toml.
const DefaultScreenRecorder = "gpu-screen-recorder"

// DefaultEncryptionMode is the outgoing encryption mode a chat starts with
// when default_encryption_mode isn't set in config.toml.
const DefaultEncryptionMode = "omemo-v1"

// defaultConfig returns the Config to pre-fill before unmarshaling a file on
// top of it, so an absent toml key leaves the default in place. Theme/
// Keybinds are deliberately left zero — see Config's doc.
func defaultConfig() Config {
	return Config{
		HistoryPageSize:       DefaultHistoryPageSize,
		MaxMessagesPerChat:    DefaultMaxMessagesPerChat,
		NoticeDuration:        DefaultNoticeDurationSeconds,
		VideoQuality:          DefaultVideoQuality,
		ScreenRecorder:        DefaultScreenRecorder,
		DefaultEncryptionMode: DefaultEncryptionMode,
	}
}

// NoticeDurationValue returns NoticeDuration as a time.Duration.
func (c Config) NoticeDurationValue() time.Duration {
	return time.Duration(c.NoticeDuration) * time.Second
}

// ResolvedTheme returns the theme to render with: the preset c.Theme names
// (the terminal's own colors by default) with any colors c.Theme sets
// overlaid on top.
func (c Config) ResolvedTheme() ui.Theme {
	base, ok := ui.ThemePreset(c.Theme.Preset)
	if !ok {
		base = ui.DefaultTheme() // rejected at load; see loadConfigFile
	}
	return mergeTheme(base, c.Theme)
}

// ResolvedKeyMap returns the keymap to use: DefaultKeyMap with any bindings
// in c.Keybinds overlaid on top.
func (c Config) ResolvedKeyMap() (ui.KeyMap, error) {
	return applyKeybinds(ui.DefaultKeyMap, c.Keybinds)
}

// DefaultAccountIndex resolves State.DefaultAccount to an index into
// Accounts — the account selected on startup. 0 (the first account) when
// unset or when the configured JID doesn't match any account.
func (c Config) DefaultAccountIndex() int {
	if c.State.DefaultAccount == "" {
		return 0
	}
	for i, acct := range c.Accounts {
		if acct.JID == c.State.DefaultAccount {
			return i
		}
	}
	return 0
}

// LastChatAccountIndex resolves State.LastChatAccount to an index into
// Accounts. 0 when unset or when the configured JID doesn't match any
// account — only meaningful alongside a non-empty State.LastChatAddress.
func (c Config) LastChatAccountIndex() int {
	for i, acct := range c.Accounts {
		if acct.JID == c.State.LastChatAccount {
			return i
		}
	}
	return 0
}

// File is name's path in kage's own directory under the user config dir,
// created if need be - the database, socket, lock and logs all live there.
func File(name string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "kage")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
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

// Load reads path, or the first of $KAGE_CONFIG, ./config.toml and
// ~/.config/kage/config.toml. With none, it's the defaults.
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
			return loadStateInto(cfg)
		}
	}
	cfg := defaultConfig()
	if defaultPath, err := DefaultWritePath(); err == nil {
		cfg.Path = defaultPath
	}
	return loadStateInto(cfg)
}

// loadStateInto loads state.toml into cfg.State, its account statuses over
// config's: they're the latest choice.
func loadStateInto(cfg Config) (Config, error) {
	st, err := loadState(cfg.Path)
	if err != nil {
		return cfg, err
	}
	cfg.State = st
	for i, acct := range cfg.Accounts {
		if status, ok := st.AccountStatuses[acct.JID]; ok {
			cfg.Accounts[i].Status = status
		}
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
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return cfg, false, fmt.Errorf("parse config %q: %w", path, err)
	}
	if _, ok := ui.ThemePreset(cfg.Theme.Preset); !ok {
		return cfg, false, fmt.Errorf("config %q: theme preset = %q, want %q or %q", path, cfg.Theme.Preset, ui.ThemeTerminal, ui.ThemeTokyoNight)
	}
	if !cfg.Autofocus.Valid() {
		return cfg, false, fmt.Errorf("config %q: autofocus = %q, want \"own\", \"others\" or \"all\"", path, cfg.Autofocus)
	}
	return cfg, true, nil
}

// stripDefaults zeroes cfg's fields equal to def's, so omitempty leaves them
// out of the file.
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
			keys.EditMsg = ui.NewBinding(override, "edit")
		case "reply":
			keys.ReplyMsg = ui.NewBinding(override, "reply")
		case "info":
			keys.InfoMsg = ui.NewBinding(override, "message info")
		case "expand":
			keys.ExpandMsg = ui.NewBinding(override, "expand/collapse message")
		case "jump_latest":
			keys.JumpLatest = ui.NewBinding(override, "jump to latest")
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
		case "call_menu":
			keys.CallMenu = ui.NewBinding(override, "call")
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
