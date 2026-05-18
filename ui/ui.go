package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/x/ansi"
)

// ── Data ──────────────────────────────────────────────────────────────────────

type Theme struct {
	AppBg       string
	PanelBg     string
	PanelAltBg  string
	PanelEdge   string
	LogBg       string
	ThemFg      string
	TextMuted   string
	Time        string
	BorderD     string
	BorderA     string
	AccentCyan  string
	ReplyFg     string
	PopupBg     string
	PopupDanger string
	FilterMatch string
	NickMe      string
	NickThem    string
	StatusFg    string
	NoticeBg    string
	NoticeFg    string
}

func DefaultTheme() Theme {
	return Theme{
		AppBg:       "#1a1b26",
		PanelBg:     "#1f2335",
		PanelAltBg:  "#24283b",
		PanelEdge:   "#292e42",
		LogBg:       "#1b1f2f",
		ThemFg:      "#c0caf5",
		TextMuted:   "#a9b1d6",
		Time:        "#565f89",
		BorderD:     "#3b4261",
		BorderA:     "#f7768e",
		AccentCyan:  "#7dcfff",
		ReplyFg:     "#73daca",
		PopupBg:     "#1f2335",
		PopupDanger: "#f7768e",
		FilterMatch: "#e0af68",
		NickMe:      "#7dcfff",
		NickThem:    "#bb9af7",
		StatusFg:    "#9ece6a",
		NoticeBg:    "#292e42",
		NoticeFg:    "#c0caf5",
	}
}

type Message struct {
	Author  string
	Content string
	SentAt  time.Time
	IsMe    bool
	ReplyTo *int // index into the message slice; nil = not a reply
}

type Chat struct {
	Name        string
	LastMessage string
}

func (c Chat) Title() string       { return c.Name }
func (c Chat) Description() string { return c.LastMessage }
func (c Chat) FilterValue() string { return c.Name }

// ── Key bindings ──────────────────────────────────────────────────────────────

type KeyMap struct {
	Quit          key.Binding
	Back          key.Binding
	Switch        key.Binding
	ChatOpen      key.Binding
	SelectSend    key.Binding
	MsgUp         key.Binding // k — navigate to previous message
	MsgDown       key.Binding // j — navigate to next message
	DeleteMsg     key.Binding // d — delete selected message (with popup)
	YankMsg       key.Binding // y — yank selected message
	EditMsg       key.Binding // e — edit (only last own message)
	ReplyMsg      key.Binding // r — reply to selected message
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
	ChatOpen:   NewBinding([]string{"l"}, "open chat"),
	SelectSend: NewBinding([]string{"enter"}, "select/send"),
	MsgUp:      NewBinding([]string{"k", "up"}, "prev msg"),
	MsgDown:    NewBinding([]string{"j", "down"}, "next msg"),
	DeleteMsg:  NewBinding([]string{"d"}, "delete"),
	YankMsg:    NewBinding([]string{"y"}, "yank"),
	EditMsg:    NewBinding([]string{"e"}, "edit (own last)"),
	ReplyMsg:   NewBinding([]string{"r"}, "reply"),
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

// ── Styles ────────────────────────────────────────────────────────────────────

const sidebarStatusHeight = 1

// ── Focus state ───────────────────────────────────────────────────────────────

type selectedView int

const (
	viewChats    selectedView = iota
	viewViewport              // scroll + message navigation
	viewInput                 // type and send / edit
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
	keys    KeyMap
	account string
	theme   Theme

	chats    list.Model
	messages map[int][]Message

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

func New(chatItems []list.Item, messages map[int][]Message, keys KeyMap, account string, theme Theme) Model {
	clrPanelBg := lipgloss.Color(theme.PanelBg)
	clrPanelEdge := lipgloss.Color(theme.PanelEdge)
	clrThemFg := lipgloss.Color(theme.ThemFg)
	clrTextMuted := lipgloss.Color(theme.TextMuted)
	clrTime := lipgloss.Color(theme.Time)
	clrBorderD := lipgloss.Color(theme.BorderD)
	clrBorderA := lipgloss.Color(theme.BorderA)
	clrAccentCyan := lipgloss.Color(theme.AccentCyan)
	clrFilterMatch := lipgloss.Color(theme.FilterMatch)
	delegate := list.NewDefaultDelegate()
	delegate.Styles.NormalTitle = delegate.Styles.NormalTitle.
		Foreground(clrThemFg).
		Background(clrPanelBg).
		PaddingLeft(1)
	delegate.Styles.NormalDesc = delegate.Styles.NormalDesc.
		Foreground(clrTextMuted).
		Background(clrPanelBg).
		PaddingLeft(1)
	delegate.Styles.SelectedTitle = delegate.Styles.SelectedTitle.
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(clrBorderA).
		Foreground(clrThemFg).
		Background(clrPanelEdge).
		Bold(true).
		PaddingLeft(1)
	delegate.Styles.SelectedDesc = delegate.Styles.SelectedDesc.
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(clrBorderA).
		Foreground(clrTextMuted).
		Background(clrPanelEdge).
		PaddingLeft(1)
	delegate.Styles.DimmedTitle = delegate.Styles.DimmedTitle.Foreground(clrTime)
	delegate.Styles.DimmedDesc = delegate.Styles.DimmedDesc.Foreground(clrTime)
	delegate.Styles.FilterMatch = delegate.Styles.FilterMatch.Foreground(clrFilterMatch).Bold(true)

	l := list.New(chatItems, delegate, 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.InfiniteScrolling = true
	l.Styles.HelpStyle = l.Styles.HelpStyle.Foreground(clrTime).Background(clrPanelBg)
	l.Styles.NoItems = l.Styles.NoItems.Foreground(clrTime).Background(clrPanelBg)
	l.Styles.PaginationStyle = l.Styles.PaginationStyle.Foreground(clrTime).Background(clrPanelBg)
	l.Styles.DefaultFilterCharacterMatch = l.Styles.DefaultFilterCharacterMatch.Foreground(clrFilterMatch).Bold(true)
	filterStyles := l.Styles.Filter
	filterStyles.Focused.Prompt = filterStyles.Focused.Prompt.Foreground(clrAccentCyan).Background(clrPanelBg)
	filterStyles.Focused.Text = filterStyles.Focused.Text.Foreground(clrThemFg).Background(clrPanelBg)
	filterStyles.Focused.Placeholder = filterStyles.Focused.Placeholder.Foreground(clrTime).Background(clrPanelBg)
	filterStyles.Blurred.Prompt = filterStyles.Blurred.Prompt.Foreground(clrAccentCyan).Background(clrPanelBg)
	filterStyles.Blurred.Text = filterStyles.Blurred.Text.Foreground(clrThemFg).Background(clrPanelBg)
	filterStyles.Blurred.Placeholder = filterStyles.Blurred.Placeholder.Foreground(clrTime).Background(clrPanelBg)
	filterStyles.Cursor.Color = clrAccentCyan
	l.Styles.Filter = filterStyles

	ti := textinput.New()
	ti.Placeholder = "message..."
	ti.Prompt = "› "
	ti.KeyMap = keys.TextInputKeys
	ti.Focus()
	tiStyles := ti.Styles()
	tiStyles.Focused.Prompt = tiStyles.Focused.Prompt.Foreground(clrAccentCyan)
	tiStyles.Focused.Text = tiStyles.Focused.Text.Foreground(clrThemFg)
	tiStyles.Focused.Placeholder = tiStyles.Focused.Placeholder.Foreground(clrTime)
	tiStyles.Blurred.Prompt = tiStyles.Blurred.Prompt.Foreground(clrBorderD)
	tiStyles.Blurred.Text = tiStyles.Blurred.Text.Foreground(clrTextMuted)
	tiStyles.Blurred.Placeholder = tiStyles.Blurred.Placeholder.Foreground(clrTime)
	tiStyles.Cursor.Color = clrAccentCyan
	ti.SetStyles(tiStyles)

	return Model{
		selectedView:  viewInput,
		keys:          keys,
		account:       account,
		theme:         theme,
		chats:         l,
		messages:      messages,
		input:         ti,
		viewport:      viewport.New(),
		editingMsgIdx: -1,
		replyToIdx:    -1,
	}
}

// ── Init ──────────────────────────────────────────────────────────────────────

func (m Model) Init() tea.Cmd {
	return textinput.Blink
}

// ── Update ────────────────────────────────────────────────────────────────────

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.updateSizes()
		m.refreshViewport()
		m.viewport.GotoBottom()
		return m, nil

	case noticeClearMsg:
		if msg.id == m.noticeID {
			m.noticeText = ""
		}
		return m, nil

	case tea.KeyMsg:
		// ── Delete confirmation popup intercepts all input ─────────────────
		if m.confirmTarget != confirmNone {
			switch {
			case key.Matches(msg, m.keys.ConfirmYes):
				if m.confirmTarget == confirmDeleteMessage {
					m.deleteSelectedMsg()
				} else if m.confirmTarget == confirmDeleteChat {
					cmds = append(cmds, m.deleteSelectedChat())
				}
				m.confirmTarget = confirmNone
				m.refreshViewport()
				return m, tea.Batch(cmds...)
			case key.Matches(msg, m.keys.ConfirmNo):
				m.confirmTarget = confirmNone
			}
			return m, nil
		}

		switch {

		// ── Global ────────────────────────────────────────────────────────
		case key.Matches(msg, m.keys.Quit):
			if msg.String() == "ctrl+c" || m.selectedView != viewInput {
				return m, tea.Quit
			}

		case key.Matches(msg, m.keys.Back):
			if m.selectedView != viewChats {
				m.cancelPending()
				m.selectedView = viewChats
				m.input.Blur()
				return m, nil
			}

		case key.Matches(msg, m.keys.ChatOpen):
			if m.selectedView == viewChats {
				return m.openCurrentChat()
			}

		case key.Matches(msg, m.keys.SelectSend):
			switch m.selectedView {
			case viewChats:
				return m.openCurrentChat()
			case viewInput:
				text := strings.TrimSpace(m.input.Value())
				if text == "" {
					return m, nil
				}
				chatIdx := m.currentChatIndex()
				if chatIdx < 0 {
					return m, nil
				}

				if m.editingMsgIdx >= 0 {
					// Apply edit in-place.
					msgs := m.messages[chatIdx]
					if m.editingMsgIdx < len(msgs) {
						msgs[m.editingMsgIdx].Content = text
					}
					m.editingMsgIdx = -1
					m.input.Placeholder = "message..."
				} else {
					// Send new message, optionally quoting a reply.
					newMsg := Message{
						Author:  "me",
						Content: text,
						SentAt:  time.Now(),
						IsMe:    true,
					}
					if m.replyToIdx >= 0 {
						rt := m.replyToIdx
						newMsg.ReplyTo = &rt
						m.replyToIdx = -1
					}
					m.messages[chatIdx] = append(m.messages[chatIdx], newMsg)
				}

				m.input.SetValue("")
				m.updateSizes()
				m.refreshViewport()
				m.viewport.GotoBottom()
				cmds = append(cmds, m.showNotification("message sent"))
				return m, tea.Batch(cmds...)
			}

		case key.Matches(msg, m.keys.Switch):
			switch m.selectedView {
			case viewViewport:
				m.selectedView = viewInput
				cmds = append(cmds, m.input.Focus())
				return m, tea.Batch(cmds...)
			case viewInput:
				m.selectedView = viewViewport
				m.input.Blur()
				return m, nil
			}

		// ── Message navigation (viewport only) ────────────────────────────
		case key.Matches(msg, m.keys.MsgUp):
			if m.selectedView == viewViewport && m.selectedMsg > 0 {
				m.selectedMsg--
				m.refreshViewportScrollTo(m.selectedMsg)
				return m, nil
			}

		case key.Matches(msg, m.keys.MsgDown):
			if m.selectedView == viewViewport {
				chatIdx := m.currentChatIndex()
				if chatIdx < 0 {
					return m, nil
				}
				if m.selectedMsg < len(m.messages[chatIdx])-1 {
					m.selectedMsg++
					m.refreshViewportScrollTo(m.selectedMsg)
					return m, nil
				}
			}

		// ── Message actions (viewport only) ───────────────────────────────
		case key.Matches(msg, m.keys.DeleteMsg):
			if m.selectedView == viewViewport {
				chatIdx := m.currentChatIndex()
				if chatIdx < 0 {
					return m, nil
				}
				if len(m.messages[chatIdx]) > 0 {
					m.confirmTarget = confirmDeleteMessage
				}
				return m, nil
			}
			if m.selectedView == viewChats && m.currentChatIndex() >= 0 {
				m.confirmTarget = confirmDeleteChat
				return m, nil
			}

		case key.Matches(msg, m.keys.YankMsg):
			if m.selectedView == viewViewport {
				if err := m.yankSelectedMsg(); err != nil {
					cmds = append(cmds, m.showNotification("copy failed"))
				} else {
					cmds = append(cmds, m.showNotification("message copied"))
				}
				return m, tea.Batch(cmds...)
			}

		case key.Matches(msg, m.keys.EditMsg):
			if m.selectedView == viewViewport {
				chatIdx := m.currentChatIndex()
				if chatIdx < 0 {
					return m, nil
				}
				msgs := m.messages[chatIdx]
				if m.canEdit(msgs) {
					m.editingMsgIdx = m.selectedMsg
					m.input.SetValue(msgs[m.selectedMsg].Content)
					m.input.Placeholder = "edit message..."
					m.selectedView = viewInput
					cmds = append(cmds, m.input.Focus())
					return m, tea.Batch(cmds...)
				}
			}

		case key.Matches(msg, m.keys.ReplyMsg):
			if m.selectedView == viewViewport {
				chatIdx := m.currentChatIndex()
				if chatIdx < 0 {
					return m, nil
				}
				if len(m.messages[chatIdx]) > 0 {
					m.replyToIdx = m.selectedMsg
					m.selectedView = viewInput
					m.updateSizes()
					m.refreshViewport()
					cmds = append(cmds, m.input.Focus())
					return m, tea.Batch(cmds...)
				}
			}
		}
	}

	// Route remaining events to the focused component.
	var cmd tea.Cmd
	switch m.selectedView {
	case viewChats:
		prev := m.chats.Index()
		m.chats, cmd = m.chats.Update(msg)
		cmds = append(cmds, cmd)
		if m.chats.Index() != prev {
			chatIdx := m.currentChatIndex()
			if chatIdx < 0 {
				m.selectedMsg = 0
				m.refreshViewport()
				break
			}
			msgs := m.messages[chatIdx]
			if len(msgs) > 0 {
				m.selectedMsg = len(msgs) - 1
			} else {
				m.selectedMsg = 0
			}
			m.refreshViewport()
			m.viewport.GotoBottom()
		}
	case viewViewport:
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
	case viewInput:
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (m Model) currentChatIndex() int {
	idx := m.chats.GlobalIndex()
	if idx < 0 || idx >= len(m.chats.Items()) {
		return -1
	}
	return idx
}

func (m *Model) showNotification(text string) tea.Cmd {
	m.noticeID++
	m.noticeText = text
	id := m.noticeID
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		return noticeClearMsg{id: id}
	})
}

func (m *Model) openCurrentChat() (tea.Model, tea.Cmd) {
	if m.currentChatIndex() < 0 {
		return m, nil
	}
	m.selectedView = viewViewport
	m.input.Blur()
	chatIdx := m.currentChatIndex()
	if msgs := m.messages[chatIdx]; len(msgs) > 0 {
		m.selectedMsg = len(msgs) - 1
	}
	m.refreshViewport()
	return m, nil
}

// canEdit returns true only when selectedMsg is the last "IsMe" message.
func (m Model) canEdit(msgs []Message) bool {
	if m.selectedMsg < 0 || m.selectedMsg >= len(msgs) {
		return false
	}
	if !msgs[m.selectedMsg].IsMe {
		return false
	}
	for i := m.selectedMsg + 1; i < len(msgs); i++ {
		if msgs[i].IsMe {
			return false
		}
	}
	return true
}

// deleteSelectedMsg removes the current message and fixes up ReplyTo indices.
func (m *Model) deleteSelectedMsg() {
	chatIdx := m.currentChatIndex()
	if chatIdx < 0 {
		return
	}
	msgs := m.messages[chatIdx]
	if m.selectedMsg < 0 || m.selectedMsg >= len(msgs) {
		return
	}
	del := m.selectedMsg
	newMsgs := make([]Message, 0, len(msgs)-1)
	for i, msg := range msgs {
		if i == del {
			continue
		}
		if msg.ReplyTo != nil {
			switch {
			case *msg.ReplyTo == del:
				// reply target is gone
				msg.ReplyTo = nil
			case *msg.ReplyTo > del:
				adj := *msg.ReplyTo - 1
				msg.ReplyTo = &adj
			}
		}
		newMsgs = append(newMsgs, msg)
	}
	m.messages[chatIdx] = newMsgs
	if m.selectedMsg >= len(newMsgs) && len(newMsgs) > 0 {
		m.selectedMsg = len(newMsgs) - 1
	}
}

func (m Model) yankSelectedMsg() error {
	chatIdx := m.currentChatIndex()
	if chatIdx < 0 {
		return nil
	}
	msgs := m.messages[chatIdx]
	if m.selectedMsg < 0 || m.selectedMsg >= len(msgs) {
		return nil
	}
	return clipboard.WriteAll(msgs[m.selectedMsg].Content)
}

func (m *Model) deleteSelectedChat() tea.Cmd {
	chatIdx := m.currentChatIndex()
	if chatIdx < 0 {
		return nil
	}

	items := m.chats.Items()
	newItems := make([]list.Item, 0, len(items)-1)
	newItems = append(newItems, items[:chatIdx]...)
	newItems = append(newItems, items[chatIdx+1:]...)

	newMessages := make(map[int][]Message, len(newItems))
	for i := 0; i < len(items); i++ {
		switch {
		case i < chatIdx:
			newMessages[i] = m.messages[i]
		case i > chatIdx:
			newMessages[i-1] = m.messages[i]
		}
	}
	m.messages = newMessages

	cmd := m.chats.SetItems(newItems)
	if len(newItems) == 0 {
		m.selectedView = viewChats
		m.selectedMsg = 0
		m.cancelPending()
		m.refreshViewport()
		return cmd
	}

	if chatIdx >= len(newItems) {
		chatIdx = len(newItems) - 1
	}
	m.chats.Select(chatIdx)
	msgs := m.messages[chatIdx]
	if len(msgs) > 0 {
		m.selectedMsg = len(msgs) - 1
	} else {
		m.selectedMsg = 0
	}
	m.cancelPending()
	m.refreshViewport()
	m.viewport.GotoBottom()
	return cmd
}

// cancelPending clears any in-progress edit or reply.
func (m *Model) cancelPending() {
	m.editingMsgIdx = -1
	m.replyToIdx = -1
	m.input.SetValue("")
	m.input.Placeholder = "message..."
	m.updateSizes()
}

// refreshViewport re-renders all messages and updates the viewport content.
func (m *Model) refreshViewport() {
	if m.currentChatIndex() < 0 {
		m.msgOffsets = nil
		m.viewport.SetContent("")
		return
	}
	content, offsets := m.renderMessagesWithOffsets()
	m.msgOffsets = offsets
	m.viewport.SetContent(content)
}

// refreshViewportScrollTo re-renders and scrolls so msgIdx is visible.
func (m *Model) refreshViewportScrollTo(msgIdx int) {
	m.refreshViewport()
	if msgIdx >= 0 && msgIdx < len(m.msgOffsets) {
		m.viewport.SetYOffset(m.msgOffsets[msgIdx])
	}
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
	m.viewport.SetHeight(m.height - ih)
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

// ── Message rendering ─────────────────────────────────────────────────────────

// renderMessagesWithOffsets renders all messages for the active chat and
// returns the full string plus the line-offset of each message within it.
func (m Model) renderMessagesWithOffsets() (string, []int) {
	cw := m.chatAreaWidth()
	if cw <= 10 {
		return "", nil
	}

	chatIdx := m.currentChatIndex()
	if chatIdx < 0 {
		return "", nil
	}
	msgs := m.messages[chatIdx]
	offsets := make([]int, len(msgs))

	var sb strings.Builder
	currentLine := 0

	for i, msg := range msgs {
		if i > 0 {
			sb.WriteByte('\n')
			currentLine++
		}
		offsets[i] = currentLine
		rendered := m.renderMessage(msg, i, cw, msgs)
		sb.WriteString(rendered)
		currentLine += strings.Count(rendered, "\n") + 1
	}
	return sb.String(), offsets
}

func (m Model) renderMessage(msg Message, msgIdx, totalWidth int, allMsgs []Message) string {
	isSelected := m.selectedView == viewViewport && msgIdx == m.selectedMsg
	clrBorderA := lipgloss.Color(m.theme.BorderA)
	clrPanelEdge := lipgloss.Color(m.theme.PanelEdge)
	clrNickThem := lipgloss.Color(m.theme.NickThem)
	clrNickMe := lipgloss.Color(m.theme.NickMe)
	stTime := lipgloss.NewStyle().Foreground(lipgloss.Color(m.theme.Time))
	stReply := lipgloss.NewStyle().Foreground(lipgloss.Color(m.theme.ReplyFg)).Italic(true)
	prefix := "  "
	if isSelected {
		prefix = lipgloss.NewStyle().Foreground(clrBorderA).Render("> ")
	}

	nick := msg.Author
	nickStyle := lipgloss.NewStyle().Foreground(clrNickThem)
	if msg.IsMe {
		nickStyle = lipgloss.NewStyle().Foreground(clrNickMe)
	}

	timeLabel := msg.SentAt.Format("15:04")
	if !sameDay(msg.SentAt, time.Now()) {
		timeLabel = msg.SentAt.Format("2006-01-02 15:04")
	}
	headerPlain := fmt.Sprintf("[%s] <%s> ", timeLabel, nick)
	header := stTime.Render("["+timeLabel+"]") + " " + nickStyle.Render("<"+nick+">") + " "
	indent := strings.Repeat(" ", lipgloss.Width(headerPlain))
	wrapWidth := totalWidth - lipgloss.Width(prefix) - lipgloss.Width(indent)
	if wrapWidth < 8 {
		wrapWidth = 8
	}

	var lines []string
	if msg.ReplyTo != nil {
		reply := m.replyPreview(*msg.ReplyTo, allMsgs)
		replyWrapped := strings.Split(ansi.Wrap(reply, max(8, totalWidth-lipgloss.Width(prefix)-2), " "), "\n")
		for _, line := range replyWrapped {
			lines = append(lines, prefix+stReply.Render(line))
			prefix = "  "
		}
	}

	bodyLines := strings.Split(ansi.Wrap(msg.Content, wrapWidth, " "), "\n")
	for i, line := range bodyLines {
		if i == 0 {
			lines = append(lines, prefix+header+line)
			continue
		}
		lines = append(lines, "  "+indent+line)
	}

	block := strings.Join(lines, "\n")
	if !isSelected {
		return block
	}
	return lipgloss.NewStyle().
		Background(clrPanelEdge).
		Width(totalWidth).
		Render(block)
}

func (m Model) replyPreview(idx int, allMsgs []Message) string {
	if idx < 0 || idx >= len(allMsgs) {
		return ""
	}
	orig := allMsgs[idx]
	preview := strings.ReplaceAll(orig.Content, "\n", " ")
	runes := []rune(preview)
	if len(runes) > 30 {
		preview = string(runes[:27]) + "…"
	}
	return fmt.Sprintf("↪ %s: %s", orig.Author, preview)
}

func spaces(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(" ", n)
}

// ── View ──────────────────────────────────────────────────────────────────────

func (m Model) View() tea.View {
	if m.width == 0 {
		v := tea.NewView("loading...")
		v.AltScreen = true
		return v
	}

	clrAppBg := lipgloss.Color(m.theme.AppBg)
	clrPanelBg := lipgloss.Color(m.theme.PanelBg)
	clrPanelAltBg := lipgloss.Color(m.theme.PanelAltBg)
	clrPanelEdge := lipgloss.Color(m.theme.PanelEdge)
	clrLogBg := lipgloss.Color(m.theme.LogBg)
	clrThemFg := lipgloss.Color(m.theme.ThemFg)
	clrBorderD := lipgloss.Color(m.theme.BorderD)
	clrBorderA := lipgloss.Color(m.theme.BorderA)
	clrAccentCyan := lipgloss.Color(m.theme.AccentCyan)
	clrStatusFg := lipgloss.Color(m.theme.StatusFg)
	clrNoticeBg := lipgloss.Color(m.theme.NoticeBg)
	clrNoticeFg := lipgloss.Color(m.theme.NoticeFg)
	stReply := lipgloss.NewStyle().Foreground(lipgloss.Color(m.theme.ReplyFg)).Italic(true)
	sw := m.sidebarWidth()

	// ── Sidebar ────────────────────────────────────────────────────────────
	sidebarBorder := clrBorderD
	if m.selectedView == viewChats {
		sidebarBorder = clrBorderA
	}
	statusLine := lipgloss.NewStyle().
		Width(sw).
		Background(clrPanelEdge).
		Foreground(clrStatusFg).
		Bold(true).
		Padding(0, 1).
		Render("account: " + m.account)
	sidebarInner := lipgloss.JoinVertical(lipgloss.Left,
		statusLine,
		lipgloss.NewStyle().
			Width(sw).
			Height(max(0, m.height-sidebarStatusHeight)).
			Background(clrPanelBg).
			Foreground(clrThemFg).
			Render(m.chats.View()),
	)
	sidebar := lipgloss.NewStyle().
		Width(sw).
		Height(m.height).
		Background(clrPanelBg).
		Foreground(clrThemFg).
		Border(lipgloss.NormalBorder(), false, true, false, false).
		BorderForeground(sidebarBorder).
		Render(sidebarInner)

	// ── Input box ──────────────────────────────────────────────────────────
	inputBorder := clrBorderD
	if m.selectedView == viewInput {
		inputBorder = clrAccentCyan
	}

	var inputInner string
	if m.replyToIdx >= 0 {
		chatIdx := m.currentChatIndex()
		if chatIdx >= 0 {
			msgs := m.messages[chatIdx]
			if m.replyToIdx < len(msgs) {
				orig := msgs[m.replyToIdx]
				preview := strings.ReplaceAll(orig.Content, "\n", " ")
				runes := []rune(preview)
				if len(runes) > 40 {
					preview = string(runes[:37]) + "…"
				}
				hint := stReply.Render(fmt.Sprintf("↩ %s: %s", orig.Author, preview))
				inputInner = hint + "\n" + m.input.View()
			} else {
				inputInner = m.input.View()
			}
		} else {
			inputInner = m.input.View()
		}
	} else {
		inputInner = m.input.View()
	}

	inputBox := lipgloss.NewStyle().
		Background(clrPanelAltBg).
		Foreground(clrThemFg).
		Border(lipgloss.NormalBorder(), true, false, false, false).
		BorderForeground(inputBorder).
		Padding(0, 1).
		Render(inputInner)

	// ── Viewport / popup ───────────────────────────────────────────────────
	var viewportArea string
	if m.confirmTarget != confirmNone {
		viewportArea = m.renderDeletePopup()
	} else {
		viewportHeight := m.height - m.inputAreaHeight()
		contentHeight := viewportHeight
		if m.noticeText != "" && contentHeight > 1 {
			contentHeight--
		}
		viewportBody := lipgloss.NewStyle().
			Background(clrLogBg).
			Foreground(clrThemFg).
			Width(m.chatAreaWidth()).
			Height(contentHeight).
			Render(m.viewport.View())
		if m.noticeText != "" {
			notice := lipgloss.NewStyle().
				Background(clrNoticeBg).
				Foreground(clrNoticeFg).
				Width(m.chatAreaWidth()).
				Padding(0, 1).
				Render(m.noticeText)
			viewportBody = lipgloss.JoinVertical(lipgloss.Left, viewportBody, notice)
		}
		viewportArea = lipgloss.NewStyle().
			Width(m.chatAreaWidth()).
			Height(viewportHeight).
			Background(clrAppBg).
			Foreground(clrThemFg).
			Render(viewportBody)
	}

	chatArea := lipgloss.JoinVertical(lipgloss.Left, viewportArea, inputBox)

	root := lipgloss.NewStyle().
		Background(clrAppBg).
		Foreground(clrThemFg).
		Render(lipgloss.JoinHorizontal(lipgloss.Top, sidebar, chatArea))

	v := tea.NewView(root)
	v.AltScreen = true
	return v
}

// renderDeletePopup renders a centered confirmation dialog inside the viewport
// area instead of overlaying raw ANSI (simpler and more portable).
func (m Model) renderDeletePopup() string {
	clrBorderA := lipgloss.Color(m.theme.BorderA)
	clrPopupBg := lipgloss.Color(m.theme.PopupBg)
	clrThemFg := lipgloss.Color(m.theme.ThemFg)
	cw := m.chatAreaWidth()
	vh := m.height - m.inputAreaHeight()

	popup := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(clrBorderA).
		Background(clrPopupBg).
		Foreground(clrThemFg).
		Padding(1, 4).
		Render(m.deletePrompt())

	return lipgloss.Place(cw, vh, lipgloss.Center, lipgloss.Center, popup)
}
func (m Model) deletePrompt() string {
	clrPopupDanger := lipgloss.Color(m.theme.PopupDanger)
	switch m.confirmTarget {
	case confirmDeleteChat:
		return lipgloss.NewStyle().Foreground(clrPopupDanger).Bold(true).Render("Leave chat?") + "\n\n  [y] yes    [n] no"
	default:
		return lipgloss.NewStyle().Foreground(clrPopupDanger).Bold(true).Render("Delete message?") + "\n\n  [y] yes    [n] no"
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
