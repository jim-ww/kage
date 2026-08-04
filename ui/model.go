package ui

import (
	"os"
	"time"

	"charm.land/bubbles/v2/filepicker"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	zone "github.com/lrstanley/bubblezone/v2"
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

	// Reactions is the aggregate (XEP-0444) reaction state on this message
	// across everyone who's reacted, one entry per distinct emoji.
	Reactions []Reaction
}

// Reaction is one distinct emoji's aggregate state on a message.
type Reaction struct {
	Emoji string
	Count int
	Mine  bool // true if our own account is one of the reactors for Emoji
}

type Account struct {
	Name     string
	Chats    []list.Item
	Messages map[int][]Message

	// Connecting is true from New()/AddAccount until the account's dial,
	// roster fetch, and local history load complete asynchronously in the
	// background — see AccountConnectedMsg/AccountConnectErrorMsg.
	Connecting bool
	// ConnectError is set (and Connecting cleared) if the background connect
	// failed; the account stays in the sidebar so the user can see which one
	// is down rather than it silently vanishing.
	ConnectError string
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
	Typing      bool // true while the peer has an active XEP-0085 "composing" state

	// EncryptionMode is this chat's outgoing message encryption: "omemo"
	// (default), "gpg", or "none". Set by ChatEncryptionSetter.
	EncryptionMode string
}

func (c Chat) Title() string { return presenceGlyph(c.Presence) + " " + c.Name }
func (c Chat) Description() string {
	switch {
	case c.LastMessage != "":
		return c.LastMessage
	case c.Address != "" && c.Address != c.Name:
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

	// ReactionTargetID, if set, sends a XEP-0444 reaction-set update instead
	// of a normal message: Reactions becomes our complete, current reaction
	// set on the message with this ID (an empty-but-non-nil slice clears
	// it). Mutually exclusive with the other options above.
	ReactionTargetID string
	Reactions        []string
}

// MessageSender delivers an outgoing message for the given account to "to"
// (a bare/full JID), returning the ID it was sent with. Implemented outside
// ui — by an adapter that knows about the xmpp session and any per-peer
// encryption — so ui stays decoupled from both.
type MessageSender interface {
	Send(accountIdx int, to, body string, opts SendOptions) (id string, err error)

	// SetTyping sends a XEP-0085 chat state notification: composing=true
	// while the user is actively typing to "to", false once they stop
	// (cleared the input or sent) or navigate away.
	SetTyping(accountIdx int, to string, composing bool) error
}

// FileSender uploads a local file and sends its download URL to a chat. It is
// separate from MessageSender so text-only senders remain compatible.
type FileSender interface {
	SendFile(accountIdx int, to, path string) tea.Msg
}

// ContactRenamer sets a custom display name for a contact — a roster set
// (RFC 6121), persisted server-side and mirrored to local storage — so ui
// stays decoupled from the XMPP/storage layers. Called synchronously from
// Update(), like MessageSender.Send/SetTyping: renaming isn't a bulk
// operation and doesn't need its own async result message.
type ContactRenamer interface {
	RenameContact(accountIdx int, address, name string) error
}

// FileSendResultMsg reports completion of an asynchronous upload and send.
type FileSendResultMsg struct {
	AccountIdx int
	To         string
	Path       string
	URL        string
	ID         string
	Err        error
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

// MessageReactionsMsg is sent into the Bubble Tea loop when a XEP-0444
// reaction-set update arrives for a message already shown for one of the
// configured accounts. Reactions is the full recomputed aggregate to
// display, replacing whatever was there before.
type MessageReactionsMsg struct {
	AccountIdx int
	From       string // bare JID (chat)
	MessageID  string // ID of the message being reacted to
	Reactions  []Reaction
}

// PresenceMsg is sent into the Bubble Tea loop when a contact's presence
// changes for one of the configured accounts.
type PresenceMsg struct {
	AccountIdx int
	From       string // bare JID
	Presence   Presence
}

// TypingMsg is sent into the Bubble Tea loop when a contact's XEP-0085 chat
// state changes for one of the configured accounts.
type TypingMsg struct {
	AccountIdx int
	From       string // bare JID
	Typing     bool
}

// typingPauseTimeout is how long the compose input can sit idle (no
// keystrokes, but not cleared either) before we tell the peer we've stopped
// composing — matching XEP-0085's convention that "composing" shouldn't
// stick forever just because the draft is still in the box.
const typingPauseTimeout = 5 * time.Second

// typingPauseMsg fires typingPauseTimeout after a keystroke that left the
// compose input non-empty. Gen must still match Model.typingGen when it
// arrives — otherwise a later keystroke already rearmed the timer (or
// stopped composing outright) and this one is stale and ignored.
type typingPauseMsg struct {
	addr string
	gen  int
}

func typingPauseTimer(addr string, gen int) tea.Cmd {
	return tea.Tick(typingPauseTimeout, func(time.Time) tea.Msg {
		return typingPauseMsg{addr: addr, gen: gen}
	})
}

// AccountAdder connects and persists a new XMPP account, implemented outside
// ui (main.go's adapter) so ui stays decoupled from the network/config
// layers. Runs on the Bubble Tea event loop's goroutine via a tea.Cmd, so it
// may block on network I/O — ui always calls it that way, never inline.
type AccountAdder interface {
	AddAccount(jid, password, gpgKeyID string) tea.Msg
}

// AccountAddedMsg is sent into the Bubble Tea loop once AccountAdder.AddAccount
// has connected the new account and it's ready to show in the sidebar.
type AccountAddedMsg struct {
	Account Account
}

// AccountAddErrorMsg is sent into the Bubble Tea loop when AccountAdder.AddAccount
// fails; the add-account form stays open so the user can correct and retry.
type AccountAddErrorMsg struct {
	Err error
}

// AccountConnectedMsg is sent into the Bubble Tea loop once a configured
// account's *local* storage has been opened and its cached roster/history
// loaded from disk — no network involved, so this is fast and lets local
// chats/messages appear instantly instead of waiting on a live connection.
// Index addresses the placeholder Account passed to New() at the same
// position — it never changes, unlike AddAccount's accounts which are always
// appended. The account is still marked Connecting until AccountLiveMsg
// arrives.
type AccountConnectedMsg struct {
	Index   int
	Account Account
}

// AccountLiveMsg is sent into the Bubble Tea loop once a configured account
// has finished dialing and fetching its live roster (in the background,
// after AccountConnectedMsg already showed local data) — clearing
// Connecting. NewChats/NewMessages carry only contacts the local snapshot
// didn't already know about (freshly added on another device); existing
// chats/history are left untouched rather than re-fetched, since they're
// already showing.
type AccountLiveMsg struct {
	Index       int
	NewChats    []list.Item
	NewMessages map[int][]Message // indices are relative to Chats *after* NewChats is appended
}

// AccountConnectErrorMsg is sent into the Bubble Tea loop when a configured
// account's background connect (local load or live dial — see
// AccountConnectedMsg/AccountLiveMsg) fails. The account stays in the
// sidebar, marked with the error, rather than disappearing — any local data
// already shown is left in place.
type AccountConnectErrorMsg struct {
	Index int
	Err   error
}

// DefaultAccountSetter persists which account should be selected on startup,
// implemented outside ui (main.go's adapter) so ui stays decoupled from the
// config layer. It's a local file write, not network I/O, so ui calls it
// inline like Send/SetTyping rather than through a tea.Cmd.
type DefaultAccountSetter interface {
	SetDefaultAccount(jid string) error
}

// ChatEncryptionSetter persists per-chat outgoing message encryption choice
// ("omemo", "gpg", or "none"), implemented outside ui (main.go's adapter) so
// ui stays decoupled from the storage layer. A local database write, called
// inline like Send/SetTyping rather than through a tea.Cmd.
type ChatEncryptionSetter interface {
	SetChatEncryption(accountIdx int, peerJID, mode string) error
}

// SidebarWidthSetter persists the sidebar width the user last dragged it to,
// implemented outside ui (main.go's adapter) so ui stays decoupled from the
// config layer. A local file write, called inline like Send/SetTyping
// rather than through a tea.Cmd.
type SidebarWidthSetter interface {
	SetSidebarWidth(width int) error
}

type Model struct {
	width, height int
	termHeight    int // raw terminal rows from the last WindowSizeMsg; height is derived from this minus the footer's actual (view-dependent) row count
	selectedView
	keys   KeyMap
	theme  Theme
	styles uiStyles
	zone   *zone.Manager // owns mouse click/scroll regions marked in View(); see ui/mouse.go

	// mouseEnabled gates all mouse handling: when false, MouseMode is left
	// off (no click/scroll events are generated by the terminal), zone
	// marks are skipped, and mouse-only UI elements (e.g. the send button)
	// are not drawn.
	mouseEnabled bool

	// hover holds the zone ID currently under the pointer (empty if none),
	// so the currently-hovered send button/chat item/account row/message/
	// context-menu item can be highlighted. It's a pointer — shared with
	// the chat list delegate, which needs it too but only sees list.Model,
	// not Model — so a hover update via a pointer-receiver method is
	// visible everywhere without threading it through render call chains.
	hover *hoverState

	// sidebarWidthOverride is the user-dragged sidebar width (see
	// zonePaneSidebarBorder in ui/mouse.go); 0 means "not set yet, use the
	// width/4-based default computed by sidebarWidth".
	sidebarWidthOverride int
	// resizingSidebar is true from the moment the sidebar border is
	// pressed until the mouse button is released (tea.MouseReleaseMsg),
	// even if the pointer drifts off the border column mid-drag.
	resizingSidebar bool

	accounts       []Account
	currentAccount int
	chats          list.Model

	input    textinput.Model
	viewport viewport.Model

	// message interaction state
	selectedMsg      int               // index of highlighted message (meaningful in viewViewport)
	editingMsgIdx    int               // >= 0 while editing a message; -1 otherwise
	replyToIdx       int               // >= 0 while composing a reply; -1 otherwise
	reactingMsgIdx   int               // >= 0 while composing a reaction; -1 otherwise
	emojiSuggestions []emojiSuggestion // live fuzzy matches for the shortcode being typed, while reactingMsgIdx >= 0
	emojiSuggestIdx  int               // which suggestion is highlighted; left/right to move, tab to accept it
	confirmTarget    confirmTarget
	contextMenu      *contextMenu // non-nil while a right-click action popup is open; see ui/contextmenu.go
	showMsgInfo      bool         // true while the message-info popup is open
	openItems        []string     // non-empty while the open-link/attachment picker is open
	openPage         int          // current page (of openItemsPerPage items) in the open picker
	openMode         pickerMode   // what picking an item from openItems actually does: open or save
	filePicker       filepicker.Model
	pickingFile      bool     // true while the Bubble file picker is open
	msgOffsets       []int    // line offset of each message inside viewport content
	viewportLines    []string // viewport content split into lines, kept in sync with msgOffsets for refreshViewportSelection's line-range patching
	noticeText       string
	noticeID         int

	// double-click detection for messages
	lastClickedMsgIdx int       // index of the last clicked message (for double-click detection)
	lastClickTime     time.Time // time of the last message click (for double-click detection)

	sender               MessageSender
	fileSender           FileSender
	accountAdder         AccountAdder
	renamer              ContactRenamer
	defaultAccountSetter DefaultAccountSetter
	sidebarWidthSetter   SidebarWidthSetter
	chatEncryptionSetter ChatEncryptionSetter

	// rename-chat prompt state, active while renamingChat is true. Opened by
	// RenameChat (viewChats) or a chat-item context menu's "Rename", prefilled
	// with the chat's custom name if it has one; submitting an empty value
	// clears it (falls back to showing the JID).
	renamingChat  bool
	renameChatIdx int
	renameInput   textinput.Model

	// typingActiveTo is the address of the chat we're currently marked as
	// "composing" to (empty if not composing to anyone). typingGen
	// increments on every keystroke while composing, so a pending
	// typingPauseMsg (scheduled a few seconds out to send "stopped
	// composing" after a stall) can tell whether it's stale — a later
	// keystroke rearmed the timer — or should actually fire.
	typingActiveTo string
	typingGen      int

	// add-account form state, active while addingAccount is true
	addingAccount    bool
	addAccountInputs [3]textinput.Model // JID, password, gpg key ID (optional)
	addAccountFocus  int
	addAccountErr    string
	addAccountBusy   bool
}

type noticeClearMsg struct {
	id int
}

func New(accounts []Account, startAccount int, keys KeyMap, theme Theme, sender MessageSender, accountAdder AccountAdder, mouseEnabled bool, initialSidebarWidth int) Model {
	styles := newUIStyles(theme)
	zm := zone.New()
	zm.SetEnabled(mouseEnabled)
	hv := &hoverState{}
	delegate := newChatListDelegate(styles.colors, zm, mouseEnabled, hv)

	if startAccount < 0 || startAccount >= len(accounts) {
		startAccount = 0
	}
	initialChats := []list.Item(nil)
	if len(accounts) > 0 {
		initialChats = accounts[startAccount].Chats
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
	picker := filepicker.New()
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		picker.CurrentDirectory = home
	}
	picker.ShowPermissions = false
	applyFilePickerStyles(&picker, styles.colors)
	fileSender, _ := sender.(FileSender)
	renamer, _ := sender.(ContactRenamer)
	defaultAccountSetter, _ := sender.(DefaultAccountSetter)
	chatEncryptionSetter, _ := sender.(ChatEncryptionSetter)
	sidebarWidthSetter, _ := sender.(SidebarWidthSetter)

	return Model{
		selectedView:         viewChat,
		keys:                 keys,
		theme:                theme,
		styles:               styles,
		zone:                 zm,
		mouseEnabled:         mouseEnabled,
		hover:                hv,
		accounts:             accounts,
		currentAccount:       startAccount,
		chats:                l,
		input:                ti,
		viewport:             viewport.New(),
		editingMsgIdx:        -1,
		replyToIdx:           -1,
		reactingMsgIdx:       -1,
		lastClickedMsgIdx:    -1,
		sender:               sender,
		fileSender:           fileSender,
		accountAdder:         accountAdder,
		renamer:              renamer,
		defaultAccountSetter: defaultAccountSetter,
		chatEncryptionSetter: chatEncryptionSetter,
		sidebarWidthSetter:   sidebarWidthSetter,
		sidebarWidthOverride: initialSidebarWidth,
		filePicker:           picker,
	}
}

// newAddAccountForm builds fresh, empty textinput.Model fields for the
// add-account popup: JID, password (masked), and an optional GPG key ID.
// addAccountFieldWidth is fixed rather than derived from terminal size — the
// add-account popup isn't full-width like the main input box, and an unset
// (zero) textinput.Width truncates the placeholder to a single character
// (see textinput.Model.placeholderView's width-based truncation math), so
// this must be at least as wide as the longest placeholder below.
const addAccountFieldWidth = 42

func (m Model) newAddAccountForm() [3]textinput.Model {
	var fields [3]textinput.Model

	jidInput := textinput.New()
	jidInput.Placeholder = "user@server"
	jidInput.Prompt = "JID:      "
	jidInput.KeyMap = m.keys.TextInputKeys
	jidInput.SetWidth(addAccountFieldWidth)
	applyTextInputStyles(&jidInput, m.styles.colors)
	jidInput.Focus()
	fields[0] = jidInput

	passInput := textinput.New()
	passInput.Placeholder = "(leave blank to use password_cmd/keyring)"
	passInput.Prompt = "Password: "
	passInput.EchoMode = textinput.EchoPassword
	passInput.KeyMap = m.keys.TextInputKeys
	passInput.SetWidth(addAccountFieldWidth)
	applyTextInputStyles(&passInput, m.styles.colors)
	fields[1] = passInput

	gpgInput := textinput.New()
	gpgInput.Placeholder = "(optional) own gpg key fingerprint"
	gpgInput.Prompt = "GPG key:  "
	gpgInput.KeyMap = m.keys.TextInputKeys
	gpgInput.SetWidth(addAccountFieldWidth)
	applyTextInputStyles(&gpgInput, m.styles.colors)
	fields[2] = gpgInput

	return fields
}

// ── Init ──────────────────────────────────────────────────────────────────────

func (m Model) Init() tea.Cmd {
	return textinput.Blink
}

// ── Sizing ────────────────────────────────────────────────────────────────────

// setSelectedView switches the focused view and resizes accordingly — the
// footer's row count (and so m.height) is view-dependent, so every focus
// change must go through this instead of a bare assignment.
func (m *Model) setSelectedView(v selectedView) {
	m.selectedView = v
	m.updateSizes()
}

func (m *Model) updateSizes() {
	fl := footerLineCount(m.keys.helpHint(m.selectedView), max(1, m.width-2), footerMaxLines)
	m.height = max(0, m.termHeight-fl-footerMarginTop)

	sw := m.sidebarWidth()
	cw := m.chatAreaWidth()
	ih := m.inputAreaHeight()

	m.chats.SetHeight(max(0, m.height-sidebarStatusHeight))
	m.chats.SetWidth(sw)

	m.input.SetWidth(m.inputFieldWidth())
	m.viewport.SetWidth(cw)
	m.viewport.SetHeight(max(0, m.height-ih-chatStatusHeight))
}

// inputFieldWidth is the text field's own width — chatAreaWidth minus the
// input box's Padding(0,1) border and, when the send button is drawn,
// the room it needs beside the field. Used both to size the actual
// textinput (here) and to lay out its rendered row in View() — kept in one
// place so those two can't drift out of sync and misalign the cursor.
func (m Model) inputFieldWidth() int {
	w := m.chatAreaWidth() - 2 // -2 for Padding(0,1) on the input box
	if m.mouseEnabled {
		w -= sendButtonWidth // room for the send button beside it
	}
	return max(0, w)
}

const sidebarMinWidth = 20

// sidebarMaxWidth caps how wide a user-drag can push the sidebar, leaving
// at least this much room for the chat area.
func (m Model) sidebarMaxWidth() int {
	return max(sidebarMinWidth, m.width-20)
}

func (m Model) sidebarWidth() int {
	if m.sidebarWidthOverride > 0 {
		return min(max(m.sidebarWidthOverride, sidebarMinWidth), m.sidebarMaxWidth())
	}
	const maxWidth = 36
	w := m.width / 4
	if w < sidebarMinWidth {
		w = sidebarMinWidth
	}
	if w > maxWidth {
		w = maxWidth
	}
	return min(w, m.width)
}
func (m Model) chatAreaWidth() int { return m.width - m.sidebarWidth() - 1 }

// inputAreaHeight accounts for the optional reply-hint / reacting-hint line.
func (m Model) inputAreaHeight() int {
	if m.replyToIdx >= 0 || m.reactingMsgIdx >= 0 {
		return 3 // top border + hint line + input line
	}
	return 2 // top border + input line
}
