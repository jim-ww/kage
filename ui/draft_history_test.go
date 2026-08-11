package ui

import (
	"testing"
	"testing/synctest"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

// fakeDraftSaver is a stub MessageSender+DraftSaver: Send always succeeds,
// and SaveDraft records every call (keyed by chat address) so tests can
// assert what got persisted and when.
type fakeDraftSaver struct {
	saved map[string]string
	calls int
}

func newFakeDraftSaver() *fakeDraftSaver { return &fakeDraftSaver{saved: map[string]string{}} }

func (f *fakeDraftSaver) Send(int, string, string, SendOptions) (string, error) {
	return "msg-id", nil
}
func (f *fakeDraftSaver) SetTyping(int, string, bool) error       { return nil }
func (f *fakeDraftSaver) MarkRetracted(int, string, string) error { return nil }

func (f *fakeDraftSaver) SaveDraft(_, chatAddress, text string) error {
	f.calls++
	f.saved[chatAddress] = text
	return nil
}

func ctrlZKey() tea.KeyMsg { return tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl} }
func ctrlShiftZKey() tea.KeyMsg {
	return tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl | tea.ModShift}
}

// TestComposeUndoRedo checks ctrl+z/ctrl+shift+z step the compose box back
// and forth through the draft's edit history for the current session.
func TestComposeUndoRedo(t *testing.T) {
	m := newTestModel(&fakeAccountAdder{})
	m.selectedView = viewChat
	m.accounts = []Account{{Name: "me", Chats: nil, Messages: map[int][]Message{}}}

	for _, s := range []string{"a", "b", "c"} {
		next, _ := m.Update(keyText(s))
		m = next.(Model)
	}
	if got, want := m.input.Value(), "abc"; got != want {
		t.Fatalf("input value after typing = %q, want %q", got, want)
	}

	next, _ := m.Update(ctrlZKey())
	m = next.(Model)
	if got, want := m.input.Value(), "ab"; got != want {
		t.Fatalf("input value after undo = %q, want %q", got, want)
	}

	next, _ = m.Update(ctrlZKey())
	m = next.(Model)
	if got, want := m.input.Value(), "a"; got != want {
		t.Fatalf("input value after second undo = %q, want %q", got, want)
	}

	next, _ = m.Update(ctrlShiftZKey())
	m = next.(Model)
	if got, want := m.input.Value(), "ab"; got != want {
		t.Fatalf("input value after redo = %q, want %q", got, want)
	}

	// Undoing once more, then typing something new, should discard the
	// forward ("abc") history rather than leaving it reachable by redo.
	next, _ = m.Update(ctrlZKey())
	m = next.(Model)
	next, _ = m.Update(keyText("x"))
	m = next.(Model)
	if got, want := m.input.Value(), "ax"; got != want {
		t.Fatalf("input value after undo+type = %q, want %q", got, want)
	}
	next, _ = m.Update(ctrlShiftZKey())
	m = next.(Model)
	if got, want := m.input.Value(), "ax"; got != want {
		t.Fatalf("redo after new edit should be a no-op, got %q, want %q", got, want)
	}
}

// TestComposeUndoHistoryClearedOnSend checks that sending a message resets
// undo history, so ctrl+z after a send can't reach back into the message
// that was just sent.
func TestComposeUndoHistoryClearedOnSend(t *testing.T) {
	chat := Chat{Address: "bob@localhost"}
	m := newTestModel(&fakeAccountAdder{})
	m.selectedView = viewChat
	m.accounts = []Account{{Name: "me", Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.chats.Select(0)

	next, _ := m.Update(keyText("hi"))
	m = next.(Model)
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	if got := m.input.Value(); got != "" {
		t.Fatalf("input should be cleared after send, got %q", got)
	}

	next, _ = m.Update(ctrlZKey())
	m = next.(Model)
	if got := m.input.Value(); got != "" {
		t.Fatalf("undo after send should stay empty, got %q", got)
	}
}

// TestDraftSavedOnChatSwitch checks that switching chats persists the
// outgoing chat's unsent text and loads in the incoming chat's stored draft.
func TestDraftSavedOnChatSwitch(t *testing.T) {
	saver := newFakeDraftSaver()
	m := newTestModelWithSender(saver, &fakeAccountAdder{})
	chatA := Chat{Address: "alice@localhost"}
	chatB := Chat{Address: "bob@localhost", Draft: "already typed for bob"}
	m.accounts = []Account{{Name: "me", Chats: []list.Item{chatA, chatB}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chatA, chatB}); cmd != nil {
		runCmd(cmd)
	}

	m.chats.Select(0)
	model, cmd := m.openCurrentChat()
	m = model.(Model)
	runCmd(cmd)
	if got := m.input.Value(); got != "" {
		t.Fatalf("input after opening alice (no stored draft) = %q, want empty", got)
	}

	next, _ := m.Update(keyText("hi alice"))
	m = next.(Model)

	m.chats.Select(1)
	model, cmd = m.openCurrentChat()
	m = model.(Model)
	runCmd(cmd)

	if got, want := saver.saved["alice@localhost"], "hi alice"; got != want {
		t.Fatalf("saved draft for alice = %q, want %q", got, want)
	}
	if got, want := m.input.Value(), "already typed for bob"; got != want {
		t.Fatalf("input after switching to bob = %q, want %q", got, want)
	}
	if chat, ok := m.accounts[0].Chats[0].(Chat); !ok || chat.Draft != "hi alice" {
		t.Fatalf("in-memory Chat.Draft for alice = %q, want %q", chat.Draft, "hi alice")
	}

	// Switching back to alice restores it.
	m.chats.Select(0)
	model, cmd = m.openCurrentChat()
	m = model.(Model)
	runCmd(cmd)
	if got, want := m.input.Value(), "hi alice"; got != want {
		t.Fatalf("input after switching back to alice = %q, want %q", got, want)
	}
}

// TestDraftClearedOnSend checks that sending a message persists an empty
// draft for that chat, so a stale draft doesn't reappear on the next visit.
func TestDraftClearedOnSend(t *testing.T) {
	saver := newFakeDraftSaver()
	m := newTestModelWithSender(saver, &fakeAccountAdder{})
	chat := Chat{Address: "bob@localhost", Draft: "leftover"}
	m.accounts = []Account{{Name: "me", Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		runCmd(cmd)
	}
	m.chats.Select(0)
	model, cmd := m.openCurrentChat()
	m = model.(Model)
	runCmd(cmd)

	next, _ := m.Update(keyText("hi"))
	m = next.(Model)

	// sendCurrentInput batches a notifyIdleTimeout (10m) tea.Tick alongside
	// the draft-save timer. tea.Tick starts its real timer the instant it's
	// constructed (inside Update, not when the returned Cmd is invoked), so
	// both the Update call and runCmd must run inside the bubble for
	// synctest's fake clock to apply — otherwise the Cmd's timer channel is
	// fed by a real, non-bubble goroutine and synctest won't fast-forward
	// it. Model setup happens outside the bubble since
	// newTestModelWithSender leaks a bubblezone worker goroutine that
	// synctest would otherwise flag as a deadlock.
	synctest.Test(t, func(t *testing.T) {
		next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = next.(Model)
		runCmd(cmd)
	})

	if got, ok := saver.saved["bob@localhost"]; !ok || got != "" {
		t.Fatalf("saved draft for bob after send = (%q, %v), want (\"\", true)", got, ok)
	}
	if chat, ok := m.accounts[0].Chats[0].(Chat); !ok || chat.Draft != "" {
		t.Fatalf("in-memory Chat.Draft after send = %q, want empty", chat.Draft)
	}
}

// TestDraftDebounceSaveWhileTyping checks that a draftSaveMsg firing after
// the debounce window persists the draft even though the chat was never
// switched away from — covers a crash/kill mid-type.
func TestDraftDebounceSaveWhileTyping(t *testing.T) {
	saver := newFakeDraftSaver()
	m := newTestModelWithSender(saver, &fakeAccountAdder{})
	chat := Chat{Address: "bob@localhost"}
	m.accounts = []Account{{Name: "me", Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		runCmd(cmd)
	}
	m.chats.Select(0)
	model, cmd := m.openCurrentChat()
	m = model.(Model)
	runCmd(cmd)

	next, _ := m.Update(keyText("still typing"))
	m = next.(Model)
	if m.draftSaveGen == 0 {
		t.Fatal("draftSaveGen should have advanced after a keystroke")
	}

	// A stale gen (as if a prior keystroke's timer fired late) must not save.
	next, cmd = m.Update(draftSaveMsg{accountIdx: m.currentAccount, addr: "bob@localhost", gen: m.draftSaveGen - 1})
	m = next.(Model)
	runCmd(cmd)
	if _, ok := saver.saved["bob@localhost"]; ok {
		t.Fatal("a stale-gen draftSaveMsg should not have persisted anything")
	}

	next, cmd = m.Update(draftSaveMsg{accountIdx: m.currentAccount, addr: "bob@localhost", gen: m.draftSaveGen})
	m = next.(Model)
	runCmd(cmd)
	if got, want := saver.saved["bob@localhost"], "still typing"; got != want {
		t.Fatalf("saved draft after debounce fired = %q, want %q", got, want)
	}
}

// TestFlushDraftOnExit checks that FlushDraft (called once by main.go right
// after the Bubble Tea program stops) persists whatever's in the compose box
// even though the 3s debounce never got a chance to fire — covering the
// "typed something, quit immediately" case.
func TestFlushDraftOnExit(t *testing.T) {
	saver := newFakeDraftSaver()
	m := newTestModelWithSender(saver, &fakeAccountAdder{})
	chat := Chat{Address: "bob@localhost"}
	m.accounts = []Account{{Name: "me", Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		runCmd(cmd)
	}
	m.chats.Select(0)
	model, cmd := m.openCurrentChat()
	m = model.(Model)
	runCmd(cmd)

	next, _ := m.Update(keyText("last words"))
	m = next.(Model)

	if _, ok := saver.saved["bob@localhost"]; ok {
		t.Fatal("nothing should be persisted yet - only the debounce timer or FlushDraft should save it")
	}
	m.FlushDraft()
	if got, want := saver.saved["bob@localhost"], "last words"; got != want {
		t.Fatalf("saved draft after FlushDraft = %q, want %q", got, want)
	}
}

// TestFlushDraftNoopWhenUnchanged checks FlushDraft doesn't fire a redundant
// save when the compose box still matches the chat's already-stored draft
// (e.g. the user opened the chat, looked at it, and quit without typing).
func TestFlushDraftNoopWhenUnchanged(t *testing.T) {
	saver := newFakeDraftSaver()
	m := newTestModelWithSender(saver, &fakeAccountAdder{})
	chat := Chat{Address: "bob@localhost", Draft: "already saved"}
	m.accounts = []Account{{Name: "me", Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		runCmd(cmd)
	}
	m.chats.Select(0)
	model, cmd := m.openCurrentChat()
	m = model.(Model)
	runCmd(cmd)

	m.FlushDraft()
	if saver.calls != 0 {
		t.Fatalf("FlushDraft should be a no-op when unchanged, got %d SaveDraft call(s)", saver.calls)
	}
}
