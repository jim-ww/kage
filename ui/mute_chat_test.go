package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
)

type fakeMuteSetter struct {
	fakeSuccessSender
	calls []string
}

func (f *fakeMuteSetter) SetChatMuted(accountJID, chatAddress string, muted bool) error {
	f.calls = append(f.calls, accountJID+" "+chatAddress+" "+map[bool]string{true: "on", false: "off"}[muted])
	return nil
}

func TestMuteChatKey(t *testing.T) {
	setter := &fakeMuteSetter{}
	m := newTestModelWithSender(setter, nil)
	chat := Chat{Name: "Bob", Address: "bob@example.test"}
	m.accounts = []Account{{Name: "me@example.test", Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	m.chats.SetItems(m.accounts[0].Chats)
	m.chats.Select(0)
	m.selectedView = viewChats

	m = press(t, m, letter('m'))
	got := m.accounts[0].Chats[0].(Chat)
	if !got.Muted || !strings.Contains(got.Title(), mutedChatGlyph) {
		t.Fatalf("after m: muted=%v title=%q", got.Muted, got.Title())
	}
	if items := m.chatItemContextMenuItems(0); items[2].label != "Unmute notifications" {
		t.Fatalf("chat menu: %v", items[2].label)
	}
	m = press(t, m, letter('m'))
	if m.accounts[0].Chats[0].(Chat).Muted {
		t.Fatal("second m didn't unmute")
	}
	if strings.Join(setter.calls, "|") != "me@example.test bob@example.test on|me@example.test bob@example.test off" {
		t.Fatalf("persisted: %v", setter.calls)
	}
}
