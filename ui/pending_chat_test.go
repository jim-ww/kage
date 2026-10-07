package ui

import (
	"testing"

	"charm.land/bubbles/v2/list"
)

// newSnapshotTestModel builds a model the way runTUI does: no accounts yet
// (the snapshot is a round trip to the daemon, so the first frame can't wait
// on it) and, optionally, a last-opened chat address to restore.
func newSnapshotTestModel(sender MessageSender, openLast string) Model {
	m := New(nil, 0, DefaultKeyMap, DefaultTheme(), sender, nil, true, 0, false, openLast, 0, nil, DisplayOptions{}, nil)
	m.width, m.height = 80, 24
	m.updateSizes()
	return m
}

func snapshotWithChat(chat Chat) AccountsSnapshotMsg {
	return AccountsSnapshotMsg{
		Accounts: []Account{{
			Name:     "me@example.test",
			Chats:    []list.Item{chat},
			Messages: map[int][]Message{},
		}},
	}
}

// TestPendingChatSurvivesTheAccountlessFirstPass is the regression for the
// last-opened chat being dropped on startup: Init fires openPendingChatMsg
// before the snapshot has brought any account in, so the chat list is still
// empty on that pass. Consuming the address there left nothing for the
// snapshot to open - and with no openCurrentChat ever running, the chat the
// TUI came up in kept its stored unread count (chat-list badge and tray dot
// lit on a chat being read).
func TestPendingChatSurvivesTheAccountlessFirstPass(t *testing.T) {
	sender := &fakeReadTrackerSender{}
	m := newSnapshotTestModel(sender, "bob@example.test")

	// The Init-time attempt, with no accounts loaded yet.
	next, cmd := m.Update(openPendingChatMsg{})
	m = next.(Model)
	runCmd(cmd)
	if m.pendingOpenChatAddress != "bob@example.test" {
		t.Fatalf("pending chat address after the accountless pass = %q, want it kept", m.pendingOpenChatAddress)
	}

	next, cmd = m.Update(snapshotWithChat(Chat{Name: "bob", Address: "bob@example.test", Unread: 2}))
	m = next.(Model)
	runCmd(cmd)

	if m.pendingOpenChatAddress != "" {
		t.Fatalf("pending chat address after the snapshot = %q, want it consumed", m.pendingOpenChatAddress)
	}
	if m.selectedView != viewChat {
		t.Fatalf("selectedView after the snapshot = %v, want viewChat", m.selectedView)
	}
	if got := m.chats.Items()[0].(Chat).Unread; got != 0 {
		t.Fatalf("Unread on the restored chat = %d, want 0", got)
	}
	if sender.resets != 1 {
		t.Fatalf("ResetChatUnread calls = %d, want 1", sender.resets)
	}
}

// TestSnapshotClearsUnreadOnTheChatItLandsIn covers the same startup state
// without a chat to restore: the cursor defaults to the first row and the
// model starts in viewChat, so the user is reading that chat even though
// nothing opened it.
func TestSnapshotClearsUnreadOnTheChatItLandsIn(t *testing.T) {
	sender := &fakeReadTrackerSender{}
	m := newSnapshotTestModel(sender, "")

	next, cmd := m.Update(snapshotWithChat(Chat{Name: "bob", Address: "bob@example.test", Unread: 1}))
	m = next.(Model)
	runCmd(cmd)

	if got := m.chats.Items()[0].(Chat).Unread; got != 0 {
		t.Fatalf("Unread on the chat the TUI landed in = %d, want 0", got)
	}
	if sender.resets != 1 {
		t.Fatalf("ResetChatUnread calls = %d, want 1", sender.resets)
	}
}

// TestInitSkipsEmptyFocusReportWhileAChatIsPending guards the startup
// notification window: runTUI reports the chat it is coming up in (from
// config) before the model exists, so Init must not overwrite that with the
// empty key activeChatKey necessarily returns while no account is loaded -
// the daemon would then believe nobody is watching that chat and notify for
// messages the user is looking at.
func TestInitSkipsEmptyFocusReportWhileAChatIsPending(t *testing.T) {
	m := newSnapshotTestModel(&fakeSuccessSender{}, "bob@example.test")
	reporter := &recordingFocusReporter{}
	m.focusReporter = reporter

	drainCmds(m.Init())
	if len(reporter.calls) != 0 {
		t.Fatalf("focus reports from Init = %v, want none while the chat is pending", reporter.calls)
	}

	// Once the accounts land and the pending chat opens, the real state goes
	// out as usual.
	next, cmd := m.Update(snapshotWithChat(Chat{Name: "bob", Address: "bob@example.test"}))
	m = next.(Model)
	drainCmds(cmd)
	if len(reporter.calls) == 0 {
		t.Fatal("no focus report after the snapshot opened the chat")
	}
	last := reporter.calls[len(reporter.calls)-1]
	if last.chatAddress != "bob@example.test" || !last.focused {
		t.Fatalf("last focus report = %+v, want bob@example.test focused", last)
	}
}
