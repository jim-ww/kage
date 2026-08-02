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
	ConfirmYes    key.Binding // y — confirm popup
	ConfirmNo     key.Binding // n / esc — cancel popup
	ListKeys      list.KeyMap
	TextInputKeys textinput.KeyMap
}

func NewBinding(keys []string, desc string) key.Binding {
	return key.NewBinding(key.WithKeys(keys...), key.WithHelp(strings.Join(keys, "/"), desc))
}

var DefaultKeyMap = KeyMap{
	Quit:       NewBinding([]string{"q", "ctrl+c"}, "quit"),
	Back:       NewBinding([]string{"esc"}, "back to chats"),
	Switch:     NewBinding([]string{"tab"}, "switch focus"),
	FocusChats: NewBinding([]string{"\\"}, "focus chats"),
	ChatOpen:   NewBinding([]string{"l", "right"}, "open chat"),
	SelectSend: NewBinding([]string{"enter"}, "select/send"),
	MsgUp:      NewBinding([]string{"ctrl+k", "up"}, "prev msg"),
	MsgDown:    NewBinding([]string{"ctrl+j", "down"}, "next msg"),
	DeleteMsg:  NewBinding([]string{"ctrl+d"}, "delete"),
	YankMsg:    NewBinding([]string{"ctrl+y"}, "yank"),
	EditMsg:    NewBinding([]string{"ctrl+e"}, "edit (own last)"),
	ReplyMsg:   NewBinding([]string{"ctrl+r"}, "reply"),
	InfoMsg:    NewBinding([]string{"ctrl+g"}, "message info"),
	OpenMsg:    NewBinding([]string{"ctrl+o"}, "open links/attachments"),
	ConfirmYes: NewBinding([]string{"y"}, "yes"),
	ConfirmNo:  NewBinding([]string{"n", "esc"}, "no"),

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
		{k.ListKeys.Filter, k.ListKeys.ClearFilter},
	}
}

// helpHint returns a compact, view-specific key hint for the app-wide footer.
// Key labels are pulled from the (possibly user-remapped) bindings; the
// descriptions are tailored to what each key does in that particular view.
func (k KeyMap) helpHint(view selectedView) string {
	part := func(b key.Binding, desc string) string {
		return b.Help().Key + " " + desc
	}

	switch view {
	case viewAccounts:
		return strings.Join([]string{
			part(k.MsgUp, "prev account"),
			part(k.MsgDown, "next account"),
			part(k.SelectSend, "select"),
			part(k.Switch, "chats"),
			part(k.Quit, "quit"),
		}, " · ")
	case viewChats:
		return strings.Join([]string{
			part(k.ListKeys.CursorUp, "up"),
			part(k.ListKeys.CursorDown, "down"),
			part(k.ChatOpen, "open"),
			part(k.DeleteMsg, "delete"),
			part(k.Switch, "accounts"),
			part(k.Quit, "quit"),
		}, " · ")
	case viewChat:
		return strings.Join([]string{
			part(k.SelectSend, "send"),
			part(k.MsgUp, "up"),
			part(k.MsgDown, "down"),
			part(k.ReplyMsg, "reply"),
			part(k.EditMsg, "edit"),
			part(k.DeleteMsg, "delete"),
			part(k.YankMsg, "yank"),
			part(k.InfoMsg, "info"),
			part(k.OpenMsg, "open"),
			part(k.Back, "back"),
		}, " · ")
	default:
		return ""
	}
}
