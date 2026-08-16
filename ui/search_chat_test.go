package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// fakeHistoryLoader stands in for a real HistoryLoader: records the anchor
// each LoadHistoryWindow call was made with and replies with a canned
// response — used to test the ui-side glue (building the right anchor,
// applying the response) without needing real storage.
type fakeHistoryLoader struct {
	*fakeSuccessSender
	lastAnchor *HistoryAnchor
	response   HistoryWindowMsg
}

func (f *fakeHistoryLoader) LoadHistoryWindow(accountIdx int, to string, anchor *HistoryAnchor) tea.Cmd {
	f.lastAnchor = anchor
	resp := f.response
	resp.AccountIdx = accountIdx
	resp.From = to
	return func() tea.Msg { return resp }
}

// TestLoadSearchResultFiresAnchoredWindowLoad guards loadSearchResult's
// anchor-based reload: it must anchor the HistoryLoader request on the
// picked match (not dump the search's entire decrypted history into the
// chat's in-memory window directly), and the response's window/HasOlder
// must land in Account state with the selection following the matched
// message by ID.
func TestLoadSearchResultFiresAnchoredWindowLoad(t *testing.T) {
	fullHistory := make([]Message, 1000)
	for i := range fullHistory {
		fullHistory[i] = Message{ID: fmt.Sprintf("m%d", i), StoreID: int64(i + 1), Content: "msg"}
	}
	fullHistory[500].Content = "needle"

	windowed := make([]Message, 100)
	copy(windowed, fullHistory[450:550])

	loader := &fakeHistoryLoader{
		fakeSuccessSender: &fakeSuccessSender{},
		response:          HistoryWindowMsg{Messages: windowed, HasOlder: true, HasNewer: true},
	}
	m := newTestModelWithMessages(fullHistory[:1], &fakeHistorySearcher{})
	m.historyLoader = loader
	m.maxMessagesPerChat = 100
	m.searchResults = &searchResultsState{
		accountIdx: 0, chatAddress: "bob@example.test",
		messages: fullHistory, matches: []int{500},
	}

	cmd := m.loadSearchResult(500)
	if cmd == nil {
		t.Fatal("loadSearchResult returned a nil cmd")
	}
	if loader.lastAnchor == nil || loader.lastAnchor.StoreID != fullHistory[500].StoreID {
		t.Fatalf("LoadHistoryWindow anchor = %+v, want StoreID %d (the matched message)", loader.lastAnchor, fullHistory[500].StoreID)
	}

	updated, _, handled := m.handleEventMsg(cmd())
	if !handled {
		t.Fatal("HistoryWindowMsg was not handled")
	}

	got := updated.accounts[0].Messages[0]
	if len(got) != 100 {
		t.Fatalf("loaded window has %d messages, want the 100 the loader responded with", len(got))
	}
	if got[updated.selectedMsg].Content != "needle" {
		t.Fatalf("selectedMsg = %d does not point at the matched message: %+v", updated.selectedMsg, got[updated.selectedMsg])
	}
	if !updated.accounts[0].HistoryMore[0] {
		t.Fatal("HistoryMore should be true — the loader reported HasOlder")
	}
}

// TestSearchResultsAuthorFilterCycles guards the 'a' keybind in the
// search-results popup: it cycles all -> me -> them -> all, narrowing which
// matches are shown/paginated/selectable without touching the underlying
// (unfiltered) match set searchResults.matches itself.
func TestSearchResultsAuthorFilterCycles(t *testing.T) {
	fullHistory := []Message{
		{Content: "hello from them", IsMe: false},
		{Content: "hello from me", IsMe: true},
		{Content: "hello again them", IsMe: false},
	}
	m := newTestModelWithMessages(fullHistory[:1], &fakeHistorySearcher{})
	m.searchResults = &searchResultsState{
		accountIdx: 0, chatAddress: "bob@example.test", query: "hello",
		messages: fullHistory, matches: []int{0, 1, 2},
	}

	if got := m.searchResults.filteredMatches(); len(got) != 3 {
		t.Fatalf("filteredMatches() (all) = %v, want 3 matches", got)
	}

	next, _ := m.Update(keyText("a"))
	m = next.(Model)
	if m.searchResults.author != authorFilterMe {
		t.Fatalf("author after one 'a' press = %v, want authorFilterMe", m.searchResults.author)
	}
	if got := m.searchResults.filteredMatches(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("filteredMatches() (me) = %v, want [1]", got)
	}

	next, _ = m.Update(keyText("a"))
	m = next.(Model)
	if m.searchResults.author != authorFilterThem {
		t.Fatalf("author after two 'a' presses = %v, want authorFilterThem", m.searchResults.author)
	}
	if got := m.searchResults.filteredMatches(); len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Fatalf("filteredMatches() (them) = %v, want [0 2]", got)
	}

	next, _ = m.Update(keyText("a"))
	m = next.(Model)
	if m.searchResults.author != authorFilterAll {
		t.Fatalf("author after three 'a' presses = %v, want authorFilterAll (wrapped)", m.searchResults.author)
	}
	if len(m.searchResults.matches) != 3 {
		t.Fatalf("underlying matches = %v, want unchanged at 3", m.searchResults.matches)
	}
}

// TestSearchResultsPopupFixedFootprint guards the popup's fixed footprint:
// busy, error, no-matches, and an actual (partial) results page must all
// render the same number of lines and the same max line width, so the
// popup never visibly resizes as its state changes underneath the user.
func TestSearchResultsPopupFixedFootprint(t *testing.T) {
	m := newTestModelWithMessages(nil, &fakeHistorySearcher{})
	m.width, m.height = 100, 30
	m.updateSizes()

	dims := func(sr *searchResultsState) (lines, width int) {
		m.searchResults = sr
		body := m.searchResultsPrompt()
		for _, line := range strings.Split(body, "\n") {
			lines++
			if w := lipgloss.Width(line); w > width {
				width = w
			}
		}
		return
	}

	busyLines, busyWidth := dims(&searchResultsState{query: "q", busy: true})
	errLines, errWidth := dims(&searchResultsState{query: "q", err: "boom"})
	emptyLines, emptyWidth := dims(&searchResultsState{query: "q", messages: nil, matches: nil})

	fullHistory := []Message{
		{Content: "one"}, {Content: "two"}, {Content: "three"},
	}
	resultsLines, resultsWidth := dims(&searchResultsState{
		query: "q", messages: fullHistory, matches: []int{0, 1, 2}, peerName: "bob",
	})

	filteringSr := &searchResultsState{query: "q", messages: fullHistory, matches: []int{0, 1, 2}, peerName: "bob"}
	filteringSr.filterInput = newSearchResultsFilterInput(m, "a much much longer filter query than any of the others")
	filteringSr.filtering = true
	filterLines, filterWidth := dims(filteringSr)

	if busyLines != errLines || errLines != emptyLines || emptyLines != resultsLines || resultsLines != filterLines {
		t.Fatalf("line counts differ across states: busy=%d err=%d empty=%d results=%d filtering=%d",
			busyLines, errLines, emptyLines, resultsLines, filterLines)
	}
	if busyWidth != errWidth || errWidth != emptyWidth || emptyWidth != resultsWidth || resultsWidth != filterWidth {
		t.Fatalf("max widths differ across states: busy=%d err=%d empty=%d results=%d filtering=%d",
			busyWidth, errWidth, emptyWidth, resultsWidth, filterWidth)
	}
}

// TestSearchResultsFuzzyFilter guards the '/' sub-mode: it opens a text
// input, typing live-narrows the visible matches via typo-tolerant
// (Levenshtein) matching against message content, and esc/enter stop
// editing without closing the whole results popup.
func TestSearchResultsFuzzyFilter(t *testing.T) {
	fullHistory := []Message{
		{Content: "I will receive the package tomorrow"},
		{Content: "completely unrelated message"},
		{Content: "another receive notice"},
	}
	m := newTestModelWithMessages(fullHistory[:1], &fakeHistorySearcher{})
	m.searchResults = &searchResultsState{
		accountIdx: 0, chatAddress: "bob@example.test", query: "e",
		messages: fullHistory, matches: []int{0, 1, 2},
	}

	next, _ := m.Update(keyText("/"))
	m = next.(Model)
	if !m.searchResults.filtering {
		t.Fatal("filtering = false after '/', want true")
	}

	for _, r := range "recieve" { // deliberate typo, should still fuzzy-match "receive"
		next, _ := m.Update(keyText(string(r)))
		m = next.(Model)
	}
	if got := m.searchResults.filteredMatches(); len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Fatalf("filteredMatches() after typo-query = %v, want [0 2]", got)
	}

	next, _ = m.Update(keyText("esc"))
	m = next.(Model)
	if m.searchResults.filtering {
		t.Fatal("filtering still true after esc, want false")
	}
	if m.searchResults == nil {
		t.Fatal("esc while filtering closed the whole popup, want only filtering mode to close")
	}
	if m.searchResults.filterQuery != "recieve" {
		t.Fatalf("filterQuery after esc = %q, want it preserved as %q", m.searchResults.filterQuery, "recieve")
	}

	next, _ = m.Update(keyText("esc"))
	m = next.(Model)
	if m.searchResults != nil {
		t.Fatal("second esc (not filtering) should close the whole popup")
	}
}
