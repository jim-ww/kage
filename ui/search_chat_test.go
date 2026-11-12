package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
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
	cmd := m.loadSearchResult(0, "bob@example.test", fullHistory[500])
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

// searchResultsWith opens the search results for query over history,
// with matches already in.
func searchResultsWith(t *testing.T, history []Message, matches []int) Model {
	t.Helper()
	m := newTestModelWithMessages(history[:1], &fakeHistorySearcher{})
	m.width, m.termHeight = 100, 30
	m.updateSizes()
	chat, _ := m.currentChat()
	m.openSearchResults(0, chat, "q")
	m, _, _ = m.handleEventMsg(HistorySearchResultMsg{AccountIdx: 0, From: chat.Address, Query: "q", Messages: history, Matches: matches})
	return m
}

func resultsOf(m Model) *searchResultsBody {
	if md := m.modals.Find(searchResultsModalID); md != nil {
		return md.Body.(*searchResultsBody)
	}
	return nil
}

// TestSearchResultsAuthorFilterCycles guards the 'a' key: it cycles all ->
// me -> them -> all, narrowing which matches are listed without touching
// the underlying match set.
func TestSearchResultsAuthorFilterCycles(t *testing.T) {
	history := []Message{
		{Content: "hello from them", IsMe: false},
		{Content: "hello from me", IsMe: true},
		{Content: "hello again them", IsMe: false},
	}
	m := searchResultsWith(t, history, []int{0, 1, 2})
	want := [][]int{{1}, {0, 2}, {0, 1, 2}}
	for i, w := range want {
		m = run(t, m, keyText("a"))
		if got := resultsOf(m).shown; len(got) != len(w) || got[0] != w[0] {
			t.Fatalf("after %d a presses: listed %v, want %v", i+1, got, w)
		}
	}
	if len(resultsOf(m).matches) != 3 {
		t.Fatalf("underlying matches = %v, want unchanged at 3", resultsOf(m).matches)
	}
}

// TestSearchResultsFixedFootprint guards the results' fixed footprint:
// busy, error, no matches and an actual list all take the same rows, so
// the frame never resizes as their state changes underneath the user.
func TestSearchResultsFixedFootprint(t *testing.T) {
	history := []Message{{Content: "one"}, {Content: "two"}, {Content: "three"}}
	m := searchResultsWith(t, history, []int{0, 1, 2})
	body := resultsOf(m)
	lines := func() int { return strings.Count(body.View(), "\n") + 1 }
	results := lines()
	body.status = "searching…"
	busy := lines()
	body.status, body.err = "", "boom"
	failed := lines()
	body.err, body.matches = "", nil
	body.relist()
	empty := lines()
	if busy != results || failed != results || empty != results {
		t.Fatalf("rows differ across states: results=%d busy=%d error=%d empty=%d", results, busy, failed, empty)
	}
}

// TestSearchResultsFuzzyFilter guards '/': it asks for a filter that
// narrows the listed matches by typo-tolerant (Levenshtein) matching
// against message content; esc there leaves the results open.
func TestSearchResultsFuzzyFilter(t *testing.T) {
	history := []Message{
		{Content: "I will receive the package tomorrow"},
		{Content: "completely unrelated message"},
		{Content: "another receive notice"},
	}
	m := searchResultsWith(t, history, []int{0, 1, 2})

	m = run(t, m, keyText("/"))
	if modalTitle(m) != "Filter results" {
		t.Fatalf("/ opened %q, want the filter", modalTitle(m))
	}
	m = run(t, m, keyText("esc"))
	if resultsOf(m) == nil || modalTitle(m) != "Search: q" {
		t.Fatal("esc on the filter closed the results too")
	}

	m = run(t, m, keyText("/"))
	for _, r := range "recieve" { // deliberate typo, should still fuzzy-match "receive"
		m = run(t, m, keyText(string(r)))
	}
	m = run(t, m, keyText("enter"))
	if got := resultsOf(m).shown; len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Fatalf("listed after a typo filter = %v, want [0 2]", got)
	}
	m = run(t, m, keyText("/"))
	if modalInput(t, m).Model.Value() != "recieve" {
		t.Fatalf("the filter reopened as %q, want what was typed before", modalInput(t, m).Model.Value())
	}
}

func TestSearchResultDate(t *testing.T) {
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.Local)
	tests := []struct {
		name string
		sent time.Time
		want string
	}{
		{"today", now.Add(-2 * time.Hour), "10:00"},
		{"yesterday", now.AddDate(0, 0, -1), "30 Sep 12:00"},
		{"earlier this year", time.Date(2026, time.February, 3, 9, 15, 0, 0, time.Local), "3 Feb 09:15"},
		{"previous year", time.Date(2025, time.December, 31, 23, 45, 0, 0, time.Local), "31 Dec 2025 23:45"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := searchResultDate(tc.sent, now, tc.sent.Format("15:04"))
			if got != tc.want {
				t.Fatalf("searchResultDate() = %q, want %q", got, tc.want)
			}
		})
	}
}
