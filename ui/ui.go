package ui

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ── Data ──────────────────────────────────────────────────────────────────────

type Message struct {
	Author  string
	Content string
	SentAt  time.Time
	IsMe    bool
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
	Back          key.Binding // Esc -> chats
	Switch        key.Binding // Tab between viewport/input
	SelectSend    key.Binding
	ListKeys      list.KeyMap
	TextInputKeys textinput.KeyMap
}

var DefaultKeyMap = KeyMap{
	Quit:       key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q/ctrl+c", "quit")),
	Back:       key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back to chats")),
	Switch:     key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "switch viewport/input")),
	SelectSend: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select/send")),

	ListKeys: list.DefaultKeyMap(),

	TextInputKeys: textinput.DefaultKeyMap(),
	// textinput.KeyMap{
	// 	CharacterForward:        key.NewBinding(key.WithKeys("right", "ctrl+f")),
	// 	CharacterBackward:       key.NewBinding(key.WithKeys("left", "ctrl+b")),
	// 	WordForward:             key.NewBinding(key.WithKeys("alt+right", "ctrl+right", "alt+f")),
	// 	WordBackward:            key.NewBinding(key.WithKeys("alt+left", "ctrl+left", "alt+b")),
	// 	DeleteWordBackward:      key.NewBinding(key.WithKeys("alt+backspace", "ctrl+w")),
	// 	DeleteWordForward:       key.NewBinding(key.WithKeys("alt+delete", "alt+d")),
	// 	DeleteAfterCursor:       key.NewBinding(key.WithKeys("ctrl+k")),
	// 	DeleteBeforeCursor:      key.NewBinding(key.WithKeys("ctrl+u")),
	// 	DeleteCharacterBackward: key.NewBinding(key.WithKeys("backspace", "ctrl+h")),
	// 	DeleteCharacterForward:  key.NewBinding(key.WithKeys("delete", "ctrl+d")),
	// 	LineStart:               key.NewBinding(key.WithKeys("home", "ctrl+a")),
	// 	LineEnd:                 key.NewBinding(key.WithKeys("end", "ctrl+e")),
	// 	Paste:                   key.NewBinding(key.WithKeys("ctrl+v")),
	// 	AcceptSuggestion:        key.NewBinding(key.WithKeys("right", "ctrl+tab"), key.WithHelp("→", "autocomplete")),
	// 	NextSuggestion:          key.NewBinding(key.WithKeys("down", "ctrl+n")),
	// 	PrevSuggestion:          key.NewBinding(key.WithKeys("up", "ctrl+p")),
	// },
}

func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Quit, k.Back, k.Switch, k.SelectSend}
}

func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Quit, k.Back, k.Switch, k.SelectSend},
		{k.ListKeys.Filter, k.ListKeys.ClearFilter},
	}
}

// ── Styles ────────────────────────────────────────────────────────────────────

var (
	clrMeBg    = lipgloss.Color("#1a3a5c")
	clrMeFg    = lipgloss.Color("#a8d4f5")
	clrThemBg  = lipgloss.Color("#2d2d2d")
	clrThemFg  = lipgloss.Color("#c8c8c8")
	clrTime    = lipgloss.Color("#555555")
	clrBorderD = lipgloss.Color("#333333")
	clrBorderA = lipgloss.Color("#4fc3f7")

	// stBubble is a value type — chaining .Background(...).Render() never
	// mutates it, so it is safe to use as a shared base.
	// TODO: fix border transparent gap
	stBubble = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Padding(0, 1)

	stTime = lipgloss.NewStyle().Foreground(clrTime)
)

// ── Focus state ───────────────────────────────────────────────────────────────

type selectedView int

const (
	viewChats    selectedView = iota
	viewViewport              // scroll without typing
	viewInput                 // type and send
)

// inputHeight = 1 top-border line + 1 text-input line.
const inputHeight = 2

// ── Model ─────────────────────────────────────────────────────────────────────

type Model struct {
	width, height int
	selectedView
	keys KeyMap

	chats    list.Model
	messages map[int][]Message

	input    textinput.Model
	viewport viewport.Model
}

func New(chatItems []list.Item, messages map[int][]Message) Model {
	l := list.New(chatItems, list.NewDefaultDelegate(), 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.InfiniteScrolling = true

	ti := textinput.New()
	ti.Placeholder = "message..."
	ti.KeyMap = DefaultKeyMap.TextInputKeys
	ti.Focus()

	return Model{
		selectedView: viewInput,
		keys:         DefaultKeyMap,
		chats:        l,
		messages:     messages,
		input:        ti,
		viewport:     viewport.New(),
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
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		return m, nil

	case tea.KeyMsg:
		switch {

		// Quit: ctrl+c always works; "q" only when not typing.
		case key.Matches(msg, m.keys.Quit):
			if msg.String() == "ctrl+c" || m.selectedView != viewInput {
				return m, tea.Quit
			}

		case key.Matches(msg, m.keys.Back):
			if m.selectedView != viewChats {
				m.selectedView = viewChats
				m.input.Blur()
				return m, nil
			}

		case key.Matches(msg, m.keys.SelectSend):
			switch m.selectedView {
			case viewChats:
				m.selectedView = viewViewport
				m.input.Blur()
				m.viewport.SetContent(m.renderMessages())
				m.viewport.GotoBottom()
				return m, nil
			case viewInput:
				text := strings.TrimSpace(m.input.Value())
				if text != "" {
					idx := m.chats.Index()
					m.messages[idx] = append(m.messages[idx], Message{
						Author:  "me",
						Content: text,
						SentAt:  time.Now(),
						IsMe:    true,
					})
					m.input.SetValue("")
					m.viewport.SetContent(m.renderMessages())
					m.viewport.GotoBottom()
				}
				return m, nil
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

		}
	}

	// Route all other events to the currently focused component.
	var cmd tea.Cmd
	switch m.selectedView {
	case viewChats:
		prev := m.chats.Index()
		m.chats, cmd = m.chats.Update(msg)
		cmds = append(cmds, cmd)
		if m.chats.Index() != prev {
			m.viewport.SetContent(m.renderMessages())
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

// ── Sizing ────────────────────────────────────────────────────────────────────

func (m *Model) updateSizes() {
	sw := m.sidebarWidth()
	cw := m.chatAreaWidth()

	m.chats.SetHeight(m.height)
	m.chats.SetWidth(sw)

	// Subtract 2 to compensate for the Padding(0,1) applied in View.
	m.input.SetWidth(cw - 2)
	m.viewport.SetWidth(cw)
	m.viewport.SetHeight(m.height - inputHeight)
}

func (m Model) sidebarWidth() int  { return m.width / 3 }
func (m Model) chatAreaWidth() int { return m.width - m.sidebarWidth() - 1 }

// ── Bubble rendering ──────────────────────────────────────────────────────────

func (m Model) renderMessages() string {
	cw := m.chatAreaWidth()
	if cw <= 10 {
		return ""
	}
	maxBubble := cw * 3 / 5 // bubbles fill at most 60 % of the chat width

	msgs := m.messages[m.chats.Index()]
	var sb strings.Builder
	for i, msg := range msgs {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(renderBubble(msg, maxBubble, cw))
	}
	return sb.String()
}

// TODO: fix long messages (wrap)
// renderBubble returns a two-line block: the bubble on line 1, the timestamp
// on line 2.  Outgoing ("me") messages are right-aligned; incoming are left.
func renderBubble(msg Message, maxWidth, totalWidth int) string {
	ts := stTime.Render(msg.SentAt.Format("15:04"))

	if msg.IsMe {
		bubble := stBubble.
			Background(clrMeBg).
			Foreground(clrMeFg).
			BorderForeground(clrMeBg).
			MaxWidth(maxWidth).
			Render(msg.Content)

		tw := lipgloss.Width(ts)
		tsLine := spaces(totalWidth-tw-2) + ts

		// rightAlignBlock pads every line of the multi-line bubble string so
		// that each line ends at the same column.  Without this, only the first
		// line receives the padding; the remaining lines (border top/bottom)
		// render at column 0 — which is the visual bug shown in the screenshot.
		return rightAlignBlock(bubble, totalWidth) + "\n" + tsLine
	}

	bubble := stBubble.
		Background(clrThemBg).
		Foreground(clrThemFg).
		BorderForeground(clrThemBg).
		MaxWidth(maxWidth).
		Render(msg.Content)

	return bubble + "\n" + "  " + ts
}

// rightAlignBlock shifts every line of a multi-line string to the right so
// that the widest line ends at totalWidth-2 (leaving a small right margin).
func rightAlignBlock(block string, totalWidth int) string {
	lines := strings.Split(block, "\n")
	var sb strings.Builder
	for i, line := range lines {
		if i > 0 {
			sb.WriteByte('\n')
		}
		lw := lipgloss.Width(line)
		if p := totalWidth - lw - 2; p > 0 {
			sb.WriteString(strings.Repeat(" ", p))
		}
		sb.WriteString(line)
	}
	return sb.String()
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

	sw := m.sidebarWidth()

	// Sidebar — highlight border when focused.
	sidebarBorder := clrBorderD
	if m.selectedView == viewChats {
		sidebarBorder = clrBorderA
	}
	sidebar := lipgloss.NewStyle().
		Width(sw).
		Height(m.height).
		Border(lipgloss.NormalBorder(), false, true, false, false).
		BorderForeground(sidebarBorder).
		Render(m.chats.View())

	// Input bar — highlight border when focused.
	inputBorder := clrBorderD
	if m.selectedView == viewInput {
		inputBorder = clrBorderA
	}
	inputBox := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true, false, false, false).
		BorderForeground(inputBorder).
		Padding(0, 1).
		Render(m.input.View())

	chatArea := lipgloss.JoinVertical(lipgloss.Left,
		m.viewport.View(),
		inputBox,
	)

	v := tea.NewView(lipgloss.JoinHorizontal(lipgloss.Top, sidebar, chatArea))
	v.AltScreen = true
	return v
}
