package ui

import (
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

// recordingSettingsSender fails the test if a setting arriving from another
// TUI gets persisted again by this one.
type recordingSettingsSender struct {
	fakeSuccessSender
	persisted []string
}

func (r *recordingSettingsSender) SetChatPinned(_, addr string, _ bool) error {
	r.persisted = append(r.persisted, "pinned "+addr)
	return nil
}
func (r *recordingSettingsSender) SetChatMuted(_, addr string, _ bool) error {
	r.persisted = append(r.persisted, "muted "+addr)
	return nil
}
func (r *recordingSettingsSender) SetChatHidden(_, addr string, _ bool) error {
	r.persisted = append(r.persisted, "hidden "+addr)
	return nil
}

func syncTestModel(sender Daemon) Model {
	m := newTestModelWithSender(sender, nil)
	chats := []list.Item{Chat{Name: "Bob", Address: "bob@x"}, Chat{Name: "Carol", Address: "carol@x"}}
	m.accounts = []Account{{Name: "me@x", Chats: chats, Messages: map[int][]Message{}}}
	m.chats.SetItems(chats)
	return m
}

func chatNamed(m Model, addr string) (Chat, bool) {
	idx := m.chatIndexByAddress(0, addr)
	if idx < 0 {
		return Chat{}, false
	}
	return m.accounts[0].Chats[idx].(Chat), true
}

func TestChatSettingFromAnotherTUI(t *testing.T) {
	sender := &recordingSettingsSender{}
	m := syncTestModel(sender)
	apply := func(msg ChatSettingMsg) {
		next, cmd := m.Update(msg)
		nonIdleCmd(cmd)
		m = next.(Model)
	}

	apply(ChatSettingMsg{AccountJID: "me@x", ChatAddress: "carol@x", Setting: "pinned", On: true})
	apply(ChatSettingMsg{AccountJID: "me@x", ChatAddress: "carol@x", Setting: "muted", On: true})
	apply(ChatSettingMsg{AccountJID: "me@x", ChatAddress: "carol@x", Setting: "encryption", Mode: "none"})
	carol, _ := chatNamed(m, "carol@x")
	if !carol.Pinned || !carol.Muted || carol.EncryptionMode != "none" {
		t.Fatalf("carol after settings: %+v", carol)
	}
	if first := m.accounts[0].Chats[0].(Chat); first.Address != "carol@x" {
		t.Fatalf("pinning didn't re-sort: first is %s", first.Address)
	}

	apply(ChatSettingMsg{AccountJID: "me@x", ChatAddress: "bob@x", Setting: "hidden", On: true})
	if _, ok := chatNamed(m, "bob@x"); ok || !m.isChatHidden(0, "bob@x") {
		t.Fatal("hiding from another TUI didn't hide bob")
	}
	// A setting changed while hidden shows once it's unhidden.
	apply(ChatSettingMsg{AccountJID: "me@x", ChatAddress: "bob@x", Setting: "muted", On: true})
	apply(ChatSettingMsg{AccountJID: "me@x", ChatAddress: "bob@x", Setting: "hidden", On: false})
	if bob, ok := chatNamed(m, "bob@x"); !ok || !bob.Muted {
		t.Fatalf("bob after unhiding: %+v (found %v)", bob, ok)
	}

	if len(sender.persisted) != 0 {
		t.Fatalf("settings from another TUI were persisted again: %v", sender.persisted)
	}
	// Another account's setting leaves this one alone.
	apply(ChatSettingMsg{AccountJID: "other@x", ChatAddress: "bob@x", Setting: "pinned", On: true})
	if bob, _ := chatNamed(m, "bob@x"); bob.Pinned {
		t.Fatal("another account's pin applied here")
	}
}

// TestAccountStatusAppliedOnce checks chats loaded by coming back online
// arrive once: through the broadcast, not again through the requesting
// TUI's own RPC result.
func TestAccountStatusAppliedOnce(t *testing.T) {
	m := syncTestModel(&fakeSuccessSender{})
	newChats := []list.Item{Chat{Name: "Dave", Address: "dave@x"}}
	for _, msg := range []tea.Msg{
		AccountStatusChangedMsg{Index: 0, Status: PresenceAway, NewChats: newChats},
		AccountStatusSetMsg{Index: 0, Status: PresenceAway},
	} {
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	if m.accounts[0].Status != PresenceAway {
		t.Fatalf("status %v", m.accounts[0].Status)
	}
	if n := len(m.accounts[0].Chats); n != 3 {
		t.Fatalf("%d chats, want 3 (dave added exactly once)", n)
	}
}
