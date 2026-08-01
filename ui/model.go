package ui

import (
	"time"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
)

type Message struct {
	Author  string
	Content string
	SentAt  time.Time
	IsMe    bool
	ReplyTo *int // index into the message slice; nil = not a reply
}

type Account struct {
	Name     string
	Chats    []list.Item
	Messages map[int][]Message
}

type Chat struct {
	Name        string
	Address     string
	LastMessage string
}

func (c Chat) Title() string { return c.Name }
func (c Chat) Description() string {
	switch {
	case c.LastMessage != "":
		return c.LastMessage
	case c.Address != "":
		return c.Address
	default:
		return ""
	}
}
func (c Chat) FilterValue() string { return c.Name }

// ── Focus state ───────────────────────────────────────────────────────────────

type selectedView int

const (
	viewAccounts selectedView = iota
	viewChats
	viewChat // viewport + input
)

type confirmTarget int

const (
	confirmNone confirmTarget = iota
	confirmDeleteMessage
	confirmDeleteChat
)

// ── Model ─────────────────────────────────────────────────────────────────────

type Model struct {
	width, height int
	selectedView
	keys   KeyMap
	theme  Theme
	styles uiStyles

	accounts       []Account
	currentAccount int
	chats          list.Model

	input    textinput.Model
	viewport viewport.Model

	// message interaction state
	selectedMsg   int // index of highlighted message (meaningful in viewViewport)
	editingMsgIdx int // >= 0 while editing a message; -1 otherwise
	replyToIdx    int // >= 0 while composing a reply; -1 otherwise
	confirmTarget confirmTarget
	msgOffsets    []int // line offset of each message inside viewport content
	noticeText    string
	noticeID      int
}

type noticeClearMsg struct {
	id int
}

func New(accounts []Account, keys KeyMap, theme Theme) Model {
	styles := newUIStyles(theme)
	delegate := newChatListDelegate(styles.colors)

	initialChats := []list.Item(nil)
	if len(accounts) > 0 {
		initialChats = accounts[0].Chats
	}
	l := list.New(initialChats, delegate, 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false) // superseded by the app-wide footer hint
	l.InfiniteScrolling = true
	applyChatListStyles(&l, styles.colors)

	ti := textinput.New()
	ti.Placeholder = "message..."
	ti.Prompt = "› "
	ti.KeyMap = keys.TextInputKeys
	ti.Focus()
	applyTextInputStyles(&ti, styles.colors)

	return Model{
		selectedView:   viewChat,
		keys:           keys,
		theme:          theme,
		styles:         styles,
		accounts:       accounts,
		currentAccount: 0,
		chats:          l,
		input:          ti,
		viewport:       viewport.New(),
		editingMsgIdx:  -1,
		replyToIdx:     -1,
	}
}

// ── Init ──────────────────────────────────────────────────────────────────────

func (m Model) Init() tea.Cmd {
	return textinput.Blink
}

// ── Sizing ────────────────────────────────────────────────────────────────────

func (m *Model) updateSizes() {
	sw := m.sidebarWidth()
	cw := m.chatAreaWidth()
	ih := m.inputAreaHeight()

	m.chats.SetHeight(max(0, m.height-sidebarStatusHeight))
	m.chats.SetWidth(sw)

	m.input.SetWidth(cw - 2) // -2 for Padding(0,1) on the input box
	m.viewport.SetWidth(cw)
	m.viewport.SetHeight(max(0, m.height-ih-chatStatusHeight))
}

func (m Model) sidebarWidth() int  { return m.width / 3 }
func (m Model) chatAreaWidth() int { return m.width - m.sidebarWidth() - 1 }

// inputAreaHeight accounts for the optional reply-hint line.
func (m Model) inputAreaHeight() int {
	if m.replyToIdx >= 0 {
		return 3 // top border + reply hint line + input line
	}
	return 2 // top border + input line
}
