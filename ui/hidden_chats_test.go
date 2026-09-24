package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

type fakeChatHider struct {
	MessageSender
	calls []string // "jid:addr:hidden"
	err   error
}

func (f *fakeChatHider) SetChatHidden(accountJID, chatAddress string, hidden bool) error {
	f.calls = append(f.calls, accountJID+":"+chatAddress+":"+map[bool]string{true: "hidden", false: "shown"}[hidden])
	return f.err
}

func hiddenChatsModel(t *testing.T, hider *fakeChatHider, chats ...Chat) Model {
	t.Helper()
	items := make([]list.Item, 0, len(chats))
	for _, c := range chats {
		items = append(items, c)
	}
	messages := make(map[int][]Message, len(chats))
	for i, c := range chats {
		messages[i] = []Message{{Content: "from " + c.Address}}
	}
	var sender MessageSender
	if hider != nil {
		sender = hider
	}
	m := newTestModelWithSender(sender, nil)
	m.accounts = []Account{{Name: "me@localhost", Chats: items, Messages: messages, HistoryMore: map[int]bool{}}}
	m.currentAccount = 0
	m.chats.SetItems(items)
	m.stripHiddenChats(0)
	if len(m.hiddenChats[0]) > 0 {
		m.chats.SetItems(m.accounts[0].Chats)
	}
	return m
}

func chatAddresses(m Model) []string {
	out := make([]string, 0, len(m.accounts[0].Chats))
	for _, item := range m.accounts[0].Chats {
		out = append(out, item.(Chat).Address)
	}
	return out
}

// The chat list's indices and Account.Messages' keys are the same index
// space, so removing a chat from the middle has to re-key everything after
// it or every message lookup lands on the wrong conversation.
func TestHidingReindexesMessages(t *testing.T) {
	hider := &fakeChatHider{}
	m := hiddenChatsModel(t, hider,
		Chat{Name: "a", Address: "a@x"},
		Chat{Name: "b", Address: "b@x"},
		Chat{Name: "c", Address: "c@x"},
	)

	m.hideChat(0, 1) // hide the middle one

	if got, want := strings.Join(chatAddresses(m), ","), "a@x,c@x"; got != want {
		t.Fatalf("chats = %q, want %q", got, want)
	}
	for i, wantAddr := range []string{"a@x", "c@x"} {
		msgs := m.accounts[0].Messages[i]
		if len(msgs) != 1 || msgs[0].Content != "from "+wantAddr {
			t.Errorf("Messages[%d] = %+v, want the messages of %s", i, msgs, wantAddr)
		}
	}
	if _, stale := m.accounts[0].Messages[2]; stale {
		t.Error("Messages still has an entry past the end of the chat list")
	}
}

func TestHidingPersistsAndUnhideRestores(t *testing.T) {
	hider := &fakeChatHider{}
	m := hiddenChatsModel(t, hider,
		Chat{Name: "a", Address: "a@x"},
		Chat{Name: "b", Address: "b@x"},
	)

	m.hideChat(0, 0)
	if !m.isChatHidden(0, "a@x") {
		t.Fatal("a@x is not marked hidden")
	}
	if len(hider.calls) != 1 || hider.calls[0] != "me@localhost:a@x:hidden" {
		t.Fatalf("persisted %v, want one hidden call for a@x", hider.calls)
	}

	m.unhideChat(0, "a@x")
	if m.isChatHidden(0, "a@x") {
		t.Error("a@x is still marked hidden after unhiding")
	}
	if got, want := strings.Join(chatAddresses(m), ","), "b@x,a@x"; got != want {
		t.Errorf("chats = %q, want %q — the unhidden chat goes back on the end", got, want)
	}
	// Its loaded history came back with it, at its new index.
	if msgs := m.accounts[0].Messages[1]; len(msgs) != 1 || msgs[0].Content != "from a@x" {
		t.Errorf("Messages[1] = %+v, want a@x's history", msgs)
	}
	if len(hider.calls) != 2 || hider.calls[1] != "me@localhost:a@x:shown" {
		t.Errorf("persisted %v, want an unhide call for a@x", hider.calls)
	}
}

// Nothing about the chat, its history, or the roster is destroyed — only
// the row goes away.
func TestHidingDestroysNothing(t *testing.T) {
	m := hiddenChatsModel(t, &fakeChatHider{}, Chat{Name: "a", Address: "a@x", Draft: "unsent", EncryptionMode: "gpg"})

	m.hideChat(0, 0)
	stashed, ok := m.hiddenChats[0]["a@x"]
	if !ok {
		t.Fatal("the hidden chat was not kept")
	}
	if stashed.chat.Draft != "unsent" || stashed.chat.EncryptionMode != "gpg" {
		t.Errorf("stashed chat = %+v, want the draft and encryption mode preserved", stashed.chat)
	}
	if len(stashed.messages) != 1 {
		t.Errorf("stashed messages = %v, want the loaded history kept", stashed.messages)
	}
}

// A chat the daemon reports as hidden must be out of the list before
// anything indexes into it.
func TestHiddenChatsFromTheDaemonAreStripped(t *testing.T) {
	m := hiddenChatsModel(t, nil,
		Chat{Name: "a", Address: "a@x"},
		Chat{Name: "b", Address: "b@x", Hidden: true},
		Chat{Name: "c", Address: "c@x"},
	)

	if got, want := strings.Join(chatAddresses(m), ","), "a@x,c@x"; got != want {
		t.Fatalf("chats = %q, want %q", got, want)
	}
	if !m.isChatHidden(0, "b@x") {
		t.Error("b@x was dropped rather than stashed")
	}
	if msgs := m.accounts[0].Messages[1]; len(msgs) != 1 || msgs[0].Content != "from c@x" {
		t.Errorf("Messages[1] = %+v, want c@x's — the keys must follow the list", msgs)
	}
}

// Someone writing to you is the one thing that reliably means you want to
// see them again.
func TestIncomingMessageUnhides(t *testing.T) {
	hider := &fakeChatHider{}
	m := hiddenChatsModel(t, hider,
		Chat{Name: "a", Address: "a@x"},
		Chat{Name: "b", Address: "b@x", Hidden: true},
	)

	next := updated(t, m, IncomingMessageMsg{
		AccountIdx: 0,
		From:       "b@x",
		Message:    Message{ID: "m1", Author: "b", Content: "still here?"},
	})

	if next.isChatHidden(0, "b@x") {
		t.Fatal("b@x is still hidden after they wrote")
	}
	idx := next.chatIndexByAddress(0, "b@x")
	if idx < 0 {
		t.Fatal("b@x has no row after unhiding")
	}
	msgs := next.accounts[0].Messages[idx]
	if len(msgs) == 0 || msgs[len(msgs)-1].Content != "still here?" {
		t.Errorf("Messages[%d] = %+v, want the message that unhid the chat appended", idx, msgs)
	}
	if len(hider.calls) != 1 || !strings.HasSuffix(hider.calls[0], ":shown") {
		t.Errorf("persisted %v, want the unhide recorded", hider.calls)
	}
}

func TestIncomingMessageKeepsChatHiddenWhenAutoUnhideDisabled(t *testing.T) {
	hider := &fakeChatHider{}
	m := hiddenChatsModel(t, hider, Chat{Name: "b", Address: "b@x", Hidden: true})
	m.autoUnhideDisabled = true

	next := updated(t, m, IncomingMessageMsg{
		AccountIdx: 0,
		From:       "b@x",
		Message:    Message{ID: "m1", Content: "still here?"},
	})

	if !next.isChatHidden(0, "b@x") {
		t.Error("b@x was unhidden despite auto_unhide_disabled")
	}
	if len(next.accounts[0].Chats) != 0 {
		t.Errorf("chats = %v, want the hidden chat to stay out of the list", chatAddresses(next))
	}
	if len(hider.calls) != 0 {
		t.Errorf("persisted %v, want nothing written", hider.calls)
	}
}

// A message from a contact who was never hidden is still ignored — that
// path predates hiding and is not what this changed.
func TestIncomingMessageForUnknownChatStillIgnored(t *testing.T) {
	m := hiddenChatsModel(t, &fakeChatHider{}, Chat{Name: "a", Address: "a@x"})

	next := updated(t, m, IncomingMessageMsg{AccountIdx: 0, From: "stranger@x", Message: Message{ID: "m1"}})
	if len(next.accounts[0].Chats) != 1 {
		t.Errorf("chats = %v, want the stranger not to create a row", chatAddresses(next))
	}
}

// The contact manager is the way back to a chat you hid and then never
// heard from again.
func TestContactManagerListsAndUnhides(t *testing.T) {
	m := hiddenChatsModel(t, &fakeChatHider{},
		Chat{Name: "a", Address: "a@x"},
		Chat{Name: "b", Address: "b@x", Hidden: true},
	)
	m.contactManagerState = &contactManagerState{accountIdx: 0}

	contacts := m.contactManagerState.contacts(m)
	var found bool
	for _, c := range contacts {
		if c.Address == "b@x" {
			found = true
			if !c.Hidden {
				t.Error("the hidden contact is listed without being marked hidden")
			}
		}
	}
	if !found {
		t.Fatalf("contact manager lists %d contacts, none of them the hidden one", len(contacts))
	}

	items := m.contactRowContextMenuItems("b@x")
	if len(items) == 0 || items[0].label != "Unhide chat" {
		labels := make([]string, len(items))
		for i, it := range items {
			labels[i] = it.label
		}
		t.Fatalf("menu for a hidden contact = %v, want Unhide chat first", labels)
	}
	// And a visible contact isn't offered it.
	for _, it := range m.contactRowContextMenuItems("a@x") {
		if it.label == "Unhide chat" {
			t.Error("a visible contact is offered Unhide")
		}
	}

	items[0].run(&m)
	if m.isChatHidden(0, "b@x") {
		t.Error("b@x is still hidden after the menu action")
	}
}

// Hiding leaves the cursor somewhere sane rather than past the end.
func TestHidingKeepsSelectionInRange(t *testing.T) {
	m := hiddenChatsModel(t, &fakeChatHider{},
		Chat{Name: "a", Address: "a@x"},
		Chat{Name: "b", Address: "b@x"},
	)
	m.chats.Select(1)

	m.hideChat(0, 1)
	if got := m.chats.Index(); got >= len(m.accounts[0].Chats) {
		t.Errorf("selection is %d with %d chats left", got, len(m.accounts[0].Chats))
	}
}

func TestHideConfirmationMentionsNothingIsDeleted(t *testing.T) {
	m := hiddenChatsModel(t, &fakeChatHider{}, Chat{Name: "a", Address: "a@x"})
	m.width, m.height, m.termHeight = 120, 40, 40
	m.updateSizes()
	m.confirmTarget = confirmHideChat

	prompt := m.deletePrompt(m.deletePromptWidth())
	for _, want := range []string{"Hide chat?", "Nothing is deleted"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt %q is missing %q", prompt, want)
		}
	}
}

var _ tea.Msg = IncomingMessageMsg{}
