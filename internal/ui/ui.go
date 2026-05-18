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
				switch m.confirmTarget {
				case confirmDeleteMessage:
					m.deleteSelectedMsg()
				case confirmDeleteChat:
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
			if msg.String() == "ctrl+c" || m.selectedView != viewChat {
				return m, tea.Quit
			}

		case key.Matches(msg, m.keys.Back):
			if m.selectedView != viewAccounts && m.selectedView != viewChats {
				m.cancelPending()
				m.selectedView = viewChats
				m.input.Blur()
				return m, nil
			}

		case key.Matches(msg, m.keys.FocusChats):
			m.selectedView = viewChats
			m.input.Blur()
			return m, nil

		case key.Matches(msg, m.keys.ChatOpen):
			if m.selectedView == viewChats {
				return m.openCurrentChat()
			}

		case key.Matches(msg, m.keys.SelectSend):
			switch m.selectedView {
			case viewChats:
				return m.openCurrentChat()
			case viewChat:
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
					msgs := m.currentMessages()
					if m.editingMsgIdx < len(msgs) {
						msgs[m.editingMsgIdx].Content = text
						m.setCurrentMessages(msgs)
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
					msgs := append(m.currentMessages(), newMsg)
					m.setCurrentMessages(msgs)
				}

				m.input.SetValue("")
				m.updateSizes()
				m.refreshViewport()
				m.viewport.GotoBottom()
				// cmds = append(cmds, m.showNotification("message sent")) # TODO: only show notification on error
				return m, tea.Batch(cmds...)
			}

		case key.Matches(msg, m.keys.Switch):
			switch m.selectedView {
			case viewAccounts:
				m.selectedView = viewChats
				return m, nil
			case viewChats:
				m.selectedView = viewAccounts
				return m, nil
			}

		// ── Viewport paging (chat view) ───────────────────────────────────
		case isViewportPagingKey(msg):
			if m.selectedView == viewChat {
				var viewportCmd tea.Cmd
				m.viewport, viewportCmd = m.viewport.Update(msg)
				cmds = append(cmds, viewportCmd)
				return m, tea.Batch(cmds...)
			}

		// ── Message navigation ─────────────────────────────────────────────
		case key.Matches(msg, m.keys.MsgUp):
			if m.selectedView == viewAccounts && m.currentAccount > 0 {
				cmds = append(cmds, m.switchAccount(m.currentAccount-1))
				return m, tea.Batch(cmds...)
			}
			if m.selectedView == viewChat && m.selectedMsg > 0 {
				m.selectedMsg--
				m.refreshViewportScrollTo(m.selectedMsg)
				return m, nil
			}

		case key.Matches(msg, m.keys.MsgDown):
			if m.selectedView == viewAccounts && m.currentAccount < len(m.accounts)-1 {
				cmds = append(cmds, m.switchAccount(m.currentAccount+1))
				return m, tea.Batch(cmds...)
			}
			if m.selectedView == viewChat {
				chatIdx := m.currentChatIndex()
				if chatIdx < 0 {
					return m, nil
				}
				if m.selectedMsg < len(m.currentMessages())-1 {
					m.selectedMsg++
					m.refreshViewportScrollTo(m.selectedMsg)
					return m, nil
				}
			}

		// ── Message actions ────────────────────────────────────────────────
		case key.Matches(msg, m.keys.DeleteMsg):
			if m.selectedView == viewChat {
				chatIdx := m.currentChatIndex()
				if chatIdx < 0 {
					return m, nil
				}
				if len(m.currentMessages()) > 0 {
					m.confirmTarget = confirmDeleteMessage
				}
				return m, nil
			}
			if m.selectedView == viewChats && m.currentChatIndex() >= 0 {
				m.confirmTarget = confirmDeleteChat
				return m, nil
			}

		case key.Matches(msg, m.keys.YankMsg):
			if m.selectedView == viewChat {
				if err := m.yankSelectedMsg(); err != nil {
					cmds = append(cmds, m.showNotification("copy failed"))
				} else {
					cmds = append(cmds, m.showNotification("message copied"))
				}
				return m, tea.Batch(cmds...)
			}

		case key.Matches(msg, m.keys.EditMsg):
			if m.selectedView == viewChat {
				if m.currentChatIndex() < 0 {
					return m, nil
				}
				msgs := m.currentMessages()
				if m.canEdit(msgs) {
					m.editingMsgIdx = m.selectedMsg
					m.input.SetValue(msgs[m.selectedMsg].Content)
					m.input.Placeholder = "edit message..."
					cmds = append(cmds, m.input.Focus())
					return m, tea.Batch(cmds...)
				}
			}

		case key.Matches(msg, m.keys.ReplyMsg):
			if m.selectedView == viewChat {
				if m.currentChatIndex() < 0 {
					return m, nil
				}
				if len(m.currentMessages()) > 0 {
					m.replyToIdx = m.selectedMsg
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
	case viewAccounts:
		// Account focus is handled by global keys only.
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
			msgs := m.currentMessages()
			if len(msgs) > 0 {
				m.selectedMsg = len(msgs) - 1
			} else {
				m.selectedMsg = 0
			}
			m.refreshViewport()
			m.viewport.GotoBottom()
		}
	case viewChat:
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

func (m Model) currentMessages() []Message {
	if m.currentAccount < 0 || m.currentAccount >= len(m.accounts) {
		return nil
	}
	chatIdx := m.currentChatIndex()
	if chatIdx < 0 {
		return nil
	}
	return m.accounts[m.currentAccount].Messages[chatIdx]
}

func (m Model) currentChat() (Chat, bool) {
	if chatIdx := m.currentChatIndex(); chatIdx >= 0 {
		if chat, ok := m.chats.Items()[chatIdx].(Chat); ok {
			return chat, true
		}
	}
	return Chat{}, false
}

func isViewportPagingKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "pgup", "pgdown", "pageup", "pagedown":
		return true
	default:
		return false
	}
}

func (m *Model) setCurrentMessages(msgs []Message) {
	if m.currentAccount < 0 || m.currentAccount >= len(m.accounts) {
		return
	}
	chatIdx := m.currentChatIndex()
	if chatIdx < 0 {
		return
	}
	m.accounts[m.currentAccount].Messages[chatIdx] = msgs
}

func (m *Model) switchAccount(index int) tea.Cmd {
	if index < 0 || index >= len(m.accounts) || index == m.currentAccount {
		return nil
	}
	m.currentAccount = index
	m.cancelPending()
	m.chats.Select(0)
	m.selectedMsg = 0
	cmd := m.chats.SetItems(m.accounts[index].Chats)
	if msgs := m.currentMessages(); len(msgs) > 0 {
		m.selectedMsg = len(msgs) - 1
	}
	m.refreshViewport()
	m.viewport.GotoBottom()
	return cmd
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
	m.selectedView = viewChat
	if msgs := m.currentMessages(); len(msgs) > 0 {
		m.selectedMsg = len(msgs) - 1
	}
	m.refreshViewport()
	return m, m.input.Focus()
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
	if m.currentChatIndex() < 0 {
		return
	}
	msgs := m.currentMessages()
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
	m.setCurrentMessages(newMsgs)
	if m.selectedMsg >= len(newMsgs) && len(newMsgs) > 0 {
		m.selectedMsg = len(newMsgs) - 1
	}
}

func (m Model) yankSelectedMsg() error {
	if m.currentChatIndex() < 0 {
		return nil
	}
	msgs := m.currentMessages()
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
	oldMessages := m.accounts[m.currentAccount].Messages
	for i := range items {
		switch {
		case i < chatIdx:
			newMessages[i] = oldMessages[i]
		case i > chatIdx:
			newMessages[i-1] = oldMessages[i]
		}
	}
	m.accounts[m.currentAccount].Chats = newItems
	m.accounts[m.currentAccount].Messages = newMessages

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
	msgs := m.currentMessages()
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
	msgs := m.currentMessages()
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
	isSelected := msgIdx == m.selectedMsg
	prefix := m.styles.renderMessagePrefix(isSelected)

	nick := msg.Author
	timeLabel := msg.SentAt.Format("15:04")
	if !sameDay(msg.SentAt, time.Now()) {
		timeLabel = msg.SentAt.Format("2006-01-02 15:04")
	}
	headerPlain := fmt.Sprintf("[%s] <%s> ", timeLabel, nick)
	header := m.styles.renderMessageHeader(timeLabel, nick, msg.IsMe)
	indent := strings.Repeat(" ", lipgloss.Width(headerPlain))
	wrapWidth := totalWidth - lipgloss.Width(prefix) - lipgloss.Width(indent)
	wrapWidth = max(wrapWidth, 8)

	var lines []string
	if msg.ReplyTo != nil {
		reply := m.replyPreview(*msg.ReplyTo, allMsgs)
		replyWrapped := strings.SplitSeq(ansi.Wrap(reply, max(8, totalWidth-lipgloss.Width(prefix)-2), " "), "\n")
		for line := range replyWrapped {
			lines = append(lines, prefix+m.styles.messageReply.Render(line))
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

	return strings.Join(lines, "\n")
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

// ── View ──────────────────────────────────────────────────────────────────────

func (m Model) View() tea.View {
	if m.width == 0 {
		v := tea.NewView("loading...")
		v.AltScreen = true
		return v
	}

	colors := m.styles.colors
	sw := m.sidebarWidth()

	// ── Sidebar ────────────────────────────────────────────────────────────
	sidebarBorder := colors.borderD
	if m.selectedView == viewChats || m.selectedView == viewAccounts {
		sidebarBorder = colors.borderA
	}
	accountBg := colors.panelEdge
	accountFg := colors.statusFg
	if m.selectedView == viewAccounts {
		accountBg = colors.borderA
		// accountFg = colors.appBg
	}
	statusLine := m.styles.sidebarStatusLine(sw, accountBg, accountFg, m.renderAccountBar(sw))
	sidebarInner := lipgloss.JoinVertical(lipgloss.Left,
		statusLine,
		m.styles.sidebarInner(sw, max(0, m.height-sidebarStatusHeight), m.chats.View()),
	)
	sidebar := m.styles.sidebarBox(sw, m.height, sidebarBorder, sidebarInner)

	// ── Input box ──────────────────────────────────────────────────────────
	inputBorder := colors.borderD
	if m.selectedView == viewChat {
		inputBorder = colors.accentCyan
	}

	var inputInner string
	inputWidth := m.chatAreaWidth() - 2
	if m.replyToIdx >= 0 {
		chatIdx := m.currentChatIndex()
		if chatIdx >= 0 {
			msgs := m.currentMessages()
			if m.replyToIdx < len(msgs) {
				orig := msgs[m.replyToIdx]
				preview := strings.ReplaceAll(orig.Content, "\n", " ")
				runes := []rune(preview)
				if len(runes) > 40 {
					preview = string(runes[:37]) + "…"
				}
				hint := m.styles.renderReplyHint(orig.Author, preview)
				inputInner = m.styles.inputInnerBox(inputWidth, hint) + "\n" + m.styles.inputInnerBox(inputWidth, m.input.View())
			} else {
				inputInner = m.styles.inputInnerBox(inputWidth, m.input.View())
			}
		} else {
			inputInner = m.styles.inputInnerBox(inputWidth, m.input.View())
		}
	} else {
		inputInner = m.styles.inputInnerBox(inputWidth, m.input.View())
	}

	inputBox := m.styles.inputContainer(inputBorder, inputInner)

	// ── Viewport / popup ───────────────────────────────────────────────────
	var viewportArea string
	if m.confirmTarget != confirmNone {
		viewportArea = m.renderDeletePopup()
	} else {
		viewportHeight := m.height - m.inputAreaHeight() - chatStatusHeight
		contentHeight := viewportHeight
		if m.noticeText != "" && contentHeight > 1 {
			contentHeight--
		}
		viewportBody := m.styles.viewportContent(m.chatAreaWidth(), contentHeight, m.viewport.View())
		if m.noticeText != "" {
			notice := m.styles.noticeBar(m.chatAreaWidth(), m.noticeText)
			viewportBody = lipgloss.JoinVertical(lipgloss.Left, viewportBody, notice)
		}
		viewportArea = m.styles.viewportFrame(m.chatAreaWidth(), viewportHeight, viewportBody)
	}

	chatStatus := m.styles.sidebarStatusLine(
		m.chatAreaWidth(),
		colors.panelEdge,
		colors.statusFg,
		m.renderChatStatusBar(m.chatAreaWidth()),
	)
	chatArea := lipgloss.JoinVertical(lipgloss.Left, chatStatus, viewportArea, inputBox)

	root := m.styles.rootView(lipgloss.JoinHorizontal(lipgloss.Top, sidebar, chatArea))

	v := tea.NewView(root)
	v.AltScreen = true
	return v
}

// renderDeletePopup renders a centered confirmation dialog inside the viewport
// area instead of overlaying raw ANSI (simpler and more portable).
func (m Model) renderDeletePopup() string {
	cw := m.chatAreaWidth()
	vh := m.height - m.inputAreaHeight()

	popup := m.styles.popupDialog(m.styles.colors.borderA, m.deletePrompt())

	return lipgloss.Place(cw, vh, lipgloss.Center, lipgloss.Center, popup)
}

func (m Model) deletePrompt() string {
	switch m.confirmTarget {
	case confirmDeleteChat:
		return m.styles.deletePrompt("Leave chat?")
	default:
		return m.styles.deletePrompt("Delete message?")
	}
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

func (m Model) renderAccountBar(width int) string {
	if len(m.accounts) == 0 {
		return "accounts: none"
	}
	parts := make([]string, 0, len(m.accounts)+1)
	parts = append(parts, "accounts:")
	for i, account := range m.accounts {
		name := account.Name
		switch {
		case i == m.currentAccount && m.selectedView == viewAccounts:
			name = "[" + name + "]"
		case i == m.currentAccount:
			name = "<" + name + ">"
		}
		parts = append(parts, name)
	}
	return ansi.Truncate(strings.Join(parts, " "), max(1, width-2), "…")
}

func (m Model) renderChatStatusBar(width int) string {
	chat, ok := m.currentChat()
	if !ok {
		return ""
	}

	label := chat.Name
	switch {
	case chat.Address != "":
		label = fmt.Sprintf("%s <%s>", chat.Name, chat.Address)
	case strings.HasPrefix(chat.Name, "#"):
		label = chat.Name
	}

	return ansi.Truncate(label, max(1, width-2), "…")
}
