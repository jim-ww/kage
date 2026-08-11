package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

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

	if busyLines != errLines || errLines != emptyLines || emptyLines != resultsLines {
		t.Fatalf("line counts differ across states: busy=%d err=%d empty=%d results=%d",
			busyLines, errLines, emptyLines, resultsLines)
	}
	if busyWidth != errWidth || errWidth != emptyWidth || emptyWidth != resultsWidth {
		t.Fatalf("max widths differ across states: busy=%d err=%d empty=%d results=%d",
			busyWidth, errWidth, emptyWidth, resultsWidth)
	}
}
