package ui

import "testing"

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
