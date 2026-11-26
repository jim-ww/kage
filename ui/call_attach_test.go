package ui

import (
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	"github.com/charmbracelet/x/ansi"
)

// TestCallBarShownAttachingMidCall: a TUI attaching while a call is up
// shows its call bar from the snapshot, before any live call event.
func TestCallBarShownAttachingMidCall(t *testing.T) {
	m := newTestModelWithSender(nil, nil)
	m.width, m.termHeight = 80, 20
	m.updateSizes()
	acc := Account{Chats: []list.Item{Chat{Address: "bob@example.test"}}, Messages: map[int][]Message{}}
	m = updated(t, m, AccountsSnapshotMsg{
		Accounts:  []Account{acc},
		CallState: &CallStateMsg{Peer: "bob@example.test", SID: "s1", State: "connected", StartedAt: time.Now().Add(-time.Minute)},
	})
	if !m.callBarActive() {
		t.Fatal("no call bar after a snapshot with a call up")
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "📞 bob@example.test · 01:00") {
		t.Errorf("call bar with the call's duration not drawn:\n%s", view)
	}
}
