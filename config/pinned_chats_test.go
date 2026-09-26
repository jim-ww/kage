package config

import "testing"

func TestSetChatPinned(t *testing.T) {
	path := hiddenChatsStatePath(t)

	if err := SetChatPinned(path, "me@x", "b@x", true); err != nil {
		t.Fatalf("pinning: %v", err)
	}
	if err := SetChatPinned(path, "me@x", "a@x", true); err != nil {
		t.Fatalf("pinning a second: %v", err)
	}
	// Pinning the same chat twice must not duplicate it.
	if err := SetChatPinned(path, "me@x", "a@x", true); err != nil {
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
	if err := SetChatPinned(path, "me@x", "A@X", false); err != nil {
		t.Fatal(err)
	}
	if err := SetChatPinned(path, "me@x", "b@x", false); err != nil {
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
	if err := SetChatHidden(path, "me@x", "a@x", true); err != nil {
		t.Fatal(err)
	}
	if err := SetChatPinned(path, "me@x", "b@x", true); err != nil {
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
	if err := SetChatPinned(path, "", "a@x", true); err == nil {
		t.Fatal("pinning without an account should fail")
	}
	if err := SetChatPinned(path, "me@x", "", true); err == nil {
		t.Fatal("pinning without a chat address should fail")
	}
}
