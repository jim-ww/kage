package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

type fakeBlocker struct {
	fakeSuccessSender
	calls []string
}

func (f *fakeBlocker) SetBlocked(accountIdx int, address string, blocked bool) tea.Msg {
	f.calls = append(f.calls, address+" "+map[bool]string{true: "block", false: "unblock"}[blocked])
	return BlockedSetMsg{AccountIdx: accountIdx, Address: address, Blocked: blocked}
}

// TestBlockChatFromMenu: the chat menu blocks a contact once confirmed,
// and the daemon's broadcast marks the chat blocked.
func TestBlockChatFromMenu(t *testing.T) {
	blocker := &fakeBlocker{}
	m := newTestModelWithSender(blocker, nil)
	chat := Chat{Name: "Bob", Address: "bob@example.test"}
	m.accounts = []Account{{Name: "me@example.test", Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	m.chats.SetItems(m.accounts[0].Chats)
	m.chats.Select(0)
	m.selectedView = viewChats

	m = press(t, m, letter('m'))
	m = press(t, m, letter('b'))
	if len(blocker.calls) != 0 {
		t.Fatal("blocked without confirming")
	}
	m = press(t, m, letter('y'))
	if strings.Join(blocker.calls, "|") != "bob@example.test block" {
		t.Fatalf("daemon calls: %v", blocker.calls)
	}

	m = updated(t, m, ChatSettingMsg{AccountJID: "me@example.test", ChatAddress: "bob@example.test", Setting: ChatBlocked, On: true})
	got := m.accounts[0].Chats[0].(Chat)
	if !got.Blocked || !strings.Contains(got.Title(), blockedChatGlyph) {
		t.Fatalf("after the broadcast: blocked=%v title=%q", got.Blocked, got.Title())
	}

	// Unblocking needs no confirmation.
	m = press(t, m, letter('m'))
	if labels := callMenuLabels(m); !strings.Contains(strings.Join(labels, "|"), "Unblock contact") {
		t.Fatalf("chat menu of a blocked contact: %v", labels)
	}
	m = press(t, m, letter('b'))
	if strings.Join(blocker.calls, "|") != "bob@example.test block|bob@example.test unblock" {
		t.Fatalf("daemon calls: %v", blocker.calls)
	}
}
