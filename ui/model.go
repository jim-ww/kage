package ui

import (
	"time"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
)

type Message struct {
	ID          string // stanza ID; enables XEP-0308 correction and XEP-0461 reply targeting
	Author      string
	Content     string
	SentAt      time.Time
	IsMe        bool
	ReplyTo     *int     // index into the message slice; nil = not a reply
	Attachments []string // file paths or URLs attached to the message

	// Retracted is set when the sender attempted a XEP-0424 retraction of
	// this message. Content is kept and still shown — we don't trust a
	// remote retraction to erase what was said on our side — but flagged so
	// the attempt is visible.
	Retracted bool
}

type Account struct {
	Name     string
	Chats    []list.Item
	Messages map[int][]Message
}

// Presence is a contact's coarse online status.
type Presence int

const (
	PresenceOffline Presence = iota // default: never seen online, or explicitly unavailable
	PresenceAway
	PresenceOnline
)

type Chat struct {
	Name        string
	Address     string
	LastMessage string
	Presence    Presence
}

func (c Chat) Title() string { return presenceGlyph(c.Presence) + " " + c.Name }
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

// SendOptions carries optional wire metadata for MessageSender.Send — kept as
// plain fields here (rather than importing the xmpp package's own type) so
// ui stays decoupled from the network layer; the adapter implementing
// MessageSender translates these into XEP-0308/XEP-0461 elements.
type SendOptions struct {
	// ReplaceID, if set, marks this send as a correction of the earlier
	// message with this ID (XEP-0308).
	ReplaceID string

	// ReplyToID, if set, marks this send as a reply to the message with this
	// ID (XEP-0461). QuotedAuthor/QuotedBody let the sender build the
	// quoted-text fallback for reply-unaware clients.
	ReplyToID    string
	QuotedAuthor string
	QuotedBody   string

	// RetractID, if set, sends a XEP-0424 retraction of the earlier message
	// with this ID instead of a normal message. Mutually exclusive with the
	// other options above.
	RetractID string
}

// MessageSender delivers an outgoing message for the given account to "to"
// (a bare/full JID), returning the ID it was sent with. Implemented outside
// ui — by an adapter that knows about the xmpp session and any per-peer
// encryption — so ui stays decoupled from both.
type MessageSender interface {
	Send(accountIdx int, to, body string, opts SendOptions) (id string, err error)
}

// IncomingMessageMsg is sent into the Bubble Tea loop when a message arrives
// from the network for one of the configured accounts.
type IncomingMessageMsg struct {
	AccountIdx int
	From       string // bare/full JID the message came from
	ReplyToID  string // non-empty if this message is a XEP-0461 reply
	Message    Message
}

// MessageCorrectedMsg is sent into the Bubble Tea loop when a XEP-0308
// correction arrives for a message already shown for one of the configured
// accounts.
type MessageCorrectedMsg struct {
	AccountIdx int
	From       string // bare JID (chat)
	ReplaceID  string // ID of the message being corrected
	NewContent string
}

// MessageRetractedMsg is sent into the Bubble Tea loop when the other party
// attempts to retract (XEP-0424) a message already shown for one of the
// configured accounts. The message is flagged, never removed — see
// Message.Retracted.
type MessageRetractedMsg struct {
	AccountIdx int
	From       string // bare JID (chat)
	RetractID  string // ID of the message being retracted
}

// PresenceMsg is sent into the Bubble Tea loop when a contact's presence
// changes for one of the configured accounts.
type PresenceMsg struct {
	AccountIdx int
	From       string // bare JID
	Presence   Presence
}

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
	showMsgInfo   bool     // true while the message-info popup is open
	openItems     []string // non-empty while the open-link/attachment picker is open
	openPage      int      // current page (of openItemsPerPage items) in the open picker
	msgOffsets    []int    // line offset of each message inside viewport content
	noticeText    string
	noticeID      int

	sender MessageSender
}

type noticeClearMsg struct {
	id int
}

func New(accounts []Account, keys KeyMap, theme Theme, sender MessageSender) Model {
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
		sender:         sender,
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

func (m Model) sidebarWidth() int {
	const minWidth, maxWidth = 20, 36
	w := m.width / 4
	if w < minWidth {
		w = minWidth
	}
	if w > maxWidth {
		w = maxWidth
	}
	return min(w, m.width)
}
func (m Model) chatAreaWidth() int { return m.width - m.sidebarWidth() - 1 }

// inputAreaHeight accounts for the optional reply-hint line.
func (m Model) inputAreaHeight() int {
	if m.replyToIdx >= 0 {
		return 3 // top border + reply hint line + input line
	}
	return 2 // top border + input line
}
