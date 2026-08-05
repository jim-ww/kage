package ui

import (
	"testing"

	"charm.land/bubbles/v2/list"
)

// fakeChatEncryptionSender is a minimal MessageSender + ChatEncryptionSetter
// stub, recording the last SetChatEncryption call.
type fakeChatEncryptionSender struct {
	lastAccountIdx    int
	lastPeerJID, mode string
}

func (f *fakeChatEncryptionSender) Send(int, string, string, SendOptions) (string, error) {
	return "", nil
}
func (f *fakeChatEncryptionSender) SetTyping(int, string, bool) error { return nil }
func (f *fakeChatEncryptionSender) SetChatEncryption(accountIdx int, peerJID, mode string) error {
	f.lastAccountIdx, f.lastPeerJID, f.mode = accountIdx, peerJID, mode
	return nil
}

// TestEncryptionModesIncludesOmemoV1V2 guards the encryption picker's option
// list: forcing a specific OMEMO protocol per chat (rather than only the
// auto-detecting "omemo-auto") must stay selectable from the UI, not just
// via config.toml's omemo_peers override.
func TestEncryptionModesIncludesOmemoV1V2(t *testing.T) {
	want := map[string]bool{"omemo-auto": true, "omemo-v1": true, "omemo-v2": true, "gpg": true, "none": true}
	if len(encryptionModes) != len(want) {
		t.Fatalf("encryptionModes = %v, want exactly %v", encryptionModes, want)
	}
	for _, m := range encryptionModes {
		if !want[m] {
			t.Errorf("unexpected encryption mode %q", m)
		}
	}
}

// TestActionSetChatEncryptionForcesOmemoV1 verifies picking "omemo-v1" from
// the menu persists that exact mode string via ChatEncryptionSetter and
// updates the chat's displayed EncryptionMode - the mechanism
// resolveOmemoManagerForMode (crypto_helpers.go) later reads to bypass
// auto-detection for this chat.
func TestActionSetChatEncryptionForcesOmemoV1(t *testing.T) {
	sender := &fakeChatEncryptionSender{}
	chat := Chat{Address: "bob@localhost"}
	m := newTestModelWithSender(sender, &fakeAccountAdder{})
	m.accounts = []Account{{Name: "me", Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.chats.Select(0)

	m.actionSetChatEncryption("omemo-v1")

	if sender.mode != "omemo-v1" {
		t.Fatalf("SetChatEncryption mode = %q, want %q", sender.mode, "omemo-v1")
	}
	if sender.lastPeerJID != "bob@localhost" {
		t.Fatalf("SetChatEncryption peerJID = %q, want %q", sender.lastPeerJID, "bob@localhost")
	}
	got, ok := m.chats.Items()[0].(Chat)
	if !ok || got.EncryptionMode != "omemo-v1" {
		t.Fatalf("chat.EncryptionMode = %+v, want omemo-v1", got)
	}
}
