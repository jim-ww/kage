package ui

import (
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

// runCmd executes cmd and, if it produced a tea.BatchMsg, recursively
// executes every sub-command too — mirroring what the real Program loop
// does, since Update() itself only returns commands without running them.
// Needed here so ChatReadTracker side effects (which ride inline func()
// tea.Msg closures batched alongside the list-item refresh) actually fire.
func runCmd(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			runCmd(c)
		}
	}
}

// fakeReadTrackerSender combines fakeSuccessSender with ChatReadTracker so
// New's sender.(ChatReadTracker) type assertion picks it up, and records
// calls so persistence can be asserted alongside in-memory state.
type fakeReadTrackerSender struct {
	fakeSuccessSender
	incremented []int // deltas passed to IncrementChatUnread, in order
	resets      int
}

func (f *fakeReadTrackerSender) IncrementChatUnread(accountJID, chatAddress string, delta int) error {
	f.incremented = append(f.incremented, delta)
	return nil
}

func (f *fakeReadTrackerSender) ResetChatUnread(accountJID, chatAddress string) error {
	f.resets++
	return nil
}

func (f *fakeReadTrackerSender) ChatUnreadCounts(accountJID string) (map[string]int, error) {
	return nil, nil
}

// TestUnreadIncrementsOnUnfocusedIncoming guards that a message arriving for
// a chat that isn't the actively-focused one bumps Chat.Unread, both in
// memory and via ChatReadTracker, while an own ("IsMe") message never does.
func TestUnreadIncrementsOnUnfocusedIncoming(t *testing.T) {
	sender := &fakeReadTrackerSender{}
	m := newTestModelWithSender(sender, nil)
	chat := Chat{Address: "bob@example.test"}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	m.currentAccount = 0
	m.chats.SetItems(m.accounts[0].Chats)
	m.selectedView = viewChats // chat list focused, not the chat itself

	next, cmd := m.Update(IncomingMessageMsg{AccountIdx: 0, From: "bob@example.test", Message: Message{ID: "m1", Content: "hi"}})
	m = next.(Model)
	runCmd(cmd)
	if got := m.chats.Items()[0].(Chat).Unread; got != 1 {
		t.Fatalf("Unread after first incoming = %d, want 1", got)
	}
	if len(sender.incremented) != 1 || sender.incremented[0] != 1 {
		t.Fatalf("IncrementChatUnread calls = %v, want [1]", sender.incremented)
	}

	next, cmd = m.Update(IncomingMessageMsg{AccountIdx: 0, From: "bob@example.test", Message: Message{ID: "m2", Content: "again"}})
	m = next.(Model)
	runCmd(cmd)
	if got := m.chats.Items()[0].(Chat).Unread; got != 2 {
		t.Fatalf("Unread after second incoming = %d, want 2", got)
	}

	// An own/outgoing message must never count as unread.
	next, cmd = m.Update(IncomingMessageMsg{AccountIdx: 0, From: "bob@example.test", Message: Message{ID: "m3", Content: "me", IsMe: true}})
	m = next.(Model)
	runCmd(cmd)
	if got := m.chats.Items()[0].(Chat).Unread; got != 2 {
		t.Fatalf("Unread after own message = %d, want unchanged 2", got)
	}
}

// TestUnreadSkipsFocusedChatAndDecryptFailures guards two exclusions: a
// message for the chat currently being viewed shouldn't count (the user is
// already looking at it), and an undecryptable-placeholder message never
// counts since opening the chat won't reveal any more content.
func TestUnreadSkipsFocusedChatAndDecryptFailures(t *testing.T) {
	sender := &fakeReadTrackerSender{}
	m := newTestModelWithSender(sender, nil)
	chat := Chat{Address: "bob@example.test"}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	m.currentAccount = 0
	m.chats.SetItems(m.accounts[0].Chats)
	m.chats.Select(0)
	m.selectedView = viewChat // actively viewing this chat

	next, _ := m.Update(IncomingMessageMsg{AccountIdx: 0, From: "bob@example.test", Message: Message{ID: "m1", Content: "hi"}})
	m = next.(Model)
	if got := m.chats.Items()[0].(Chat).Unread; got != 0 {
		t.Fatalf("Unread while chat focused = %d, want 0", got)
	}

	m.selectedView = viewChats // unfocus, then a decrypt failure arrives
	next, _ = m.Update(IncomingMessageMsg{AccountIdx: 0, From: "bob@example.test", Message: Message{ID: "m2", Content: "[message could not be decrypted: x]", DecryptFailed: true}})
	m = next.(Model)
	if got := m.chats.Items()[0].(Chat).Unread; got != 0 {
		t.Fatalf("Unread after decrypt-failed message = %d, want 0", got)
	}
	if len(sender.incremented) != 0 {
		t.Fatalf("IncrementChatUnread calls = %v, want none", sender.incremented)
	}
}

// TestUnreadResetsOnOpenChat guards that opening a chat (openCurrentChat,
// the funnel used by Enter/click/etc.) zeroes its unread count, in memory
// and via ChatReadTracker.
func TestUnreadResetsOnOpenChat(t *testing.T) {
	sender := &fakeReadTrackerSender{}
	m := newTestModelWithSender(sender, nil)
	chat := Chat{Address: "bob@example.test", Unread: 3}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	m.currentAccount = 0
	m.chats.SetItems(m.accounts[0].Chats)
	m.chats.Select(0)
	m.selectedView = viewChats

	next, cmd := m.openCurrentChat()
	m = next.(Model)
	runCmd(cmd)
	if got := m.chats.Items()[0].(Chat).Unread; got != 0 {
		t.Fatalf("Unread after opening chat = %d, want 0", got)
	}
	if sender.resets != 1 {
		t.Fatalf("ResetChatUnread calls = %d, want 1", sender.resets)
	}
}

// TestUnreadCountsHistorySyncBatch guards the MAM catch-up path
// (HistorySyncedMsg): messages delivered this way while the chat isn't
// focused count toward unread too (they can be genuinely new messages that
// arrived while offline, not just historical replay), except own messages
// and decrypt failures within the batch.
func TestUnreadCountsHistorySyncBatch(t *testing.T) {
	sender := &fakeReadTrackerSender{}
	m := newTestModelWithSender(sender, nil)
	chat := Chat{Address: "bob@example.test"}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	m.currentAccount = 0
	m.chats.SetItems(m.accounts[0].Chats)
	m.selectedView = viewChats

	next, cmd := m.Update(HistorySyncedMsg{
		AccountIdx: 0,
		From:       "bob@example.test",
		Messages: []Message{
			{ID: "h1", Content: "one"},
			{ID: "h2", Content: "me", IsMe: true},
			{ID: "h3", Content: "[message could not be decrypted: x]", DecryptFailed: true},
			{ID: "h4", Content: "two"},
		},
	})
	m = next.(Model)
	runCmd(cmd)
	if got := m.chats.Items()[0].(Chat).Unread; got != 2 {
		t.Fatalf("Unread after history sync batch = %d, want 2", got)
	}
	if len(sender.incremented) != 1 || sender.incremented[0] != 2 {
		t.Fatalf("IncrementChatUnread calls = %v, want [2]", sender.incremented)
	}
}

// TestChatDescriptionShowsUnreadCount guards the chat-list description
// prefix used to surface the unread badge.
func TestChatDescriptionShowsUnreadCount(t *testing.T) {
	c := Chat{Name: "bob", Address: "bob@example.test", LastMessage: "hi", Unread: 3}
	if got, want := c.Description(), "(3) hi"; got != want {
		t.Fatalf("Description() = %q, want %q", got, want)
	}
	c2 := Chat{Name: "bob", Unread: 2}
	if got, want := c2.Description(), "(2)"; got != want {
		t.Fatalf("Description() with no LastMessage/Address = %q, want %q", got, want)
	}
	c3 := Chat{Name: "bob", Address: "bob@example.test", LastMessage: "hi"}
	if got, want := c3.Description(), "hi"; got != want {
		t.Fatalf("Description() with zero Unread = %q, want %q", got, want)
	}
}
