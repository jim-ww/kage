package ui

import (
	"testing"

	"charm.land/bubbles/v2/list"
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
