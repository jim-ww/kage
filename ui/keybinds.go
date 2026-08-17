package ui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// KeyMap holds all configurable key bindings.
type KeyMap struct {
	Quit                  key.Binding
	Back                  key.Binding
	Switch                key.Binding
	FocusChats            key.Binding
	ChatOpen              key.Binding
	SelectSend            key.Binding
	MsgUp                 key.Binding // k — navigate to previous message
	MsgDown               key.Binding // j — navigate to next message
	HalfPageUp            key.Binding // Ctrl+U — jump up by half the visible messages
	HalfPageDown          key.Binding // Ctrl+D — jump down by half the visible messages
	DeleteMsg             key.Binding // Ctrl+Shift+D — delete selected message (with popup)
	YankMsg               key.Binding // Ctrl+Y — yank selected message
	YankDraft             key.Binding // Ctrl+Shift+Y — copy the compose box's current draft
	EditMsg               key.Binding // Ctrl+E — edit (only last own message)
	ReplyMsg              key.Binding // Ctrl+R — reply to selected message
	RetryMsg              key.Binding // Ctrl+Shift+T — retry a failed send
	InfoMsg               key.Binding // Ctrl+I — show message info popup
	OpenMsg               key.Binding // Ctrl+O — open links/attachments in selected message
	SaveMsg               key.Binding // Ctrl+S — save links/attachments in selected message to disk
	SaveMsgAs             key.Binding // Ctrl+Shift+S — save links/attachments, prompting for a destination path first
	ReactMsg              key.Binding // Ctrl+T — compose a reaction (shortcode/emoji) on the selected message
	ConfirmYes            key.Binding // y — confirm popup
	ConfirmNo             key.Binding // n / esc — cancel popup
	AddAccount            key.Binding // a — open the add-account form (only while accounts panel is focused)
	AttachFile            key.Binding // Ctrl+F — open the file picker to attach/send a file (toggles closed if pressed again)
	SortFilePicker        key.Binding // Ctrl+S — cycle the file picker's sort order (updated/created × asc/desc)
	PasteImage            key.Binding // Ctrl+P — stage whatever image is on the system clipboard as an attachment
	RenameChat            key.Binding // r — open the rename-contact prompt for the selected chat
	ToggleSidebar         key.Binding // Ctrl+\ — show/hide the chat list sidebar
	DeviceList            key.Binding // u (accounts panel) — view/purge the current account's published OMEMO device list
	ContactManager        key.Binding // c — add/remove roster contacts for the current account (accounts panel)
	RemoveAttachment      key.Binding // Backspace (on empty input) — drop the highlighted pending attachment
	ClearDraft            key.Binding // Ctrl+Shift+E — erase the compose box
	UndoDraft             key.Binding // Ctrl+Z — undo the last change to the compose box
	RedoDraft             key.Binding // Ctrl+Shift+Z — redo a change undone by UndoDraft
	ChangeStoragePassword key.Binding // Ctrl+Shift+P — change the local message/draft storage encryption password (accounts panel)
	CallToggle            key.Binding // Ctrl+G — start a voice call to the open chat, or hang up the current call
	VideoCallToggle       key.Binding // Ctrl+Shift+G — start a video call to the open chat (prompts camera/screen), or hang up the current call
	ToggleComposeExpand   key.Binding // Ctrl+` — grow the compose box to ~half the chat pane, or shrink it back
	Help                  key.Binding // Ctrl+H — open the full-keybindings help popup
	SearchChat            key.Binding // Ctrl+/ — search messages in the open chat
	ListKeys              list.KeyMap
	TextInputKeys         textinput.KeyMap
	InputAreaKeys         textarea.KeyMap
}

// defaultInputAreaKeys is textarea.DefaultKeyMap with InsertNewline moved off
// plain "enter" — enter is globally bound to SelectSend (send the message),
// so the compose box's own newline key must be something else to avoid the
// two racing for the same keypress. shift+enter only arrives as a distinct
// key when the terminal supports the Kitty keyboard protocol (kitty,
// wezterm, ghostty, alacritty, foot, ...) — plain VTE/xterm-family terminals
// and unpatched tmux report it identically to bare enter, which SelectSend
// would swallow first. alt+enter (ESC + CR, no special protocol needed) is
// kept as a fallback that works in effectively every terminal.
func defaultInputAreaKeys() textarea.KeyMap {
	km := textarea.DefaultKeyMap()
	km.InsertNewline = key.NewBinding(key.WithKeys("shift+enter", "alt+enter"), key.WithHelp("shift+enter", "new line"))
	// ctrl+left/right alongside the defaults (alt+left/right) — the more
	// familiar word-jump binding in terminals/editors outside the readline
	// tradition, and free to bind here (nothing else in the app claims
	// ctrl+left/right).
	km.WordBackward.SetKeys(append(km.WordBackward.Keys(), "ctrl+left")...)
	km.WordForward.SetKeys(append(km.WordForward.Keys(), "ctrl+right")...)
	return km
}

// caretKey shortens a "ctrl+x" key label to "^X" — the footer hint is
// already tight on space, and control-key labels are the common case there.
// Anything else (plain letters, "up", "tab", "esc", ...) passes through
// unchanged.
func caretKey(k string) string {
	rest, ok := strings.CutPrefix(k, "ctrl+")
	if !ok {
		return k
	}
	return "^" + strings.ToUpper(rest)
}

// NewBinding builds a key.Binding bound to keys, described by desc.
func NewBinding(keys []string, desc string) key.Binding {
	return key.NewBinding(key.WithKeys(keys...), key.WithHelp(strings.Join(keys, "/"), desc))
}

// DefaultKeyMap is the default set of key bindings.
var DefaultKeyMap = KeyMap{
	Quit:         NewBinding([]string{"q", "ctrl+c"}, "quit"),
	Back:         NewBinding([]string{"esc"}, "back to chats"),
	Switch:       NewBinding([]string{"tab"}, "switch focus"),
	FocusChats:   NewBinding([]string{"ctrl+\\"}, "focus chats"),
	ChatOpen:     NewBinding([]string{"l", "right"}, "open chat"),
	SelectSend:   NewBinding([]string{"enter"}, "select/send"),
	MsgUp:        NewBinding([]string{"ctrl+k", "up"}, "prev msg"),
	MsgDown:      NewBinding([]string{"ctrl+j", "down"}, "next msg"),
	HalfPageUp:   NewBinding([]string{"ctrl+u"}, "half page up"),
	HalfPageDown: NewBinding([]string{"ctrl+d"}, "half page down"),
	DeleteMsg:    NewBinding([]string{"ctrl+shift+d"}, "delete"),
	YankMsg:      NewBinding([]string{"ctrl+y"}, "yank"),
	YankDraft:    NewBinding([]string{"ctrl+shift+y"}, "copy draft"),
	EditMsg:      NewBinding([]string{"ctrl+e"}, "edit (own last)"),
	ReplyMsg:     NewBinding([]string{"ctrl+r"}, "reply"),
	RetryMsg:     NewBinding([]string{"ctrl+shift+t"}, "retry"),
	InfoMsg:      NewBinding([]string{"ctrl+i"}, "message info"),
	OpenMsg:      NewBinding([]string{"ctrl+o"}, "open links/attachments"),
	SaveMsg:      NewBinding([]string{"ctrl+s"}, "save links/attachments"),
	SaveMsgAs:    NewBinding([]string{"ctrl+shift+s"}, "save links/attachments as"),
	ReactMsg:     NewBinding([]string{"ctrl+t"}, "react"),
	ConfirmYes:   NewBinding([]string{"y"}, "yes"),
	ConfirmNo:    NewBinding([]string{"n", "esc"}, "no"),
	AddAccount:   NewBinding([]string{"a"}, "add account"),
	AttachFile:   NewBinding([]string{"ctrl+f"}, "attach file"),
	// Ctrl+<letter> combos are matched by physical key position on
	// essentially all terminals (see matchesKey), unlike bare letters whose
	// layout-independent BaseCode needs Kitty protocol support the user's
	// terminal may not have — pick Ctrl+S here so sorting actually works
	// under non-Latin layouts instead of just in theory. Reused from
	// SaveMsg's binding is fine: the file picker intercepts all input while
	// open, so SaveMsg never sees a keypress in that state.
	SortFilePicker:        NewBinding([]string{"ctrl+s"}, "cycle sort"),
	PasteImage:            NewBinding([]string{"ctrl+p"}, "paste image"),
	RenameChat:            NewBinding([]string{"r"}, "rename chat"),
	ToggleSidebar:         NewBinding([]string{"ctrl+shift+\\"}, "toggle chat list"),
	DeviceList:            NewBinding([]string{"u"}, "omemo devices"),
	ContactManager:        NewBinding([]string{"c"}, "manage contacts"),
	RemoveAttachment:      NewBinding([]string{"backspace"}, "remove attachment"),
	ClearDraft:            NewBinding([]string{"ctrl+shift+e"}, "erase draft"),
	ChangeStoragePassword: NewBinding([]string{"ctrl+shift+p"}, "change storage password"),
	CallToggle:            NewBinding([]string{"ctrl+g"}, "call"),
	VideoCallToggle:       NewBinding([]string{"ctrl+shift+g"}, "video call"),
	ToggleComposeExpand:   NewBinding([]string{"ctrl+`"}, "expand input"),
	UndoDraft:             NewBinding([]string{"ctrl+z"}, "undo"),
	RedoDraft:             NewBinding([]string{"ctrl+shift+z"}, "redo"),
	Help:                  NewBinding([]string{"ctrl+h"}, "help"),
	// "ctrl+?" is the intended gesture (ctrl + the "?" that shares the "/"
	// key on a US layout), but no terminal actually reports that literal
	// string: legacy encoding sends the raw ctrl+/ control byte as
	// "ctrl+_" (see uv's C0 table — ansi.US maps to Code:'_', ModCtrl),
	// while under the Kitty protocol used here (see ReportAllKeysAsEscapeCodes
	// in view.go) Key.String() prefers the shifted "?" text over the
	// ctrl-prefixed keystroke, so it plainly reports "ctrl+/" (no shift in
	// the string) or occasionally "ctrl+shift+/". Bind every variant; list
	// "ctrl+/" first purely so it's what the footer/help-popup label shows.
	SearchChat: key.NewBinding(
		key.WithKeys("ctrl+/", "ctrl+_", "ctrl+shift+/"),
		key.WithHelp("ctrl+/", "search"),
	),

	ListKeys:      list.DefaultKeyMap(),
	TextInputKeys: textinput.DefaultKeyMap(),
	InputAreaKeys: defaultInputAreaKeys(),
}

// matchesKey reports whether msg matches any of b's bound keys. For a
// single-letter binding it prefers the layout-independent PC-101 base key
// (populated only under the Kitty keyboard protocol, e.g. kitty, wezterm,
// ghostty, foot) over the literal typed character, so bindings like "c" for
// ContactManager still fire when the active keyboard layout produces a
// different character for that physical key (e.g. Cyrillic). Falls back to
// key.Matches (which compares msg.String(), the typed character) when no
// base code is available — the same behavior as before this existed.
func matchesKey(msg tea.KeyMsg, b key.Binding) bool {
	kk := msg.Key()
	if kk.BaseCode != 0 && kk.Mod == 0 {
		for _, want := range b.Keys() {
			if len(want) == 1 && rune(want[0]) == kk.BaseCode {
				return b.Enabled()
			}
		}
	}
	return key.Matches(msg, b)
}

// matchesLetter is matchesKey's counterpart for call sites that switch on a
// raw msg.String() letter instead of a key.Binding.
func matchesLetter(msg tea.KeyMsg, r rune) bool {
	kk := msg.Key()
	if kk.BaseCode != 0 && kk.Mod == 0 {
		return kk.BaseCode == r
	}
	return msg.String() == string(r)
}

// helpEntry pairs a binding with the description it should show for a
// particular view — the same binding can mean different things depending on
// which view it's pressed in (e.g. SelectSend is "select" in the accounts
// list but "send" in a chat).
type helpEntry struct {
	binding key.Binding
	desc    string
}

// shortestKey is the shortest bound key for b, not the full "ctrl+k/up"-style
// joined label Help().Key would give — used wherever space is tight (the
// footer) or a single representative label is wanted (the help popup).
func shortestKey(b key.Binding) string {
	keys := b.Keys()
	if len(keys) == 0 {
		return ""
	}
	shortest := keys[0]
	for _, k := range keys[1:] {
		if len(k) < len(shortest) {
			shortest = k
		}
	}
	return shortest
}

// globalEntries lists bindings that work the same in every view — shown as
// their own section in the help popup, and appended to each view's entries
// for the footer hint.
func (k KeyMap) globalEntries() []helpEntry {
	return []helpEntry{
		{k.Switch, "switch focus"},
		{k.Help, "help"},
		{k.Quit, "quit"},
	}
}

// viewEntries lists the bindings meaningful in view, each paired with the
// description that applies there. This is the single source of truth for
// both the footer hint (helpHint) and the per-tab sections in the help popup
// (renderHelpPopup) — keeping one list means the two can't drift apart.
func (k KeyMap) viewEntries(view selectedView, hasPendingAttachments bool) []helpEntry {
	switch view {
	case viewAccounts:
		return []helpEntry{
			{k.MsgUp, "prev account"},
			{k.MsgDown, "next account"},
			{k.SelectSend, "select"},
			{k.AddAccount, "add"},
			{k.DeviceList, "omemo devices"},
			{k.ContactManager, "contacts"},
			{k.ChangeStoragePassword, "change storage password"},
		}
	case viewChats:
		return []helpEntry{
			{k.ListKeys.CursorUp, "up"},
			{k.ListKeys.CursorDown, "down"},
			{k.ChatOpen, "open"},
			{k.RenameChat, "rename"},
			{k.DeleteMsg, "delete"},
			{k.ListKeys.Filter, "filter"},
			{k.ToggleSidebar, "hide chats"},
		}
	case viewChat:
		// Ordered by how often each is used — least-used trail off the end
		// so narrow terminals still show the important ones first when
		// footerMaxLines caps how far the footer hint wraps.
		entries := []helpEntry{
			{k.SelectSend, "send"},
			{k.ReplyMsg, "reply"},
			{k.EditMsg, "edit"},
			{k.DeleteMsg, "delete"},
			{k.RetryMsg, "retry"},
			{k.ReactMsg, "react"},
			{k.SearchChat, "search"},
			{k.YankMsg, "yank"},
			{k.InfoMsg, "info"},
			{k.OpenMsg, "open"},
			{k.SaveMsg, "save"},
			{k.SaveMsgAs, "save as"},
			{k.AttachFile, "attach"},
			{k.PasteImage, "paste image"},
			{k.CallToggle, "call"},
			{k.VideoCallToggle, "video call"},
			{k.ClearDraft, "erase draft"},
			{k.YankDraft, "copy draft"},
			{k.UndoDraft, "undo"},
			{k.RedoDraft, "redo"},
			{k.ToggleComposeExpand, "expand input"},
		}
		if hasPendingAttachments {
			entries = append(entries, helpEntry{k.RemoveAttachment, "remove attachment"})
		}
		entries = append(entries,
			helpEntry{k.FocusChats, "chats"},
			helpEntry{k.ToggleSidebar, "hide list"},
			helpEntry{k.Back, "back"},
		)
		return entries
	default:
		return nil
	}
}

// helpHint returns a view-specific key hint for the app-wide footer, one
// "key desc" entry per binding joined by " · ". Key labels are pulled from
// the (possibly user-remapped) bindings; the descriptions are tailored to
// what each key does in that particular view.
func (k KeyMap) helpHint(view selectedView, hasPendingAttachments bool) string {
	entries := k.viewEntries(view, hasPendingAttachments)
	if entries == nil {
		return ""
	}
	entries = append(entries, k.globalEntries()...)

	// Within an entry, join key and desc (and desc's own words) with a
	// non-breaking space instead of a regular one — wrapFooterHint word-wraps
	// this whole string, and a regular space would let it break an entry
	// across lines (e.g. "ctrl+k" on one line, "prev msg" on the next). Only
	// the " · " between entries is meant to be a wrap point.
	parts := make([]string, len(entries))
	for i, e := range entries {
		key := shortestKey(e.binding)
		if key == "" {
			parts[i] = e.desc
			continue
		}
		parts[i] = strings.Join(strings.Fields(caretKey(key)+" "+e.desc), " ")
	}
	return strings.Join(parts, " · ")
}

// helpSection is one titled group of bindings in the full-keybindings help
// popup — one section per tab (view) plus a "Global" section, so it's clear
// which keys only apply while that tab is focused.
type helpSection struct {
	Title   string
	Entries []helpEntry
}

// helpSections builds the full-keybindings help popup's content: one section
// per tab (labeled so it's clear which keys apply where), plus the bindings
// that work everywhere.
func (k KeyMap) helpSections() []helpSection {
	return []helpSection{
		{"Accounts tab", k.viewEntries(viewAccounts, false)},
		{"Chats tab", k.viewEntries(viewChats, false)},
		// hasPendingAttachments: true so the popup documents every binding
		// that view can use, not just the ones live for the current draft.
		{"Chat tab", k.viewEntries(viewChat, true)},
		{"Global", k.globalEntries()},
	}
}
