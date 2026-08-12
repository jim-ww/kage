package ui

import (
	"errors"
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

var errTestSend = errors.New("simulated send failure")

type fakeSuccessSender struct{}

func (f *fakeSuccessSender) Send(int, string, string, SendOptions) (string, error) {
	return "msg-id", nil
}
func (f *fakeSuccessSender) SetTyping(int, string, bool) error       { return nil }
func (f *fakeSuccessSender) MarkRetracted(int, string, string) error { return nil }
func (f *fakeSuccessSender) DeleteQueued(int, string) error          { return nil }

// fakeErrSender always fails/queues Send with a fixed err, for exercising
// sendCurrentInput's non-success paths.
type fakeErrSender struct{ err error }

func (f *fakeErrSender) Send(int, string, string, SendOptions) (string, error) { return "", f.err }
func (f *fakeErrSender) SetTyping(int, string, bool) error                     { return nil }
func (f *fakeErrSender) MarkRetracted(int, string, string) error               { return nil }
func (f *fakeErrSender) DeleteQueued(int, string) error                        { return nil }

// fakeDeleteQueuedSender records every DeleteQueued call, for verifying
// Ctrl+Shift+D on a Pending message goes through the sender rather than
// just quietly disappearing from the local view.
type fakeDeleteQueuedSender struct {
	lastAccountIdx int
	lastLocalID    string
	calls          int
	err            error
}

func (f *fakeDeleteQueuedSender) Send(int, string, string, SendOptions) (string, error) {
	return "", nil
}
func (f *fakeDeleteQueuedSender) SetTyping(int, string, bool) error       { return nil }
func (f *fakeDeleteQueuedSender) MarkRetracted(int, string, string) error { return nil }
func (f *fakeDeleteQueuedSender) DeleteQueued(accountIdx int, localID string) error {
	f.calls++
	f.lastAccountIdx, f.lastLocalID = accountIdx, localID
	return f.err
}

// TestDeleteUnsentMessageRemovesItOutright checks that confirming a delete
// (Ctrl+Shift+D, then y) on a still-Pending or a Failed message - both never
// actually sent, both backed by an outbox row - calls
// MessageSender.DeleteQueued and removes the message from local history
// entirely, unlike a normal delete, which only flags Retracted and keeps
// the row: a message that was never actually sent has no server-side copy
// worth preserving a record of.
func TestDeleteUnsentMessageRemovesItOutright(t *testing.T) {
	for _, tt := range []struct {
		name   string
		target Message
	}{
		{"pending", Message{Author: "me", Content: "queued reply", IsMe: true, Pending: true, LocalID: "local-1"}},
		{"failed", Message{Author: "me", Content: "failed reply", IsMe: true, Failed: true, LocalID: "local-1"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sender := &fakeDeleteQueuedSender{}
			m := newTestModelWithSender(sender, nil)
			m.selectedView = viewChat
			chat := Chat{Address: "bob@example.test"}
			msgs := []Message{{Author: "Bob", Content: "hi"}, tt.target}
			m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{0: msgs}}}
			if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
				_ = cmd()
			}
			m.selectedMsg = 1
			m.confirmTarget = confirmDeleteMessage

			next, _, handled := m.updateKeyMsg(keyCode('y'))
			if !handled {
				t.Fatal("confirm-yes key was not handled")
			}
			m = next

			if sender.calls != 1 {
				t.Fatalf("DeleteQueued calls = %d, want 1", sender.calls)
			}
			if sender.lastLocalID != "local-1" {
				t.Errorf("DeleteQueued LocalID = %q, want %q", sender.lastLocalID, "local-1")
			}

			got := m.currentMessages()
			if len(got) != 1 {
				t.Fatalf("currentMessages() has %d entries, want 1 (unsent message removed outright)", len(got))
			}
			if got[0].Content != "hi" {
				t.Errorf("remaining message = %+v, want the other (already-sent) one", got[0])
			}
			if m.confirmTarget != confirmNone {
				t.Errorf("confirmTarget = %v, want confirmNone", m.confirmTarget)
			}
		})
	}
}

// TestSendCurrentInputNeverEchoesWithoutASend guards against the bug where a
// message the app never actually handed to MessageSender.Send (chat/sender
// unavailable) still showed up in the local chat history as if it had been
// sent — indistinguishable on screen from a message that really went out,
// and permanently missing on the recipient's side since nothing was ever
// transmitted. No local message must be created in that case, and the
// problem must be surfaced instead of silently swallowed.
func TestSendCurrentInputNeverEchoesWithoutASend(t *testing.T) {
	m := newTestModelWithSender(nil, nil) // sender == nil
	chat := Chat{Address: "bob@example.test"}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.chats.Select(0)
	m.input.SetValue("hello")

	cmd := m.sendCurrentInput()

	if msgs := m.currentMessages(); len(msgs) != 0 {
		t.Fatalf("currentMessages() has %d entries, want 0 (nothing was ever sent)", len(msgs))
	}
	if cmd == nil {
		t.Fatal("sendCurrentInput() returned a nil cmd, want a notification surfacing the failure")
	}
	if got := m.input.Value(); got != "hello" {
		t.Fatalf("input.Value() = %q, want the typed text preserved for a retry", got)
	}
}

// TestSendCurrentInputMarksQueuedPending guards against a queued (offline)
// send being locally echoed as if fully delivered - MessageSender.Send
// returning ErrQueued must leave the message visibly Pending (not encrypted,
// not carrying a real ID) until MessageSendResolvedMsg reconciles it, never
// indistinguishable from an actually-sent message.
func TestSendCurrentInputMarksQueuedPending(t *testing.T) {
	m := newTestModelWithSender(&fakeErrSender{err: ErrQueued}, nil)
	chat := Chat{Address: "bob@example.test", EncryptionMode: "omemo-v1"}
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
	if !got.Pending {
		t.Error("Pending = false, want true for a queued send")
	}
	if got.Failed {
		t.Error("Failed = true, want false for a queued (not failed) send")
	}
	if got.ID != "" {
		t.Errorf("ID = %q, want empty until MessageSendResolvedMsg reports the real one", got.ID)
	}
	if got.Encrypted {
		t.Error("Encrypted = true, want false - the message hasn't actually been sent/encrypted yet")
	}
	if got.LocalID == "" {
		t.Error("LocalID is empty, want a correlation key for MessageSendResolvedMsg to find this message again")
	}
}

// TestMessageSendResolvedReconcilesPendingMessage checks that a
// MessageSendResolvedMsg (adapter.flushOutbox actually attempting a queued
// send) finds the right placeholder by LocalID and clears Pending, filling
// in the real ID on success or flipping to Failed on error - the queued send
// must never leave the placeholder permanently stuck showing Pending once
// its outcome is actually known.
func TestMessageSendResolvedReconcilesPendingMessage(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		m := newTestModelWithSender(&fakeErrSender{err: ErrQueued}, nil)
		chat := Chat{Address: "bob@example.test"}
		m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
		if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
			_ = cmd()
		}
		m.chats.Select(0)
		m.input.SetValue("hello")
		m.sendCurrentInput()
		localID := m.currentMessages()[0].LocalID

		next, _, handled := m.handleEventMsg(MessageSendResolvedMsg{
			AccountIdx: m.currentAccount,
			To:         chat.Address,
			LocalID:    localID,
			ID:         "real-stanza-id",
			Encrypted:  true,
			EncMethod:  "omemo-v1",
		})
		if !handled {
			t.Fatal("MessageSendResolvedMsg was not handled")
		}
		m = next

		got := m.currentMessages()[0]
		if got.Pending {
			t.Error("Pending = true after resolution, want false")
		}
		if got.ID != "real-stanza-id" {
			t.Errorf("ID = %q, want %q", got.ID, "real-stanza-id")
		}
		if !got.Encrypted || got.EncMethod != "omemo-v1" {
			t.Errorf("Encrypted/EncMethod = %v/%q, want true/omemo-v1", got.Encrypted, got.EncMethod)
		}
	})

	t.Run("failure", func(t *testing.T) {
		m := newTestModelWithSender(&fakeErrSender{err: ErrQueued}, nil)
		chat := Chat{Address: "bob@example.test"}
		m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
		if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
			_ = cmd()
		}
		m.chats.Select(0)
		m.input.SetValue("hello")
		m.sendCurrentInput()
		localID := m.currentMessages()[0].LocalID

		next, cmd, handled := m.handleEventMsg(MessageSendResolvedMsg{
			AccountIdx: m.currentAccount,
			To:         chat.Address,
			LocalID:    localID,
			Err:        "connection refused",
		})
		if !handled {
			t.Fatal("MessageSendResolvedMsg was not handled")
		}
		if cmd == nil {
			t.Fatal("expected a notification cmd for the failed queued send")
		}
		m = next

		got := m.currentMessages()[0]
		if got.Pending {
			t.Error("Pending = true after resolution, want false")
		}
		if !got.Failed {
			t.Error("Failed = false, want true")
		}
	})
}

// TestSendCurrentInputMarksRealFailure guards against a genuine send error
// (not queued) being silently dropped or shown as delivered - the typed
// text must still appear (nothing lost) but flagged Failed, and the error
// must be surfaced via a notification.
func TestSendCurrentInputMarksRealFailure(t *testing.T) {
	m := newTestModelWithSender(&fakeErrSender{err: errTestSend}, nil)
	chat := Chat{Address: "bob@example.test"}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.chats.Select(0)
	m.input.SetValue("hello")

	cmd := m.sendCurrentInput()

	msgs := m.currentMessages()
	if len(msgs) != 1 {
		t.Fatalf("currentMessages() has %d entries, want 1", len(msgs))
	}
	got := msgs[0]
	if !got.Failed {
		t.Error("Failed = false, want true for a real send error")
	}
	if got.Pending {
		t.Error("Pending = true, want false for a real (non-queued) failure")
	}
	if got.Content != "hello" {
		t.Errorf("Content = %q, want the typed text preserved", got.Content)
	}
	if cmd == nil {
		t.Fatal("sendCurrentInput() returned a nil cmd, want a notification surfacing the error")
	}
}

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
