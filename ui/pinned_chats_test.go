package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
)

type fakeChatPinner struct {
	MessageSender
	calls []string // "jid:addr:pinned|unpinned"
	err   error
}

func (f *fakeChatPinner) SetChatPinned(accountJID, chatAddress string, pinned bool) error {
	f.calls = append(f.calls, accountJID+":"+chatAddress+":"+map[bool]string{true: "pinned", false: "unpinned"}[pinned])
	return f.err
}

func pinnedChatsModel(t *testing.T, pinner *fakeChatPinner, chats ...Chat) Model {
	t.Helper()
	items := make([]list.Item, 0, len(chats))
	messages := make(map[int][]Message, len(chats))
	for i, c := range chats {
		items = append(items, c)
		messages[i] = []Message{{Content: "from " + c.Address}}
	}
	var sender MessageSender
	if pinner != nil {
		sender = pinner
	}
	m := newTestModelWithSender(sender, nil)
	m.accounts = []Account{{Name: "me@localhost", Chats: items, Messages: messages, HistoryMore: map[int]bool{}}}
	m.currentAccount = 0
	m.chats.SetItems(items)
	return m
}

// A pinned chat outranks a more recently active unpinned one, and its
// messages travel with it — the chat list's indices and Account.Messages'
// keys are one index space.
func TestPinnedChatSortsFirst(t *testing.T) {
	now := time.Now()
	pinner := &fakeChatPinner{}
	m := pinnedChatsModel(t, pinner,
		Chat{Name: "a", Address: "a@x", LastActivity: now},
		Chat{Name: "b", Address: "b@x", LastActivity: now.Add(-time.Hour)},
		Chat{Name: "c", Address: "c@x", LastActivity: now.Add(-2 * time.Hour)},
	)

	m.togglePinChat(0, 2) // pin the least recently active one

	if got, want := strings.Join(chatAddresses(m), ","), "c@x,a@x,b@x"; got != want {
		t.Fatalf("chats = %q, want %q", got, want)
	}
	for i, wantAddr := range []string{"c@x", "a@x", "b@x"} {
		msgs := m.accounts[0].Messages[i]
		if len(msgs) != 1 || msgs[0].Content != "from "+wantAddr {
			t.Errorf("Messages[%d] = %+v, want the messages of %s", i, msgs, wantAddr)
		}
	}
	if want := []string{"me@localhost:c@x:pinned"}; len(pinner.calls) != 1 || pinner.calls[0] != want[0] {
		t.Fatalf("persisted %v, want %v", pinner.calls, want)
	}
}

// Pinned chats stay activity-sorted among themselves rather than stacking in
// the order they were pinned.
func TestPinnedChatsSortedAmongThemselves(t *testing.T) {
	now := time.Now()
	m := pinnedChatsModel(t, &fakeChatPinner{},
		Chat{Name: "a", Address: "a@x", LastActivity: now},
		Chat{Name: "b", Address: "b@x", LastActivity: now.Add(-time.Hour)},
		Chat{Name: "c", Address: "c@x", LastActivity: now.Add(-2 * time.Hour)},
	)

	m.togglePinChat(0, 2) // c — list becomes c, a, b
	m.togglePinChat(0, 2) // b

	if got, want := strings.Join(chatAddresses(m), ","), "b@x,c@x,a@x"; got != want {
		t.Fatalf("chats = %q, want %q", got, want)
	}
}

func TestUnpinningRestoresActivityOrder(t *testing.T) {
	now := time.Now()
	pinner := &fakeChatPinner{}
	m := pinnedChatsModel(t, pinner,
		Chat{Name: "a", Address: "a@x", LastActivity: now},
		Chat{Name: "b", Address: "b@x", LastActivity: now.Add(-time.Hour)},
	)

	m.togglePinChat(0, 1) // pin b
	if got, want := strings.Join(chatAddresses(m), ","), "b@x,a@x"; got != want {
		t.Fatalf("after pinning chats = %q, want %q", got, want)
	}

	m.togglePinChat(0, 0) // unpin it again
	if got, want := strings.Join(chatAddresses(m), ","), "a@x,b@x"; got != want {
		t.Fatalf("after unpinning chats = %q, want %q", got, want)
	}
	if chat := m.accounts[0].Chats[1].(Chat); chat.Pinned {
		t.Error("chat still flagged pinned after unpinning")
	}
	want := []string{"me@localhost:b@x:pinned", "me@localhost:b@x:unpinned"}
	if strings.Join(pinner.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("persisted %v, want %v", pinner.calls, want)
	}
}

// A failing persist still pins for this session — the row has already moved
// under the user — and says so.
func TestPinPersistFailureNotifies(t *testing.T) {
	pinner := &fakeChatPinner{err: errors.New("nope")}
	m := pinnedChatsModel(t, pinner, Chat{Name: "a", Address: "a@x"})

	m.togglePinChat(0, 0)

	if chat := m.accounts[0].Chats[0].(Chat); !chat.Pinned {
		t.Error("chat not pinned after a failed persist")
	}
	if !strings.Contains(m.noticeText, "pinning a@x") {
		t.Errorf("notification = %q, want it to mention the failure", m.noticeText)
	}
}

// The pin marker has to reach the rendered row, not just the model.
func TestPinnedChatRowShowsGlyph(t *testing.T) {
	m := pinnedChatsModel(t, &fakeChatPinner{}, Chat{Name: "a", Address: "a@x"})
	m.togglePinChat(0, 0)
	if !strings.Contains(m.accounts[0].Chats[0].(Chat).Title(), pinnedChatGlyph) {
		t.Error("pinned chat's title is missing the pin glyph")
	}
}
