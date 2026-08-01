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
