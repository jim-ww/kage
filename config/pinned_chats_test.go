package config

import (
	"testing"

	"github.com/jim-ww/kage/ui"
)

func TestSetChatPinned(t *testing.T) {
	path := hiddenChatsStatePath(t)

	if err := SetChatFlag(path, "me@x", "b@x", ui.ChatPinned, true); err != nil {
		t.Fatalf("pinning: %v", err)
	}
	if err := SetChatFlag(path, "me@x", "a@x", ui.ChatPinned, true); err != nil {
		t.Fatalf("pinning a second: %v", err)
	}
	// Pinning the same chat twice must not duplicate it.
	if err := SetChatFlag(path, "me@x", "a@x", ui.ChatPinned, true); err != nil {
		t.Fatal(err)
	}
	st, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.PinnedChats["me@x"]; len(got) != 2 || got[0] != "a@x" || got[1] != "b@x" {
		t.Fatalf("pinned = %v, want [a@x b@x]", got)
	}

	// Unpinning the last one drops the account key rather than leaving a
	// dangling empty entry behind.
	if err := SetChatFlag(path, "me@x", "A@X", ui.ChatPinned, false); err != nil {
		t.Fatal(err)
	}
	if err := SetChatFlag(path, "me@x", "b@x", ui.ChatPinned, false); err != nil {
		t.Fatal(err)
	}
	st, err = loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.PinnedChats["me@x"]; ok {
		t.Fatalf("pinned entry kept after unpinning everything: %v", st.PinnedChats)
	}
}

// Pinning and hiding are separate lists; neither may clobber the other.
func TestPinningAndHidingAreIndependent(t *testing.T) {
	path := hiddenChatsStatePath(t)
	if err := SetChatFlag(path, "me@x", "a@x", ui.ChatHidden, true); err != nil {
		t.Fatal(err)
	}
	if err := SetChatFlag(path, "me@x", "b@x", ui.ChatPinned, true); err != nil {
		t.Fatal(err)
	}
	st, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.HiddenChats["me@x"]; len(got) != 1 || got[0] != "a@x" {
		t.Fatalf("hidden = %v, want [a@x]", got)
	}
	if got := st.PinnedChats["me@x"]; len(got) != 1 || got[0] != "b@x" {
		t.Fatalf("pinned = %v, want [b@x]", got)
	}
}

func TestSetChatPinnedRejectsEmptyKeys(t *testing.T) {
	path := hiddenChatsStatePath(t)
	if err := SetChatFlag(path, "", "a@x", ui.ChatPinned, true); err == nil {
		t.Fatal("pinning without an account should fail")
	}
	if err := SetChatFlag(path, "me@x", "", ui.ChatPinned, true); err == nil {
		t.Fatal("pinning without a chat address should fail")
	}
}

func TestSetChatMuted(t *testing.T) {
	path := hiddenChatsStatePath(t)
	if err := SetChatFlag(path, "me@x", "b@x", ui.ChatMuted, true); err != nil {
		t.Fatal(err)
	}
	st, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.MutedChats["me@x"]; len(got) != 1 || got[0] != "b@x" {
		t.Fatalf("muted = %v", got)
	}
	if err := SetChatFlag(path, "me@x", "B@X", ui.ChatMuted, false); err != nil {
		t.Fatal(err)
	}
	if st, err = loadState(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.MutedChats["me@x"]; ok {
		t.Fatalf("muted entry kept after unmuting: %v", st.MutedChats)
	}
}
