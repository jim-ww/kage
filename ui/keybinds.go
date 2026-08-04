package ui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
)

type KeyMap struct {
	Quit          key.Binding
	Back          key.Binding
	Switch        key.Binding
	FocusChats    key.Binding
	ChatOpen      key.Binding
	SelectSend    key.Binding
	MsgUp         key.Binding // k — navigate to previous message
	MsgDown       key.Binding // j — navigate to next message
	DeleteMsg     key.Binding // Ctrl+D — delete selected message (with popup)
	YankMsg       key.Binding // Ctrl+Y — yank selected message
	EditMsg       key.Binding // Ctrl+E — edit (only last own message)
	ReplyMsg      key.Binding // Ctrl+R — reply to selected message
	InfoMsg       key.Binding // Ctrl+G — show message info popup
	OpenMsg       key.Binding // Ctrl+O — open links/attachments in selected message
	SaveMsg       key.Binding // Ctrl+W — save links/attachments in selected message to disk
	ReactMsg      key.Binding // Ctrl+T — compose a reaction (shortcode/emoji) on the selected message
	ConfirmYes    key.Binding // y — confirm popup
	ConfirmNo     key.Binding // n / esc — cancel popup
	AddAccount    key.Binding // a — open the add-account form (only while accounts panel is focused)
	AttachFile    key.Binding // Ctrl+F — open the file picker to attach/send a file (toggles closed if pressed again)
	RenameChat    key.Binding // r — open the rename-contact prompt for the selected chat
	ToggleSidebar key.Binding // Ctrl+\ — show/hide the chat list sidebar
	ListKeys      list.KeyMap
	TextInputKeys textinput.KeyMap
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

func NewBinding(keys []string, desc string) key.Binding {
	return key.NewBinding(key.WithKeys(keys...), key.WithHelp(strings.Join(keys, "/"), desc))
}

var DefaultKeyMap = KeyMap{
	Quit:          NewBinding([]string{"q", "ctrl+c"}, "quit"),
	Back:          NewBinding([]string{"esc"}, "back to chats"),
	Switch:        NewBinding([]string{"tab"}, "switch focus"),
	FocusChats:    NewBinding([]string{"\\"}, "focus chats"),
	ChatOpen:      NewBinding([]string{"l", "right"}, "open chat"),
	SelectSend:    NewBinding([]string{"enter"}, "select/send"),
	MsgUp:         NewBinding([]string{"ctrl+k", "up"}, "prev msg"),
	MsgDown:       NewBinding([]string{"ctrl+j", "down"}, "next msg"),
	DeleteMsg:     NewBinding([]string{"ctrl+d"}, "delete"),
	YankMsg:       NewBinding([]string{"ctrl+y"}, "yank"),
	EditMsg:       NewBinding([]string{"ctrl+e"}, "edit (own last)"),
	ReplyMsg:      NewBinding([]string{"ctrl+r"}, "reply"),
	InfoMsg:       NewBinding([]string{"ctrl+g"}, "message info"),
	OpenMsg:       NewBinding([]string{"ctrl+o"}, "open links/attachments"),
	SaveMsg:       NewBinding([]string{"ctrl+w"}, "save links/attachments"),
	ReactMsg:      NewBinding([]string{"ctrl+t"}, "react"),
	ConfirmYes:    NewBinding([]string{"y"}, "yes"),
	ConfirmNo:     NewBinding([]string{"n", "esc"}, "no"),
	AddAccount:    NewBinding([]string{"a"}, "add account"),
	AttachFile:    NewBinding([]string{"ctrl+f"}, "attach file"),
	RenameChat:    NewBinding([]string{"r"}, "rename chat"),
	ToggleSidebar: NewBinding([]string{"ctrl+\\"}, "toggle chat list"),

	ListKeys:      list.DefaultKeyMap(),
	TextInputKeys: textinput.DefaultKeyMap(),
}

func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Quit, k.Back, k.Switch, k.SelectSend}
}

func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Quit, k.Back, k.Switch, k.ChatOpen, k.SelectSend},
		{k.MsgUp, k.MsgDown, k.DeleteMsg, k.YankMsg, k.EditMsg, k.ReplyMsg},
		{k.InfoMsg, k.OpenMsg, k.SaveMsg, k.ReactMsg},
		{k.AddAccount, k.AttachFile, k.RenameChat, k.ToggleSidebar},
		{k.ListKeys.Filter, k.ListKeys.ClearFilter},
	}
}

// helpHint returns a view-specific key hint for the app-wide footer, one
// "key desc" entry per binding joined by " · ". Key labels are pulled from
// the (possibly user-remapped) bindings; the descriptions are tailored to
// what each key does in that particular view.
func (k KeyMap) helpHint(view selectedView) string {
	// Use only the shortest bound key, not the full "ctrl+k/up"-style joined
	// label Help().Key would give — this stays compact even wrapped over
	// several lines; the full set of alternate keys is in FullHelp.
	//
	// Within an entry, join key and desc (and desc's own words) with a
	// non-breaking space instead of a regular one — wrapFooterHint word-wraps
	// this whole string, and a regular space would let it break an entry
	// across lines (e.g. "ctrl+k" on one line, "prev msg" on the next). Only
	// the " · " between entries is meant to be a wrap point.
	part := func(b key.Binding, desc string) string {
		keys := b.Keys()
		if len(keys) == 0 {
			return desc
		}
		shortest := keys[0]
		for _, k := range keys[1:] {
			if len(k) < len(shortest) {
				shortest = k
			}
		}
		return strings.Join(strings.Fields(caretKey(shortest)+" "+desc), " ")
	}

	switch view {
	case viewAccounts:
		return strings.Join([]string{
			part(k.MsgUp, "prev account"),
			part(k.MsgDown, "next account"),
			part(k.SelectSend, "select"),
			part(k.AddAccount, "add"),
			part(k.Switch, "chats"),
			part(k.Quit, "quit"),
		}, " · ")
	case viewChats:
		return strings.Join([]string{
			part(k.ListKeys.CursorUp, "up"),
			part(k.ListKeys.CursorDown, "down"),
			part(k.ChatOpen, "open"),
			part(k.RenameChat, "rename"),
			part(k.DeleteMsg, "delete"),
			part(k.ListKeys.Filter, "filter"),
			part(k.ToggleSidebar, "hide chats"),
			part(k.Switch, "accounts"),
			part(k.Quit, "quit"),
		}, " · ")
	case viewChat:
		// Ordered by how often each is used — least-used trail off the end
		// so narrow terminals still show the important ones first when
		// footerMaxLines caps how far this wraps. See KeyMap.FullHelp for
		// the complete, view-agnostic reference.
		//
		// Joined with " · " (spaces, not a bare "·") like the other views —
		// wrapFooterHint only breaks on regular spaces (see helpHint's nbsp
		// note), so a bare separator leaves it no break point between
		// entries and it hard-splits mid-word instead.
		return strings.Join([]string{
			part(k.SelectSend, "send"),
			part(k.ReplyMsg, "reply"),
			part(k.EditMsg, "edit"),
			part(k.DeleteMsg, "delete"),
			part(k.ReactMsg, "react"),
			part(k.YankMsg, "yank"),
			part(k.InfoMsg, "info"),
			part(k.OpenMsg, "open"),
			part(k.SaveMsg, "save"),
			part(k.AttachFile, "attach"),
			part(k.FocusChats, "chats"),
			part(k.ToggleSidebar, "hide list"),
			part(k.Back, "back"),
		}, " · ")
	default:
		return ""
	}
}
