package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// renderSearchChatPopup shows the search-in-chat query prompt: a single text
// field, submitted (enter) to run the search across the chat's entire
// persisted history (see submitSearchChat).
func (m Model) renderSearchChatPopup() string {
	cw := m.chatAreaWidth()
	vh := m.height - m.inputAreaHeight()

	footer := "[enter] search  ·  [esc] cancel"
	body := m.styles.listPopup("Search chat", []string{m.searchInput.View()}, footer)
	popup := m.styles.popupDialog(m.styles.colors.borderA, body)

	return lipgloss.Place(cw, vh, lipgloss.Center, lipgloss.Center, popup)
}

// searchResultsState holds the state of the search-in-chat results popup,
// active while Model.searchResults is non-nil. Opened by submitSearchChat
// once the query prompt is submitted; starts busy while
// HistorySearcher.SearchHistory runs in the background, then holds the
// chat's entire persisted history (Messages) plus which indices into it
// matched the query (Matches) once HistorySearchResultMsg arrives. Only
// Matches — narrowed further by author (see filteredMatches) — are
// shown/paginated as rows; Messages exists so picking one can load the whole
// chat wholesale (see loadSearchResult) without a second round trip.
type searchResultsState struct {
	accountIdx  int
	chatAddress string
	peerName    string // the chat's display name, for authorFilter's label
	query       string
	pagedCursor

	busy     bool
	err      string
	messages []Message
	matches  []int
	author   authorFilter
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

// filteredMatches returns sr.matches narrowed to sr.author.
func (sr *searchResultsState) filteredMatches() []int {
	if sr.author == authorFilterAll {
		return sr.matches
	}
	wantMe := sr.author == authorFilterMe
	out := make([]int, 0, len(sr.matches))
	for _, idx := range sr.matches {
		if sr.messages[idx].IsMe == wantMe {
			out = append(out, idx)
		}
	}
	return out
}

// renderSearchResultsPopup shows the search-in-chat results popup: one row
// per matched message (glyph, truncated content, date), paginated like the
// contact manager/OMEMO device list.
func (m Model) renderSearchResultsPopup() string {
	cw := m.chatAreaWidth()
	vh := m.height - m.inputAreaHeight()

	popup := m.styles.popupDialog(m.styles.colors.borderA, m.searchResultsPrompt())
	popup = m.zone.Mark(zoneSearchResultsPopup, popup)
	return lipgloss.Place(cw, vh, lipgloss.Center, lipgloss.Center, popup)
}

func (m Model) searchResultsPrompt() string {
	sr := m.searchResults
	if sr == nil {
		return ""
	}
	title := fmt.Sprintf("Search: %s", sr.query)
	closeKey := m.keys.SearchChat.Help().Key

	if sr.busy {
		return m.styles.infoPopup(title, []string{"Searching…"}, closeKey)
	}
	if sr.err != "" {
		return m.styles.infoPopup(title, []string{"Error: " + sr.err}, closeKey)
	}
	matches := sr.filteredMatches()
	if len(matches) == 0 {
		return m.styles.infoPopup(title, []string{fmt.Sprintf("No matches (author: %s)", sr.author.label(sr.peerName))}, closeKey)
	}

	start, end := sr.bounds(len(matches))
	page := matches[start:end]
	if sr.cursor >= len(page) {
		sr.cursor = max(0, len(page)-1)
	}
	// Right-align each row's date against the widest text part on this page,
	// so the dates form a straight column regardless of how long each
	// message's truncated preview happens to be.
	texts := make([]string, len(page))
	dates := make([]string, len(page))
	textWidth := 0
	for i, msgIdx := range page {
		texts[i], dates[i] = m.searchResultLabel(sr.messages[msgIdx])
		textWidth = max(textWidth, lipgloss.Width(texts[i]))
	}

	rows := make([]string, 0, len(page)+2)
	for i := range page {
		gap := textWidth - lipgloss.Width(texts[i]) + 2
		label := texts[i] + strings.Repeat(" ", gap) + dates[i]
		rows = append(rows, m.renderRow(zoneSearchResultRow(i), i, sr.cursor, label))
	}

	hint := fmt.Sprintf("a: author (%s) · enter: jump to message", sr.author.label(sr.peerName))
	if pages := openPageCount(len(matches)); pages > 1 {
		hint = fmt.Sprintf("page %d/%d · left/right: page · %s", sr.page+1, pages, hint)
	}
	rows = append(rows, "", hint)

	return m.styles.infoPopup(title, rows, closeKey)
}

// searchResultLabel formats a search-result row's two parts: the direction
// glyph plus truncated message content, and its date — returned separately
// so the caller can right-align the date against the widest text part on
// the page instead of just trailing each row by a fixed gap. The glyph and
// date are colored by sender the same way the chat view's own message
// header is (see renderMessageHeader) so a result list reads consistently
// with the chat it's drawn from; the message text itself stays plain, same
// as a message's body in the chat view.
func (m Model) searchResultLabel(msg Message) (text, date string) {
	style := m.styles.messageNickThem
	glyph := "«"
	if msg.IsMe {
		style = m.styles.messageNickMe
		glyph = "»"
	}
	content := MessagePreviewContent(msg)
	if content == "" && msg.Retracted {
		content = "*deleted*"
	}
	return style.Render(glyph) + " " + previewText(content, previewLen), style.Render(m.formatMessageTime(msg.SentAt))
}

// updateSearchResultsKey handles all input while the search-results popup
// (m.searchResults != nil) is open.
func (m Model) updateSearchResultsKey(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	sr := m.searchResults

	if sr.busy {
		return m, nil, true
	}
	if sr.err != "" {
		if matchesKey(msg, m.keys.Back) || matchesKey(msg, m.keys.ConfirmNo) || matchesKey(msg, m.keys.SearchChat) {
			m.searchResults = nil
		}
		return m, nil, true
	}

	if matchesLetter(msg, 'a') {
		sr.author = sr.author.next()
		sr.cursor, sr.page = 0, 0
		return m, nil, true
	}

	matches := sr.filteredMatches()
	if len(matches) == 0 {
		if matchesKey(msg, m.keys.Back) || matchesKey(msg, m.keys.ConfirmNo) || matchesKey(msg, m.keys.SearchChat) {
			m.searchResults = nil
		}
		return m, nil, true
	}

	start, end := sr.bounds(len(matches))
	rowCount := end - start

	if sr.handleNavKey(msg, rowCount, len(matches)) {
		return m, nil, true
	}

	switch {
	case matchesKey(msg, m.keys.SelectSend):
		if sr.cursor >= 0 && sr.cursor < rowCount {
			m.loadSearchResult(matches[start+sr.cursor])
			m.searchResults = nil
		}
		return m, nil, true
	case matchesKey(msg, m.keys.Back), matchesKey(msg, m.keys.ConfirmNo), matchesKey(msg, m.keys.SearchChat):
		m.searchResults = nil
	}
	return m, nil, true
}

// loadSearchResult loads sr's full (already-fetched) history as the current
// chat's in-memory window — it's already the entire persisted history, so no
// further "load older" fetch is needed for this chat — and moves the
// selection/viewport to msgIdx.
func (m *Model) loadSearchResult(msgIdx int) {
	sr := m.searchResults
	chatIdx := m.chatIndexByAddress(sr.accountIdx, sr.chatAddress)
	if chatIdx < 0 {
		return
	}

	if m.accounts[sr.accountIdx].Messages == nil {
		m.accounts[sr.accountIdx].Messages = make(map[int][]Message)
	}
	m.accounts[sr.accountIdx].Messages[chatIdx] = sr.messages
	if m.accounts[sr.accountIdx].HistoryMore == nil {
		m.accounts[sr.accountIdx].HistoryMore = make(map[int]bool)
	}
	m.accounts[sr.accountIdx].HistoryMore[chatIdx] = false

	if sr.accountIdx != m.currentAccount || chatIdx != m.currentChatIndex() {
		return
	}
	m.selectedMsg = msgIdx
	m.refreshViewportFullScrollTo(msgIdx)
}

// handleSearchResultsClick handles mouse clicks while the search-results
// popup is open: clicking a row moves the cursor there (left click also
// jumps to it); clicking outside the popup closes it.
func (m Model) handleSearchResultsClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	sr := m.searchResults
	if sr.busy || sr.err != "" {
		return m, nil
	}

	if !m.zone.Get(zoneSearchResultsPopup).InBounds(msg) {
		m.searchResults = nil
		return m, nil
	}

	matches := sr.filteredMatches()
	if len(matches) == 0 {
		return m, nil
	}

	start, end := sr.bounds(len(matches))
	if i := m.rowUnderMouse(msg, end-start, zoneSearchResultRow); i >= 0 {
		sr.cursor = i
		if msg.Mouse().Button == tea.MouseLeft {
			m.loadSearchResult(matches[start+i])
			m.searchResults = nil
		}
	}
	return m, nil
}
