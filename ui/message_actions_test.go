package ui

import (
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

type fakeSuccessSender struct{}

func (f *fakeSuccessSender) Send(int, string, string, SendOptions) (string, error) {
	return "msg-id", nil
}
func (f *fakeSuccessSender) SetTyping(int, string, bool) error       { return nil }
func (f *fakeSuccessSender) MarkRetracted(int, string, string) error { return nil }

// TestSendCurrentInputMarksLocalEchoEncrypted guards against the local
// optimistic echo of a just-sent message silently reporting itself as
// plaintext regardless of the chat's actual encryption mode - adapter.go's
// Send only succeeds without falling back to plaintext, so a successful send
// under a configured encryption mode means the message really did go out
// encrypted, but this in-memory copy has no other way to know that before
// the next reload from storage.
func TestSendCurrentInputMarksLocalEchoEncrypted(t *testing.T) {
	tests := []struct {
		mode          string
		wantEncrypted bool
		wantMethod    string
	}{
		{mode: "omemo-v1", wantEncrypted: true, wantMethod: "omemo-v1"},
		{mode: "omemo-v2", wantEncrypted: true, wantMethod: "omemo-v2"},
		{mode: "gpg", wantEncrypted: true, wantMethod: "gpg"},
		{mode: "none", wantEncrypted: false, wantMethod: ""},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			sender := &fakeSuccessSender{}
			m := newTestModelWithSender(sender, nil)
			chat := Chat{Address: "bob@example.test", EncryptionMode: tt.mode}
			m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
			if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
				_ = cmd()
			}
			m.chats.Select(0)
			m.input.SetValue("hello")

			m.sendCurrentInput()

			msgs := m.currentMessages()
			if len(msgs) != 1 {
				t.Fatalf("currentMessages() has %d entries, want 1", len(msgs))
			}
			got := msgs[0]
			if got.Encrypted != tt.wantEncrypted {
				t.Errorf("Encrypted = %v, want %v", got.Encrypted, tt.wantEncrypted)
			}
			if got.EncMethod != tt.wantMethod {
				t.Errorf("EncMethod = %q, want %q", got.EncMethod, tt.wantMethod)
			}
		})
	}
}

// fakeHistorySearcher is a stub HistorySearcher for testing search-in-chat
// without any real storage/decrypt dependency: SearchHistory just returns
// whatever messages/matches were configured for the query it's asked about.
type fakeHistorySearcher struct {
	messages []Message
	matches  []int
}

func (f *fakeHistorySearcher) SearchHistory(accountIdx int, to, query string) tea.Cmd {
	return func() tea.Msg {
		return HistorySearchResultMsg{AccountIdx: accountIdx, From: to, Query: query, Messages: f.messages, Matches: f.matches}
	}
}

// newTestModelWithMessages builds a model with a single open chat containing
// msgs (as the chat's currently-loaded window) and searcher wired up as its
// HistorySearcher, for tests exercising search-in-chat.
func newTestModelWithMessages(msgs []Message, searcher HistorySearcher) Model {
	m := newTestModelWithSender(nil, nil)
	m.historySearcher = searcher
	chat := Chat{Address: "bob@example.test"}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{0: msgs}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.chats.Select(0)
	m.selectedView = viewChat
	m.refreshViewport()
	return m
}

// TestSearchChatOpensPromptRunsSearchAndJumpsToResult guards the full
// search-in-chat flow: Ctrl+/ opens the query prompt, enter submits it and
// runs HistorySearcher.SearchHistory (whose result — the chat's entire
// history, since a real implementation scans everything, plus which indices
// matched — arrives as HistorySearchResultMsg), the results popup opens
// showing those matches, and picking one loads it as the chat's in-memory
// window with the selection on the matched message.
func TestSearchChatOpensPromptRunsSearchAndJumpsToResult(t *testing.T) {
	fullHistory := []Message{
		{Content: "hello there"},
		{Content: "nothing to see"},
		{Content: "hello again"},
	}
	searcher := &fakeHistorySearcher{messages: fullHistory, matches: []int{0, 2}}
	m := newTestModelWithMessages(fullHistory[:1], searcher)

	next, _ := m.Update(tea.KeyPressMsg{Code: '/', Mod: tea.ModCtrl})
	m = next.(Model)
	if !m.searchingChat {
		t.Fatal("searchingChat = false after Ctrl+/, want true")
	}

	for _, r := range "hello" {
		next, _ := m.Update(keyText(string(r)))
		m = next.(Model)
	}

	next, cmd := m.Update(keyText("enter"))
	m = next.(Model)
	if m.searchingChat {
		t.Fatal("searchingChat still true after submitting, want false")
	}
	if m.searchResults == nil || !m.searchResults.busy {
		t.Fatal("searchResults not opened in its busy state after submitting")
	}
	resultMsg := nonIdleCmd(cmd)
	if resultMsg == nil {
		t.Fatal("submitting the query returned no cmd to run the search")
	}

	next, _ = m.Update(resultMsg)
	m = next.(Model)
	if m.searchResults == nil || m.searchResults.busy {
		t.Fatalf("searchResults still busy after HistorySearchResultMsg arrived: %+v", m.searchResults)
	}
	if len(m.searchResults.matches) != 2 {
		t.Fatalf("searchResults.matches = %v, want 2 matches", m.searchResults.matches)
	}

	next, _ = m.Update(keyText("enter"))
	m = next.(Model)
	if m.searchResults != nil {
		t.Fatal("searchResults still open after picking a result")
	}
	if m.selectedMsg != 0 {
		t.Fatalf("selectedMsg after picking first result = %d, want 0", m.selectedMsg)
	}
	if got := m.accounts[0].Messages[0]; len(got) != len(fullHistory) {
		t.Fatalf("chat's loaded window has %d messages, want %d (the full history)", len(got), len(fullHistory))
	}
}
