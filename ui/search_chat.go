package ui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jim-ww/kage/ui/datepicker"
	"github.com/jim-ww/kage/ui/modal"
)

// dateJump is a jump to the start of day in one chat, waiting on the
// history window jumpToDate asked for.
type dateJump struct {
	accountIdx  int
	chatAddress string
	day         time.Time
}

// newSearchDatePicker is the search's go-to-date calendar, on today.
// Future days are disabled: there's nothing there to jump to.
func (m Model) newSearchDatePicker() datepicker.Model {
	p := datepicker.New(time.Now())
	p.Max = time.Now()
	applyDatePickerStyles(&p, m.styles.colors)
	return p
}

// jumpToDate loads the current chat's history from the start of day on and
// lands on its first message there (see landOnDate) - or on the newest
// before it, if nothing was said that day or since.
func (m *Model) jumpToDate(day time.Time) tea.Cmd {
	chat, ok := m.currentChat()
	chatIdx := m.currentChatIndex()
	if !ok || chatIdx < 0 || m.historyLoader == nil {
		return nil
	}
	// StoreID 0 sorts before every message at that second, so the window's
	// newer half starts with the day's first message.
	anchor := &HistoryAnchor{Delay: day.Unix(), StoreID: 0}
	cmd := m.historyLoader.LoadHistoryWindow(m.currentAccount, chat.Address, anchor)
	if cmd == nil {
		return nil // see markHistoryWindowLoading
	}
	m.markHistoryWindowLoading(chatIdx, "")
	m.pendingDateJump = &dateJump{accountIdx: m.currentAccount, chatAddress: chat.Address, day: day}
	return cmd
}

// landOnDate selects the first of msgs sent on or after day and scrolls it,
// with the day divider above it, to the top of the chat - or, when nothing
// was, selects the newest message.
func (m *Model) landOnDate(msgs []Message, day time.Time) {
	idx := len(msgs) - 1
	for i, msg := range msgs {
		if !msg.SentAt.Before(day) {
			idx = i
			break
		}
	}
	m.selectedMsg = idx
	m.refreshViewport()
	if idx >= 0 && idx < len(m.msgOffsets) {
		m.viewport.SetYOffset(m.msgOffsets[idx])
	}
}

// searchResultsModalID finds the open search results, to fill them when the
// search is done.
const searchResultsModalID = "search-results"

// searchResultsBody is the search-in-chat results: opened busy while
// HistorySearcher.SearchHistory scans the chat's whole persisted history,
// then a list of the messages that matched - narrowed by author (a cycles
// all/me/peer) and a typo-tolerant filter (/ asks for it) - picking one
// jumps the chat to it (see loadSearchResult). messages is the chat's
// whole history, so a picked match has something to anchor that load on.
type searchResultsBody struct {
	*modal.Menu
	accountIdx  int
	chatAddress string
	peerName    string // the chat's display name, for authorFilter's label
	query       string
	messages    []Message
	matches     []int
	shown       []int // matches narrowed by author and filter: the list's rows
	author      authorFilter
	filter      string
	status      string
	err         string
	m           *Model // for searchResultLabel's formatting only
	width       int
	height      int
}

// What the results act with: ask for a filter (/).
type searchResultsFilter struct{}

func (b *searchResultsBody) Update(msg tea.Msg) (tea.Cmd, modal.Outcome) {
	if b.status != "" || b.err != "" {
		return nil, modal.Continue
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch {
		case matchesLetter(k, 'a'):
			b.author = b.author.next()
			b.relist()
			return nil, modal.Continue
		case matchesLetter(k, '/'):
			return nil, modal.Act(searchResultsFilter{})
		}
	}
	return b.Menu.Update(msg)
}

// header says how the results are narrowed.
func (b *searchResultsBody) header() string {
	line := "author: " + b.author.label(b.peerName)
	if b.filter != "" {
		line += " · filter: " + b.filter
	}
	return b.m.styles.messageMuted.Render(line)
}

// searchResultsRows is how many result rows the list always takes, fewer
// only when the screen is shorter: busy, an error, no matches and a short
// list all keep that footprint, so the frame never jumps under the pointer
// as results arrive or a filter narrows them.
const searchResultsRows = 9

func (b *searchResultsBody) rows() int { return max(1, min(searchResultsRows, b.height-1)) }

func (b *searchResultsBody) View() string {
	var view string
	switch {
	case b.status != "":
		view = b.m.styles.messageMuted.Render(b.status)
	case b.err != "":
		view = b.m.styles.popupDanger.Render("Error: " + b.err)
	default:
		view = b.header() + "\n" + b.Menu.View()
	}
	if pad := 1 + b.rows() - (strings.Count(view, "\n") + 1); pad > 0 {
		view += strings.Repeat("\n", pad)
	}
	return view
}

func (b *searchResultsBody) SetSize(width, height int) {
	b.width, b.height = width, height
	b.Menu.SetSize(width, b.rows())
	b.relist()
}

func (b *searchResultsBody) PreferredWidth() int { return 60 }

func (b *searchResultsBody) Hints() []modal.Hint {
	if b.status != "" || b.err != "" {
		return nil
	}
	return []modal.Hint{
		modal.KeyHint(tea.KeyPressMsg{Code: tea.KeyEnter}, "jump"),
		modal.KeyHint(tea.KeyPressMsg{Code: '/', Text: "/"}, "filter"),
		modal.KeyHint(tea.KeyPressMsg{Code: 'a', Text: "a"}, "author"),
	}
}

// relist narrows matches by author and filter into the list's rows: the
// direction glyph and message, the date at the right.
func (b *searchResultsBody) relist() {
	b.shown = b.shown[:0]
	for _, idx := range b.matches {
		msg := b.messages[idx]
		if b.author != authorFilterAll && msg.IsMe != (b.author == authorFilterMe) {
			continue
		}
		if strings.TrimSpace(b.filter) != "" && !fuzzyContains(msg.Content, b.filter) {
			continue
		}
		b.shown = append(b.shown, idx)
	}
	items := make([]modal.MenuItem, len(b.shown))
	for i, idx := range b.shown {
		text, date := b.m.searchResultLabel(b.messages[idx])
		if b.width > 0 {
			room := max(1, b.width-lipgloss.Width(date)-2)
			text = ansi.Truncate(text, room, "…")
			text += strings.Repeat(" ", max(2, b.width-lipgloss.Width(text)-lipgloss.Width(date)))
		} else {
			text += "  "
		}
		items[i] = modal.MenuItem{Label: text + date}
	}
	b.Empty = "no matches"
	b.SetItems(items...)
}

// authorFilter narrows the search-results popup to messages from one
// participant, cycled with the 'a' key. Only "all"/"me"/<peer> for now,
// matching kage's current 1:1-chat-only model — a chat with more than two
// participants (group chat) would need a picker instead of a 3-way cycle,
// but that's a change local to this filter, not the search itself.
type authorFilter int

const (
	authorFilterAll authorFilter = iota
	authorFilterMe
	authorFilterThem
)

// label names f, using peerName (the chat's display name) for
// authorFilterThem rather than a generic "them" — in a 1:1 chat "them" is
// always one specific, already-known person.
func (f authorFilter) label(peerName string) string {
	switch f {
	case authorFilterMe:
		return "me"
	case authorFilterThem:
		return peerName
	default:
		return "all"
	}
}

func (f authorFilter) next() authorFilter {
	return (f + 1) % 3
}

// searchResultLabel formats a search-result row's two parts: the direction
// glyph (» mine, « theirs) plus truncated message content, and its date —
// returned separately so the caller can right-align the date. Plain text:
// a list row is highlighted whole, and colors inside it would cut the
// highlight short.
func (m Model) searchResultLabel(msg Message) (text, date string) {
	glyph := "«"
	if msg.IsMe {
		glyph = "»"
	}
	content := MessagePreviewContent(msg)
	return glyph + " " + previewText(content, previewLen), searchResultDate(msg.SentAt, time.Now(), m.formatMessageTime(msg.SentAt))
}

// searchResultDate stamps a result row: timePart alone (the chat view's own
// time-of-day format) for a match sent today, prefixed with the calendar day
// for anything older, and with the year too once it isn't obvious anymore.
// Unlike the chat view, results are drawn without day dividers around them,
// so a bare "15:04" would leave a match from months ago unplaceable in time.
func searchResultDate(t, now time.Time, timePart string) string {
	t, now = t.Local(), now.Local()
	switch {
	case sameDay(t, now):
		return timePart
	case t.Year() == now.Year():
		return t.Format("2 Jan") + " " + timePart
	default:
		return t.Format("2 Jan 2006") + " " + timePart
	}
}

// openSearchResults shows the results of searching the current chat for
// query, busy until HistorySearchResultMsg brings them.
func (m *Model) openSearchResults(accountIdx int, chat Chat, query string) tea.Cmd {
	menu := modal.NewMenu()
	menu.Styles.Cursor = onAccent(m.styles.colors.accentCyan)
	menu.Styles.Hover = lipgloss.NewStyle().Foreground(m.styles.colors.accentCyan)
	body := &searchResultsBody{Menu: menu, accountIdx: accountIdx, chatAddress: chat.Address, peerName: chat.Name, query: query, status: "searching…", m: m}
	return m.openModal(&modal.Modal[*Model]{
		ID:    searchResultsModalID,
		Title: "Search: " + query,
		Body:  body,
		OnSubmit: func(m *Model, v any) tea.Cmd {
			return m.loadSearchResult(body.accountIdx, body.chatAddress, body.messages[body.shown[v.(int)]])
		},
		OnAct: func(m *Model, _ any) tea.Cmd {
			return m.openModal(&modal.Modal[*Model]{
				Title: "Filter results",
				Body:  m.newInput(body.filter, "filter results", "/ ", "filter"),
				OnSubmit: func(m *Model, v any) tea.Cmd {
					body.filter = v.(string)
					body.relist()
					return nil
				},
			})
		},
	})
}

// loadSearchResult fires a HistoryLoader request for a fresh window anchored
// on target — the same anchor-based load every other window
// transition uses (see HistoryAnchor's doc comment), just with the anchor
// coming from a search match instead of a scroll edge.
func (m *Model) loadSearchResult(accountIdx int, chatAddress string, target Message) tea.Cmd {
	chatIdx := m.chatIndexByAddress(accountIdx, chatAddress)
	if chatIdx < 0 || m.historyLoader == nil {
		return nil
	}
	anchor := &HistoryAnchor{Delay: target.SentAt.Unix(), StoreID: target.StoreID}
	cmd := m.historyLoader.LoadHistoryWindow(accountIdx, chatAddress, anchor)
	if cmd == nil {
		return nil // see markHistoryWindowLoading
	}
	m.markHistoryWindowLoading(chatIdx, target.ID)
	return cmd
}
