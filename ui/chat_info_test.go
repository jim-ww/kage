package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	"github.com/charmbracelet/x/ansi"
)

// TestChatInfoFromStatusBarAndMenu: clicking the chat's name in the status
// bar opens its contact info, as does the chat menu's Info item.
func TestChatInfoFromStatusBarAndMenu(t *testing.T) {
	m := newTestModelWithManyMessages(3)
	chat := Chat{
		Name: "Bob", Address: "bob@example.test", Presence: PresenceAway, Subscription: "to",
		Resources: []ResourcePresence{{Resource: "phone1", Presence: PresenceAway, Name: "Conversations"}},
	}
	m.accounts[0].Chats = []list.Item{chat}
	m.chats.SetItems(m.accounts[0].Chats)
	m.chats.Select(0)
	m.width, m.termHeight = 100, 30
	m.updateSizes()

	m, _ = clickZone(t, m, zoneChatStatusBar)
	if !m.modals.Open() {
		t.Fatal("clicking the status bar opened nothing")
	}
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"Address: bob@example.test", "Conversations (phone1)", "you see them, they don't see you"} {
		if !strings.Contains(view, want) {
			t.Errorf("contact info lacks %q:\n%s", want, view)
		}
	}

	m.modals.Close()
	items := m.chatItemContextMenuItems(0)
	last := items[len(items)-1]
	if last.label != "Info" || last.key != 'i' {
		t.Fatalf("chat menu's last item = %q %q, want Info on i", last.label, last.key)
	}
	last.run(&m)
	if modalTitle(m) != "Bob" {
		t.Fatalf("chat menu Info opened %q, want Bob's info", modalTitle(m))
	}
}
