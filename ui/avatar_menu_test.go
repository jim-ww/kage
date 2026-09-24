package ui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// MessageSender is embedded (and left nil) purely so this satisfies the
// interface New takes: the avatar publisher is discovered by type assertion
// on that same value, and none of these tests send a message.
type fakeAvatarPublisher struct {
	MessageSender
	setPath    string
	setCalls   int
	removeCall int
	err        error
}

func (f *fakeAvatarPublisher) SetOwnAvatar(_ int, path string) error {
	f.setCalls++
	f.setPath = path
	return f.err
}

func (f *fakeAvatarPublisher) RemoveOwnAvatar(int) error {
	f.removeCall++
	return f.err
}

func avatarMenuModel(t *testing.T, pub *fakeAvatarPublisher) Model {
	t.Helper()
	m := newTestModelWithSender(pub, nil)
	m.accounts = []Account{{Name: "me"}}
	m.currentAccount = 0
	m.selectedView = viewAccounts
	return m
}

// The menu is account-scoped, like the storage-password popup — opening it
// from a chat would be asking which account it meant.
func TestAvatarMenuOnlyOpensOnAccountsPanel(t *testing.T) {
	m := avatarMenuModel(t, &fakeAvatarPublisher{})

	m.selectedView = viewChat
	next := updated(t, m, tea.KeyPressMsg{Code: 'v', Text: "v"})
	if next.avatarMenu != nil {
		t.Error("avatar menu opened from the chat view")
	}

	m.selectedView = viewAccounts
	next = updated(t, m, tea.KeyPressMsg{Code: 'v', Text: "v"})
	if next.avatarMenu == nil {
		t.Fatal("avatar menu did not open on the accounts panel")
	}
	rendered := ansi.Strip(next.avatarMenuPrompt())
	for _, want := range []string{"Avatar", "Set from file", "Remove avatar"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("menu %q is missing %q", rendered, want)
		}
	}
}

// Nothing wired up to publish with means the menu would be two dead
// entries.
func TestAvatarMenuNeedsAPublisher(t *testing.T) {
	m := newTestModel(nil)
	m.accounts = []Account{{Name: "me"}}
	m.selectedView = viewAccounts
	if cmd := m.openAvatarMenu(); cmd == nil {
		t.Error("openAvatarMenu with no publisher gave no feedback")
	}
	if m.avatarMenu != nil {
		t.Error("avatar menu opened with no publisher behind it")
	}
}

func TestAvatarMenuRemove(t *testing.T) {
	pub := &fakeAvatarPublisher{}
	m := avatarMenuModel(t, pub)
	m.avatarMenu = &avatarMenuState{index: avatarMenuRemove}

	next, cmd, handled := m.updateAvatarMenuKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !handled || cmd == nil {
		t.Fatalf("enter on Remove: handled=%v cmd=%v", handled, cmd)
	}
	if !next.avatarMenu.busy {
		t.Error("menu is not marked busy while the removal is in flight")
	}

	msg, ok := cmd().(AvatarPublishedMsg)
	if !ok {
		t.Fatalf("command returned %T, want AvatarPublishedMsg", cmd())
	}
	if pub.removeCall != 1 {
		t.Errorf("RemoveOwnAvatar called %d times, want 1", pub.removeCall)
	}
	if !msg.Removed || msg.Err != nil {
		t.Errorf("msg = %+v, want a successful removal", msg)
	}

	done := updated(t, next, msg)
	if done.avatarMenu != nil {
		t.Error("menu stayed open after a successful removal")
	}
}

// Picking "Set from file" hands off to the attachment file picker; its
// selection has to publish rather than stage an attachment.
func TestAvatarMenuSetGoesThroughFilePicker(t *testing.T) {
	pub := &fakeAvatarPublisher{}
	m := avatarMenuModel(t, pub)
	m.avatarMenu = &avatarMenuState{index: avatarMenuSet}

	next, _, handled := m.updateAvatarMenuKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !handled {
		t.Fatal("enter on Set was not handled")
	}
	if !next.pickingFile || !next.pickingAvatar {
		t.Fatalf("pickingFile=%v pickingAvatar=%v, want both true", next.pickingFile, next.pickingAvatar)
	}
	if next.avatarMenu != nil {
		t.Error("menu stayed open behind the picker")
	}

	cmd := next.setOwnAvatarCmd(0, "/tmp/face.png")
	msg, ok := cmd().(AvatarPublishedMsg)
	if !ok {
		t.Fatalf("command returned %T, want AvatarPublishedMsg", cmd())
	}
	if pub.setPath != "/tmp/face.png" || pub.setCalls != 1 {
		t.Errorf("SetOwnAvatar got (%q, %d calls), want (/tmp/face.png, 1)", pub.setPath, pub.setCalls)
	}
	if msg.Removed || msg.Err != nil {
		t.Errorf("msg = %+v, want a successful publish", msg)
	}
}

// Cancelling the picker must clear the avatar mode too, or the next
// attachment would publish itself as an avatar.
func TestCancellingAvatarPickerClearsMode(t *testing.T) {
	m := avatarMenuModel(t, &fakeAvatarPublisher{})
	m.pickingFile, m.pickingAvatar = true, true

	next := updated(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if next.pickingFile || next.pickingAvatar {
		t.Errorf("after esc: pickingFile=%v pickingAvatar=%v, want both false", next.pickingFile, next.pickingAvatar)
	}
}

// The failures worth reporting here — unreadable file, wrong format, too
// large — are ones the user fixes by picking a different file, so the
// message belongs in the popup they're looking at.
func TestAvatarPublishErrorStaysInTheMenu(t *testing.T) {
	pub := &fakeAvatarPublisher{err: errors.New("only PNG and JPEG can be published")}
	m := avatarMenuModel(t, pub)
	m.avatarMenu = &avatarMenuState{busy: true}

	msg := AvatarPublishedMsg{AccountIdx: 0, Err: pub.err}
	next := updated(t, m, msg)
	if next.avatarMenu == nil {
		t.Fatal("menu closed on failure, losing the reason")
	}
	if next.avatarMenu.busy {
		t.Error("menu still marked busy after the result arrived")
	}
	if !strings.Contains(next.avatarMenu.err, "PNG") {
		t.Errorf("menu error = %q, want the publisher's reason", next.avatarMenu.err)
	}
	if !strings.Contains(ansi.Strip(next.avatarMenuPrompt()), "PNG") {
		t.Error("the reason is not rendered in the popup")
	}
}

// A second enter while a publish is in flight would start a second one.
func TestAvatarMenuIgnoresInputWhileBusy(t *testing.T) {
	pub := &fakeAvatarPublisher{}
	m := avatarMenuModel(t, pub)
	m.avatarMenu = &avatarMenuState{index: avatarMenuRemove, busy: true}

	next, cmd, handled := m.updateAvatarMenuKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !handled || cmd != nil {
		t.Errorf("enter while busy: handled=%v cmd=%v, want handled with no command", handled, cmd)
	}
	if pub.removeCall != 0 {
		t.Errorf("RemoveOwnAvatar called %d times while busy", pub.removeCall)
	}

	// Esc still works, so a hung publish can't trap the user in the popup.
	next, _, _ = next.updateAvatarMenuKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if next.avatarMenu != nil {
		t.Error("esc did not close the menu while busy")
	}
}

// updated runs one message through Update and returns the resulting Model.
func updated(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", next)
	}
	return got
}
