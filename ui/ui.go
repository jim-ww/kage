package ui

import (
	"fmt"
	"image/color"
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
	SelectSend    key.Binding
	MsgUp         key.Binding // k — navigate to previous message
	MsgDown       key.Binding // j — navigate to next message
	DeleteMsg     key.Binding // d — delete selected message (with popup)
	EditMsg       key.Binding // e — edit (only last own message)
	ReplyMsg      key.Binding // r — reply to selected message
	ConfirmYes    key.Binding // y — confirm popup
	ConfirmNo     key.Binding // n / esc — cancel popup
	ListKeys      list.KeyMap
	TextInputKeys textinput.KeyMap
}

var DefaultKeyMap = KeyMap{
	Quit:       key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q/ctrl+c", "quit")),
	Back:       key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back to chats")),
	Switch:     key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "switch focus")),
	SelectSend: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select/send")),
	MsgUp:      key.NewBinding(key.WithKeys("k"), key.WithHelp("k", "prev msg")),
	MsgDown:    key.NewBinding(key.WithKeys("j"), key.WithHelp("j", "next msg")),
	DeleteMsg:  key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete")),
	EditMsg:    key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit (own last)")),
	ReplyMsg:   key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reply")),
	ConfirmYes: key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "yes")),
	ConfirmNo:  key.NewBinding(key.WithKeys("n", "esc"), key.WithHelp("n/esc", "no")),

	ListKeys:      list.DefaultKeyMap(),
	TextInputKeys: textinput.DefaultKeyMap(),
}

func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Quit, k.Back, k.Switch, k.SelectSend}
}

func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Quit, k.Back, k.Switch, k.SelectSend},
		{k.MsgUp, k.MsgDown, k.DeleteMsg, k.EditMsg, k.ReplyMsg},
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
	clrReplyFg = lipgloss.Color("#6699aa")
	clrPopupBg = lipgloss.Color("#1e1e1e")

	// stBubble is a value type — safe to chain without mutation.
	stBubble = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Padding(0, 1)

	stTime  = lipgloss.NewStyle().Foreground(clrTime)
	stReply = lipgloss.NewStyle().
		Foreground(clrReplyFg).
		Italic(true)
)

// bubble geometry constants: rounded border adds 1 col each side, padding adds 1 each side → 4 total.
const bubbleDecorationWidth = 4

// ── Focus state ───────────────────────────────────────────────────────────────

type selectedView int

const (
	viewChats    selectedView = iota
	viewViewport              // scroll + message navigation
	viewInput                 // type and send / edit
)

// ── Model ─────────────────────────────────────────────────────────────────────

type Model struct {
	width, height int
	selectedView
	keys KeyMap

	chats    list.Model
	messages map[int][]Message

	input    textinput.Model
	viewport viewport.Model

	// message interaction state
	selectedMsg       int // index of highlighted message (meaningful in viewViewport)
	editingMsgIdx     int // >= 0 while editing a message; -1 otherwise
	replyToIdx        int // >= 0 while composing a reply; -1 otherwise
	showDeleteConfirm bool
	msgOffsets        []int // line offset of each message inside viewport content
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
		selectedView:  viewInput,
		keys:          DefaultKeyMap,
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

	case tea.KeyMsg:
		// ── Delete confirmation popup intercepts all input ─────────────────
		if m.showDeleteConfirm {
			switch {
			case key.Matches(msg, m.keys.ConfirmYes):
				m.deleteSelectedMsg()
				m.showDeleteConfirm = false
				m.refreshViewport()
			case key.Matches(msg, m.keys.ConfirmNo):
				m.showDeleteConfirm = false
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

		case key.Matches(msg, m.keys.SelectSend):
			switch m.selectedView {

			case viewChats:
				m.selectedView = viewViewport
				m.input.Blur()
				chatIdx := m.chats.Index()
				if msgs := m.messages[chatIdx]; len(msgs) > 0 {
					m.selectedMsg = len(msgs) - 1
				}
				m.refreshViewport()
				return m, nil

			case viewInput:
				text := strings.TrimSpace(m.input.Value())
				if text == "" {
					return m, nil
				}
				chatIdx := m.chats.Index()

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

		// ── Message navigation (viewport only) ────────────────────────────
		case key.Matches(msg, m.keys.MsgUp):
			if m.selectedView == viewViewport && m.selectedMsg > 0 {
				m.selectedMsg--
				m.refreshViewportScrollTo(m.selectedMsg)
			}
			return m, nil

		case key.Matches(msg, m.keys.MsgDown):
			if m.selectedView == viewViewport {
				chatIdx := m.chats.Index()
				if m.selectedMsg < len(m.messages[chatIdx])-1 {
					m.selectedMsg++
					m.refreshViewportScrollTo(m.selectedMsg)
				}
			}
			return m, nil

		// ── Message actions (viewport only) ───────────────────────────────
		case key.Matches(msg, m.keys.DeleteMsg):
			if m.selectedView == viewViewport {
				chatIdx := m.chats.Index()
				if len(m.messages[chatIdx]) > 0 {
					m.showDeleteConfirm = true
				}
				return m, nil
			}

		case key.Matches(msg, m.keys.EditMsg):
			if m.selectedView == viewViewport {
				chatIdx := m.chats.Index()
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
			return m, nil

		case key.Matches(msg, m.keys.ReplyMsg):
			if m.selectedView == viewViewport {
				chatIdx := m.chats.Index()
				if len(m.messages[chatIdx]) > 0 {
					m.replyToIdx = m.selectedMsg
					m.selectedView = viewInput
					m.updateSizes()
					m.refreshViewport()
					cmds = append(cmds, m.input.Focus())
					return m, tea.Batch(cmds...)
				}
			}
			return m, nil
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
			chatIdx := m.chats.Index()
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
	chatIdx := m.chats.Index()
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

	m.chats.SetHeight(m.height)
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

	// Bubbles fill at most 60 % of the chat area.
	// Content width = maxBubble - bubbleDecorationWidth (border + padding).
	maxBubble := cw * 3 / 5

	chatIdx := m.chats.Index()
	msgs := m.messages[chatIdx]
	offsets := make([]int, len(msgs))

	var sb strings.Builder
	currentLine := 0

	for i, msg := range msgs {
		if i > 0 {
			sb.WriteString("\n\n")
			currentLine += 2
		}
		offsets[i] = currentLine
		rendered := m.renderBubble(msg, i, maxBubble, cw, msgs)
		sb.WriteString(rendered)
		currentLine += strings.Count(rendered, "\n") + 1
	}
	return sb.String(), offsets
}

// renderBubble renders a single message bubble with its timestamp.
// Incoming messages are left-aligned; outgoing are right-aligned.
func (m Model) renderBubble(msg Message, msgIdx, maxBubble, totalWidth int, allMsgs []Message) string {
	isSelected := m.selectedView == viewViewport && msgIdx == m.selectedMsg

	ts := stTime.Render(msg.SentAt.Format("15:04"))
	content := m.buildContent(msg, allMsgs)

	// The Width() style method sets the content area width (inside border+padding).
	// lipgloss wraps text automatically to fit this width.
	contentW := maxBubble - bubbleDecorationWidth
	if contentW < 4 {
		contentW = 4
	}

	borderClr := func(defaultClr color.Color) color.Color {
		if isSelected {
			return clrBorderA // cyan highlight when selected
		}
		return defaultClr
	}

	if msg.IsMe {
		bubble := stBubble.
			Background(clrMeBg).
			Foreground(clrMeFg).
			BorderForeground(borderClr(clrMeBg)).
			Width(contentW).
			Render(content)

		tw := lipgloss.Width(ts)
		// Small right-side indicator for selected outgoing message.
		indicator := ""
		if isSelected {
			indicator = lipgloss.NewStyle().Foreground(clrBorderA).Render(" ▐")
		}
		tsLine := spaces(totalWidth-tw-2) + ts + indicator
		return rightAlignBlock(bubble, totalWidth) + "\n" + tsLine
	}

	// Incoming message.
	bubble := stBubble.
		Background(clrThemBg).
		Foreground(clrThemFg).
		BorderForeground(borderClr(clrThemBg)).
		Width(contentW).
		Render(content)

	// Prefix the first line with a selection indicator; remaining lines get
	// the same-width blank so text stays aligned.
	prefix := "  "
	if isSelected {
		prefix = lipgloss.NewStyle().Foreground(clrBorderA).Render("▌") + " "
	}
	lines := strings.Split(bubble, "\n")
	var sb strings.Builder
	for i, line := range lines {
		if i > 0 {
			sb.WriteByte('\n')
			sb.WriteString("  ") // 2-space blank to match prefix width
		} else {
			sb.WriteString(prefix)
		}
		sb.WriteString(line)
	}
	return sb.String() + "\n" + "  " + ts
}

// buildContent assembles the content string: optional reply quote followed by
// the message body. lipgloss Width() on the bubble will wrap the whole thing.
func (m Model) buildContent(msg Message, allMsgs []Message) string {
	if msg.ReplyTo == nil {
		return msg.Content
	}
	idx := *msg.ReplyTo
	if idx < 0 || idx >= len(allMsgs) {
		return msg.Content
	}
	orig := allMsgs[idx]
	preview := strings.ReplaceAll(orig.Content, "\n", " ")
	runes := []rune(preview)
	if len(runes) > 30 {
		preview = string(runes[:27]) + "…"
	}
	quote := stReply.Render(fmt.Sprintf("↩ %s: %s", orig.Author, preview))
	return quote + "\n" + msg.Content
}

// rightAlignBlock pads every line of a multi-line string so the widest line
// ends at totalWidth-2, preserving the visual alignment of rounded borders.
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

	// ── Sidebar ────────────────────────────────────────────────────────────
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

	// ── Input box ──────────────────────────────────────────────────────────
	inputBorder := clrBorderD
	if m.selectedView == viewInput {
		inputBorder = clrBorderA
	}

	var inputInner string
	if m.replyToIdx >= 0 {
		chatIdx := m.chats.Index()
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

	inputBox := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true, false, false, false).
		BorderForeground(inputBorder).
		Padding(0, 1).
		Render(inputInner)

	// ── Viewport / popup ───────────────────────────────────────────────────
	var viewportArea string
	if m.showDeleteConfirm {
		viewportArea = m.renderDeletePopup()
	} else {
		viewportArea = m.viewport.View()
	}

	chatArea := lipgloss.JoinVertical(lipgloss.Left, viewportArea, inputBox)

	v := tea.NewView(lipgloss.JoinHorizontal(lipgloss.Top, sidebar, chatArea))
	v.AltScreen = true
	return v
}

// renderDeletePopup renders a centered confirmation dialog inside the viewport
// area instead of overlaying raw ANSI (simpler and more portable).
func (m Model) renderDeletePopup() string {
	cw := m.chatAreaWidth()
	vh := m.height - m.inputAreaHeight()

	popup := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(clrBorderA).
		Background(clrPopupBg).
		Foreground(lipgloss.Color("#e0e0e0")).
		Padding(1, 4).
		Render("Delete message?\n\n  [y] yes    [n] no")

	return lipgloss.Place(cw, vh, lipgloss.Center, lipgloss.Center, popup)
}
