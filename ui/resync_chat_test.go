package ui

import (
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

type fakeResyncer struct {
	stubDaemon
	calls []string
}

func (f *fakeResyncer) ResyncChat(accountIdx int, address string) tea.Msg {
	f.calls = append(f.calls, address)
	return ChatResyncedMsg{AccountIdx: accountIdx, Address: address, Recovered: 4}
}

func TestResyncChatMenuItem(t *testing.T) {
	d := &fakeResyncer{}
	m := newTestModelWithSender(d, nil)
	chat := Chat{Name: "Bob", Address: "bob@example.test"}
	m.accounts = []Account{{Name: "me@example.test", Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	m.chats.SetItems(m.accounts[0].Chats)
	m.chats.Select(0)

	var run func(*Model) tea.Cmd
	for _, it := range m.chatItemContextMenuItems(0) {
		if it.label == "Re-sync history" {
			run = it.run
		}
	}
	if run == nil {
		t.Fatal("chat menu has no Re-sync history item")
	}
	batch, ok := run(&m)().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("action returned %T, want the notice and the daemon call", batch)
	}
	result := batch[1]()
	if len(d.calls) != 1 || d.calls[0] != "bob@example.test" {
		t.Fatalf("ResyncChat calls: %v", d.calls)
	}

	next, _ := m.Update(result)
	if got := next.(Model).noticeText; got != "re-sync: 4 messages recovered" {
		t.Errorf("notice = %q", got)
	}
}
