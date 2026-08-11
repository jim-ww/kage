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

// newTestModelWithMessages builds a model with a single open chat containing
// msgs, for tests exercising selection/search over message content.
func newTestModelWithMessages(msgs []Message) Model {
	m := newTestModelWithSender(nil, nil)
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

// TestSearchChatFindsAndCyclesMatches guards actionSearchChat/
// updateSearchMatches: typing a query selects the nearest match at/after the
// current selection, and enter cycles forward through the rest, wrapping
// back to the first after the last.
func TestSearchChatFindsAndCyclesMatches(t *testing.T) {
	m := newTestModelWithMessages([]Message{
		{Content: "hello there"},
		{Content: "nothing to see"},
		{Content: "hello again"},
	})
	m.selectedMsg = 1

	next, _ := m.Update(tea.KeyPressMsg{Code: '/', Mod: tea.ModCtrl})
	m = next.(Model)
	if !m.searchingChat {
		t.Fatal("searchingChat = false after Ctrl+/, want true")
	}

	for _, r := range "hello" {
		next, _ := m.Update(keyText(string(r)))
		m = next.(Model)
	}
	if len(m.searchMatches) != 2 {
		t.Fatalf("searchMatches = %v, want 2 matches", m.searchMatches)
	}
	if m.selectedMsg != 2 {
		t.Fatalf("selectedMsg = %d, want 2 (nearest match at/after selectedMsg=1)", m.selectedMsg)
	}

	next, _ = m.Update(keyText("enter"))
	m = next.(Model)
	if m.selectedMsg != 0 {
		t.Fatalf("selectedMsg after enter = %d, want 0 (wrapped to first match)", m.selectedMsg)
	}

	next, _ = m.Update(keyText("esc"))
	m = next.(Model)
	if m.searchingChat {
		t.Fatal("searchingChat still true after esc, want false")
	}
	if m.selectedMsg != 0 {
		t.Fatalf("selectedMsg after esc = %d, want unchanged at 0", m.selectedMsg)
	}
}
