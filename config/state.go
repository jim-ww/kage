package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// State holds settings the app itself persists at runtime - dragged sidebar
// width, last opened chat, cycled sort order, ... - as opposed to Config,
// which holds settings a user (or a Nix module) sets deliberately. Kept in
// its own file (state.yaml, next to config.yaml) specifically so a
// declaratively-managed config.yaml (e.g. written read-only by Nix/
// home-manager) is never clobbered by the app's own runtime writes: every
// Set* helper in setup.go that used to rewrite config.yaml now rewrites
// state.yaml instead, and Load never writes state.yaml's fields back into
// config.yaml.
type State struct {
	// SidebarWidth is persisted from dragging the sidebar border; 0 (unset)
	// means the width/4-based default.
	SidebarWidth int `yaml:"sidebar_width,omitempty"`
	// InputHeight is persisted from dragging the compose box border; 0
	// (unset) means the DynamicHeight-based default.
	InputHeight int `yaml:"input_height,omitempty"`
	// SidebarHidden is persisted from toggling the chat list (Ctrl+\ /
	// status-bar button); unset means open.
	SidebarHidden bool `yaml:"sidebar_hidden,omitempty"`
	// FilePickerSortField is "created" or "updated"; persisted from
	// cycling sort in the attach-file picker; "updated" by default.
	FilePickerSortField string `yaml:"file_picker_sort_field,omitempty"`
	// FilePickerSortAscending is persisted from cycling sort in the
	// attach-file picker; unset means descending.
	FilePickerSortAscending bool `yaml:"file_picker_sort_ascending,omitempty"`
	// DefaultAccount is the JID selected on startup; unset means the first
	// configured account. Persisted whenever the user switches the default
	// account from the UI.
	DefaultAccount string `yaml:"default_account,omitempty"`
	// LastChatAccount is the JID of the account owning the last opened
	// chat.
	LastChatAccount string `yaml:"last_chat_account,omitempty"`
	// LastChatAddress is the peer JID of the last opened chat, reopened on
	// startup unless Config.OpenLastChatDisabled.
	LastChatAddress string `yaml:"last_chat_address,omitempty"`
	// AccountStatuses is JID -> configured presence ("", "chat", "away",
	// "xa", "dnd", "offline"), persisted immediately whenever changed from
	// the UI so a restart comes back up in the same status. Keyed
	// separately from Config.Accounts (which is declarative/Nix-managed)
	// rather than living on the Account struct.
	AccountStatuses map[string]string `yaml:"account_statuses,omitempty"`
	// ReactionEmojiUsage is emoji -> how many times the user has sent it as
	// a reaction, incremented every time a reaction is sent (see
	// ui.ReactionEmojiUsageRecorder). Ranked descending to build the
	// quick-pick default suggestions offered before any typing (see
	// ui.defaultEmojiSuggestions) so the picker converges on what this user
	// actually reaches for instead of a fixed list.
	ReactionEmojiUsage map[string]int `yaml:"reaction_emoji_usage,omitempty"`

	// Path is the state file this was loaded from / would be written to.
	// Not part of the yaml shape.
	Path string `yaml:"-"`
}

func defaultState() State {
	return State{
		FilePickerSortField: "updated",
	}
}

// statePath returns the state.yaml path that sits alongside the config file
// at cfgPath.
func statePath(cfgPath string) string {
	return filepath.Join(filepath.Dir(cfgPath), "state.yaml")
}

// loadState reads the state file next to cfgPath, applying defaults for
// anything unset. A missing file is not an error - it just means no runtime
// state has been persisted yet.
func loadState(cfgPath string) (State, error) {
	st := defaultState()
	st.Path = statePath(cfgPath)
	data, err := os.ReadFile(st.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return st, fmt.Errorf("read state %q: %w", st.Path, err)
	}
	if err := yaml.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("parse state %q: %w", st.Path, err)
	}
	return st, nil
}

// writeState writes st to its Path, stripping fields equal to their default
// so state.yaml only ever contains settings that differ from default -
// mirrors stripDefaults/writeFileConfig's behavior for config.yaml.
func writeState(st State) error {
	def := defaultState()
	out := st
	out.Path = ""
	if out.FilePickerSortField == def.FilePickerSortField {
		out.FilePickerSortField = ""
	}
	if len(out.AccountStatuses) == 0 {
		out.AccountStatuses = nil
	}
	if len(out.ReactionEmojiUsage) == 0 {
		out.ReactionEmojiUsage = nil
	}

	if dir := filepath.Dir(st.Path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create state dir %q: %w", dir, err)
		}
	}
	data, err := yaml.Marshal(out)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	if err := os.WriteFile(st.Path, data, 0o600); err != nil {
		return fmt.Errorf("write state %q: %w", st.Path, err)
	}
	return nil
}
