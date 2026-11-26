package ui

import (
	"os/exec"
	"testing"
)

func TestMessageDecryptedReplacesCiphertext(t *testing.T) {
	m := newDraftSyncModel(t, "")
	m.accounts[0].Messages[0] = []Message{{StoreID: 7, Content: "-----BEGIN PGP MESSAGE-----", DecryptFailed: true}}
	next, _, _ := m.handleEventMsg(MessageDecryptedMsg{AccountIdx: 0, From: "bob@localhost", StoreID: 7, Content: "hello"})
	got := next.accounts[0].Messages[0][0]
	if got.Content != "hello" || got.DecryptFailed {
		t.Fatalf("got %+v", got)
	}
}

func TestGPGLockedAsksOnce(t *testing.T) {
	m := newDraftSyncModel(t, "")
	m.SetGPGUnlock(func(string) *exec.Cmd { return exec.Command("true") })
	next, _, _ := m.handleEventMsg(GPGLockedMsg{AccountIdx: 0, KeyID: "ABCD"})
	if !next.modals.Open() || !next.gpgUnlocking {
		t.Fatal("locked key didn't ask to unlock")
	}
	if cmd := next.askGPGUnlock(GPGLockedMsg{AccountIdx: 0, KeyID: "ABCD"}); cmd != nil {
		t.Fatal("asked again while already asking")
	}
}
