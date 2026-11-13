package ui

import (
	"os"
	"time"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/jim-ww/kage/ui/filepicker"
	"github.com/jim-ww/kage/ui/modal"
	"github.com/jim-ww/kage/ui/viewport"
)

// ── Model ─────────────────────────────────────────────────────────────────────

// Model is the top-level Bubble Tea model for the app.
type Model struct {
	width, height int
	termHeight    int // raw terminal rows from the last WindowSizeMsg; height is derived from this minus the footer's actual (view-dependent) row count
	selectedView
	keys   *KeyMap
	theme  Theme
	styles *uiStyles
	zone   *zone.Manager // owns mouse click/scroll regions marked in View(); see ui/mouse.go

	// maxMessagesPerChat caps a chat's loaded messages, trimming the oldest
	// (see appendAndTrim), and is the history window size. <= 0: no cap.
	maxMessagesPerChat int

	// mouseEnabled gates all mouse handling, zone marks and mouse-only
	// elements.
	mouseEnabled bool

	// icons selects icon glyphs for attachments/encryption instead of the
	// plain-text [tag] fallback — on only when the user's config opts in.
	icons bool

	// showEncryptedIcon shows a lock icon/tag next to encrypted messages —
	// off unless the user's config opts in.
	showEncryptedIcon bool

	// useGPG mirrors config's use_gpg: whether gpg encryption is available
	// at all. When off, "gpg" is hidden from the per-chat encryption picker.
	useGPG bool

	// defaultEncryptionMode is a chat's encryption until one is picked.
	defaultEncryptionMode string

	// timeLayout is a time layout for message timestamps; "" for "15:04",
	// dates being on day dividers.
	timeLayout string

	// hover is the zone under the pointer. A pointer, shared with the chat
	// list delegate, which only sees list.Model.
	hover *hoverState

	// hoverGen counts hover changes, see hoverDevicesRevealMsg.
	hoverGen int

	// sidebarWidthOverride is the dragged sidebar width; 0 for the default.
	sidebarWidthOverride int
	// resizingSidebar is set while the sidebar border is dragged.
	resizingSidebar bool
	// sidebarHidden is set while the chat list is toggled off.
	sidebarHidden bool

	// inputHeightOverride is the dragged compose box height, applied as its
	// MinHeight; 0 for the default.
	inputHeightOverride int
	// resizingInput is set while the compose box border is dragged.
	resizingInput bool

	// composeExpanded is set while ToggleComposeExpand grew the compose box;
	// composeHeightBeforeExpand is the height to go back to. Not saved.
	composeExpanded           bool
	composeHeightBeforeExpand int

	// daemon is the kage daemon everything the UI does goes through.
	daemon Daemon

	accounts       []Account
	currentAccount int
	// chats, input and filePicker are pointers: Model is passed by value
	// everywhere, and these are several KB each.
	chats *list.Model

	// sidebarRenderCache memoizes the rendered sidebar, keyed on its inputs'
	// values rather than invalidated where chat-list state changes, so it
	// can't go stale.
	sidebarRenderCache *sidebarCacheEntry
	// viewportFrameCache is renderSidebar's counterpart for the message
	// pane's border/frame — see viewportFrameCacheEntry's doc comment.
	viewportFrameCache *viewportFrameCacheEntry
	// msgRowCache memoizes individual rendered message rows — see
	// msgRowCacheState's doc comment.
	msgRowCache *msgRowCacheState
	// chatListViewCache memoizes the chat list's body, see chatListBody.
	chatListViewCache *chatListCacheEntry

	input    *textarea.Model
	viewport *viewport.Model

	// draftHistory is the compose box's undo stack; draftHistory[draftHistIdx]
	// is what's in it. Reset when it changes other than by typing.
	draftHistory []string
	draftHistIdx int

	// openChatAccountIdx/openChatAddress are the chat whose draft is in
	// m.input, "" for none yet (see swapComposeDraft).
	openChatAccountIdx int
	openChatAddress    string

	// draftSaveGen counts draft changes, see draftSaveMsg.
	draftSaveGen int

	// stashedDraft is the draft put aside while editing a message, restored
	// by restoreStashedDraft.
	stashedDraft *string

	// message interaction state
	selectedMsg int    // index of highlighted message (meaningful in viewViewport)
	editingMsg  msgRef // names the message being edited; zero value while not editing
	// replyTo is the message being replied to, if any. By identity, not
	// index, as is editingMsg: trimming or a history load moves indices.
	replyTo            msgRef
	flashMsgIdx        int             // >= 0 while a message is briefly highlighted (e.g. after jumping to it via a reply quote); -1 otherwise
	expandedMsgs       map[string]bool // keyed by msgKey(msg, idx); true once a long message's collapsed body has been manually expanded
	flashGen           int             // bumped on every flash so a stale flashClearMsg from a superseded flash is ignored
	reactionEmojiUsage map[string]int  // emoji -> times sent as a reaction (in-memory mirror of config.State.ReactionEmojiUsage); ranked and passed to emojipicker.New as its "recent" seed
	// modals are the open modals, the top one taking all input (see
	// ui/modal and ui/modals.go).
	modals                 modal.Stack[*Model]
	filePicker             *filepicker.Model
	pendingAttachments     []pendingAttachment // files staged (via the file picker) to go out with the next sent message; nothing is uploaded until send
	selectedAttachment     int                 // index into pendingAttachments highlighted for Tab/Backspace/ctrl+o; -1 when empty
	msgOffsets             []int               // line offset of each message inside viewport content
	viewportLines          []string            // viewport content split into lines, kept in sync with msgOffsets for refreshViewportSelection's line-range patching
	noticeText             string
	noticeID               int
	noticeDuration         time.Duration                      // how long a notification toast stays visible before auto-dismissing
	transferOrder          []string                           // insertion order of transfers keys, for stable multi-line rendering
	transfers              map[string]FileTransferProgressMsg // active uploads/downloads, keyed by ID (local path for an upload, URL for a download); removed once the transfer's terminal result msg arrives
	finishedTransfers      map[string]bool                    // IDs whose terminal result msg already arrived, so a progress msg racing in afterward (e.g. a final 100% event arriving after the result) doesn't resurrect the entry
	downloadsInFlight      map[string]bool                    // URLs currently being downloaded (open or save), so mashing the open/save key doesn't start a duplicate download of the same file; cleared once the terminal openResultMsg/saveResultMsg for that URL arrives
	attachmentSizes        map[string]int64                   // attachment target -> size fetched via fetchAttachmentSizeCmd (on-demand, see actionInfoMessage) or measured from a local copy
	attachmentSizeFetching map[string]bool                    // attachment targets with a fetchAttachmentSizeCmd in flight, so reopening the info popup doesn't refire it
	attachmentSizeFailed   map[string]bool                    // attachment targets whose HEAD request failed or didn't report a size, so the info popup doesn't keep retrying every time it's reopened

	// double-click detection for messages
	lastClickedMsgIdx int       // index of the last clicked message (for double-click detection)
	lastClickTime     time.Time // time of the last message click (for double-click detection)

	// hiddenChats holds the chats kept out of each account's list, keyed
	// by account index then lowercased bare JID — see ui/hidden_chats.go.
	hiddenChats map[int]map[string]hiddenChat
	// autoUnhideDisabled keeps a hidden chat hidden when a message arrives
	// from it (config's auto_unhide_disabled).
	autoUnhideDisabled bool
	// hoverSelectDisabled: see DisplayOptions.HoverSelectDisabled.
	hoverSelectDisabled bool
	// autofocus: see DisplayOptions.Autofocus.
	autofocus Autofocus
	// landedOnce marks the startup landing on the newest message done (see
	// landedInChat): later reconnects mustn't move the view.
	landedOnce bool
	// audioDevicesPending is set while the audio devices are being listed.
	audioDevicesPending bool

	// focused is whether the terminal has focus. Starts true, for terminals
	// that never report it.
	focused bool

	// idle is set after notifyIdleTimeout without input, so a chat left on
	// screen still gets notified. idleGen counts input, see idleMsg.
	idle    bool
	idleGen int

	// loadingHistoryWindow marks chats with a history load in flight.
	loadingHistoryWindow map[int]bool

	// pendingWindowAnchor is, per chat, the message a history load is
	// anchored on, selected once it arrives.
	pendingWindowAnchor map[int]string

	// pendingOpenChatAddress is the last chat, to open once its account's
	// chats load; cleared after the first try.
	pendingOpenChatAddress string

	// pendingDateJump is the day jumpToDate is loading a history window
	// for; the HistoryWindowMsg answering it scrolls to that day's first
	// message.
	pendingDateJump *dateJump

	// typingActiveTo is the chat we're "composing" to, if any. typingGen
	// counts keystrokes, see typingPauseMsg.
	typingActiveTo string
	typingGen      int

	// peerTypingGen counts each contact's "composing", by peerTypingKey; see
	// peerTypingExpiredMsg.
	peerTypingGen map[string]int

	// chatSwitchGen counts selection moves, see chatSwitchSettledMsg.
	chatSwitchGen int

	// call is the call in progress, or just ended (see ui/call.go).
	call *callUIState
}

// DisplayOptions bundles the message-rendering config toggles.
type DisplayOptions struct {
	Icons              bool   // show icons for attachments/encryption instead of plain-text tags
	TimeLayout         string // custom Go time layout for message timestamps; empty means the default
	MaxMessagesPerChat int    // cap on messages kept per chat in memory/view; <= 0 means no cap
	UseGPG             bool   // whether gpg encryption is available; hides "gpg" from the per-chat encryption picker when off
	ShowEncryptedIcon  bool   // show a lock icon/tag next to encrypted messages
	// DefaultEncryptionMode is a chat's encryption until one is picked; ""
	// for "omemo-v1".
	DefaultEncryptionMode string
	NoticeDuration        time.Duration // how long an in-app notification toast stays visible; <= 0 means the default
	FilePickerDirsFirst   bool          // group directories before files in the attach-file picker regardless of sort order
	// FilePickerSortField/FilePickerSortAscending are the file picker's
	// saved sort (see filepicker.ParseSortField).
	FilePickerSortField     string
	FilePickerSortAscending bool
	// AvatarsDisabled hides the sidebar's avatar panel; negative so the zero
	// value is the default.
	AvatarsDisabled bool
	// AutoUnhideDisabled keeps a hidden chat hidden when a message arrives.
	AutoUnhideDisabled bool
	// HoverSelectDisabled stops hovering a message from selecting it; a
	// click still does. Named the same way: hover selects by default.
	HoverSelectDisabled bool
	// Autofocus is whose new messages get selected and scrolled to.
	Autofocus Autofocus
}

// New builds the initial Model. initialCallState is a call already in
// progress when the TUI attaches.
func New(accounts []Account, startAccount int, keys KeyMap, theme Theme, daemon Daemon, mouseEnabled bool, initialSidebarWidth int, initialSidebarHidden bool, openLastChatAddress string, initialInputHeight int, initialReactionEmojiUsage map[string]int, display DisplayOptions, initialCallState *CallStateMsg) Model {
	setAvatarsEnabled(!display.AvatarsDisabled)
	styles := newUIStyles(theme)
	zm := zone.New()
	zm.SetEnabled(mouseEnabled)
	hv := &hoverState{replyKeyIdx: -1, reactKeyIdx: -1, expandBtnIdx: -1, reactionMsgIdx: -1, reactionIdx: -1}
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
	// The list would quit the program on esc/q; KeyMap handles those.
	l.DisableQuitKeybindings()
	l.InfiniteScrolling = true
	applyChatListStyles(&l, styles.colors)

	ti := textarea.New()
	ti.Placeholder = "message..."
	// On the first line only: repeated down every line of a multi-line
	// message it reads as a column of bullets.
	ti.SetPromptFunc(lipgloss.Width(inputPrompt), func(p textarea.PromptInfo) string {
		if p.LineNumber == 0 {
			return inputPrompt
		}
		return ""
	})
	ti.ShowLineNumbers = false
	ti.KeyMap = keys.InputAreaKeys
	ti.DynamicHeight = true
	ti.MaxContentHeight = composeMaxContentHeight
	ti.MinHeight = 1
	ti.MaxHeight = inputMaxHeight
	if initialInputHeight > 0 {
		ti.MinHeight = initialInputHeight
		ti.MaxHeight = max(inputMaxHeight, initialInputHeight)
	}
	ti.Focus()
	applyTextAreaStyles(&ti, styles.colors)
	picker := filepicker.New()
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		picker.CurrentDirectory = home
	}
	picker.ShowPermissions = false
	picker.DirsFirst = display.FilePickerDirsFirst
	picker.SortField = filepicker.ParseSortField(display.FilePickerSortField)
	picker.SortAscending = display.FilePickerSortAscending
	applyFilePickerStyles(&picker, styles.colors)
	vp := viewport.New()

	noticeDuration := display.NoticeDuration
	if noticeDuration <= 0 {
		noticeDuration = DefaultNoticeDuration
	}

	m := Model{
		selectedView:           viewChat,
		keys:                   &keys,
		theme:                  theme,
		styles:                 styles,
		zone:                   zm,
		modals:                 newModalStack(zm, styles.colors),
		daemon:                 daemon,
		mouseEnabled:           mouseEnabled,
		maxMessagesPerChat:     display.MaxMessagesPerChat,
		icons:                  display.Icons,
		showEncryptedIcon:      display.ShowEncryptedIcon,
		useGPG:                 display.UseGPG,
		defaultEncryptionMode:  display.DefaultEncryptionMode,
		noticeDuration:         noticeDuration,
		timeLayout:             display.TimeLayout,
		hover:                  hv,
		accounts:               accounts,
		autoUnhideDisabled:     display.AutoUnhideDisabled,
		hoverSelectDisabled:    display.HoverSelectDisabled,
		autofocus:              display.Autofocus,
		currentAccount:         startAccount,
		chats:                  &l,
		sidebarRenderCache:     &sidebarCacheEntry{},
		viewportFrameCache:     &viewportFrameCacheEntry{},
		msgRowCache:            newMsgRowCacheState(),
		chatListViewCache:      &chatListCacheEntry{},
		input:                  &ti,
		draftHistory:           []string{""},
		viewport:               &vp,
		selectedAttachment:     -1,
		flashMsgIdx:            -1,
		expandedMsgs:           make(map[string]bool),
		lastClickedMsgIdx:      -1,
		focused:                true,
		openChatAccountIdx:     -1,
		loadingHistoryWindow:   make(map[int]bool),
		pendingOpenChatAddress: openLastChatAddress,
		sidebarWidthOverride:   initialSidebarWidth,
		reactionEmojiUsage:     copyEmojiUsage(initialReactionEmojiUsage),
		sidebarHidden:          initialSidebarHidden,
		inputHeightOverride:    initialInputHeight,
		filePicker:             &picker,
	}
	m.installAccounts(accounts, startAccount)
	if initialCallState != nil {
		m, _ = m.handleCallStateMsg(*initialCallState)
	}
	return m
}

// installAccounts replaces the model's accounts with a daemon snapshot, for
// New and AccountsSnapshotMsg, and updates what derives from them.
func (m *Model) installAccounts(accounts []Account, startAccount int) tea.Cmd {
	m.accounts = accounts
	if startAccount < 0 || startAccount >= len(accounts) {
		startAccount = 0
	}
	m.currentAccount = startAccount

	// Hidden chats are taken out (see ui/hidden_chats.go), and every
	// account's chats sorted: the daemon doesn't sort them.
	for i := range m.accounts {
		m.stripHiddenChats(i)
		m.sortChatsByActivity(i)
	}
	if len(m.accounts) == 0 {
		return nil
	}
	return m.chats.SetItems(m.accounts[m.currentAccount].Chats)
}

// newAddAccountForm builds fresh, empty textinput.Model fields for the
// add-account popup: JID, password (masked), and an optional GPG key ID.

// DefaultNoticeDuration is how long an in-app notification toast stays
// visible before auto-dismissing when DisplayOptions.NoticeDuration is unset.
const DefaultNoticeDuration = 3 * time.Second

// ── Init ──────────────────────────────────────────────────────────────────────

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{textinput.Blink, func() tea.Msg { return openPendingChatMsg{} }, idleTimer(m.idleGen)}
	// Report the starting focus now: Update only reports changes. Not while
	// the chat to open is pending without accounts, which would undo the
	// starting chat runTUI already reported.
	if len(m.accounts) > 0 || m.pendingOpenChatAddress == "" {
		accountJID, chatAddress := m.activeChatKey()
		focused := m.focused && !m.idle
		reporter := m.daemon
		cmds = append(cmds, func() tea.Msg {
			_ = reporter.SetFocusState(accountJID, chatAddress, focused)
			return nil
		})
	}
	return tea.Batch(cmds...)
}
