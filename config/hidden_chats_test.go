package config

import (
	"os"
	"path/filepath"
	"testing"
)

func hiddenChatsStatePath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSetChatHidden(t *testing.T) {
	path := hiddenChatsStatePath(t)

	if err := SetChatHidden(path, "me@x", "b@x", true); err != nil {
		t.Fatalf("hiding: %v", err)
	}
	if err := SetChatHidden(path, "me@x", "a@x", true); err != nil {
		t.Fatalf("hiding a second: %v", err)
	}
	st, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	// Sorted, so the file doesn't churn on every write.
	if got := st.HiddenChats["me@x"]; len(got) != 2 || got[0] != "a@x" || got[1] != "b@x" {
		t.Fatalf("hidden = %v, want [a@x b@x]", got)
	}

	// Hiding the same chat twice must not duplicate it.
	if err := SetChatHidden(path, "me@x", "a@x", true); err != nil {
		t.Fatal(err)
	}
	if st, _ = loadState(path); len(st.HiddenChats["me@x"]) != 2 {
		t.Errorf("hidden = %v after re-hiding, want no duplicate", st.HiddenChats["me@x"])
	}

	// Unhiding the last one drops the account's entry rather than leaving
	// an empty list behind in the file.
	if err := SetChatHidden(path, "me@x", "a@x", false); err != nil {
		t.Fatal(err)
	}
	if err := SetChatHidden(path, "me@x", "B@X", false); err != nil {
		t.Fatal(err)
	}
	st, _ = loadState(path)
	if _, present := st.HiddenChats["me@x"]; present {
		t.Errorf("hidden = %v, want the account's entry gone (and the match case-insensitive)", st.HiddenChats)
	}
}

func TestSetChatHiddenKeepsAccountsApart(t *testing.T) {
	path := hiddenChatsStatePath(t)

	if err := SetChatHidden(path, "one@x", "a@x", true); err != nil {
		t.Fatal(err)
	}
	if err := SetChatHidden(path, "two@x", "a@x", true); err != nil {
		t.Fatal(err)
	}
	if err := SetChatHidden(path, "one@x", "a@x", false); err != nil {
		t.Fatal(err)
	}
	st, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := st.HiddenChats["one@x"]; present {
		t.Error("unhiding for one account left it hidden")
	}
	if got := st.HiddenChats["two@x"]; len(got) != 1 {
		t.Errorf("the other account's hidden list = %v, want a@x still hidden", got)
	}
}

func TestSetChatHiddenRejectsEmptyKeys(t *testing.T) {
	path := hiddenChatsStatePath(t)
	if err := SetChatHidden(path, "", "a@x", true); err == nil {
		t.Error("accepted an empty account JID")
	}
	if err := SetChatHidden(path, "me@x", "", true); err == nil {
		t.Error("accepted an empty chat address")
	}
}
