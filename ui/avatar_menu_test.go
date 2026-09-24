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

func avatarMenuItem(t *testing.T, m Model, label string) int {
	t.Helper()
	if m.contextMenu == nil {
		t.Fatal("no menu is open")
	}
	for i, item := range m.contextMenu.items {
		if item.label == label {
			return i
		}
	}
	t.Fatalf("menu %v has no %q entry", menuLabels(m.contextMenu), label)
	return -1
}

// Built as an ordinary context menu, so it is clickable and
// keyboard-navigable like every other menu rather than carrying its own
// input handling.
func TestAvatarMenuIsAContextMenu(t *testing.T) {
	m := avatarMenuModel(t, &fakeAvatarPublisher{})
	m.width, m.height, m.termHeight = 120, 40, 40
	m.updateSizes()

	if cmd := m.openAvatarMenu(); cmd != nil {
		t.Fatalf("openAvatarMenu returned a notification: %v", cmd())
	}
	if m.contextMenu == nil {
		t.Fatal("no context menu opened")
	}
	if m.contextMenu.title != "Avatar" {
		t.Errorf("menu title = %q, want %q", m.contextMenu.title, "Avatar")
	}
	rendered := ansi.Strip(m.renderContextMenuPopup())
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
	if m.contextMenu != nil {
		t.Error("avatar menu opened with no publisher behind it")
	}
}

// A click on a menu row runs it — the thing the bespoke popup couldn't do.
func TestAvatarMenuRemoveByClick(t *testing.T) {
	pub := &fakeAvatarPublisher{}
	m := avatarMenuModel(t, pub)
	m.width, m.height, m.termHeight = 120, 40, 40
	m.updateSizes()
	m.openAvatarMenu()

	idx := avatarMenuItem(t, m, "Remove avatar")
	cmd := m.contextMenu.items[idx].run(&m)
	if cmd == nil {
		t.Fatal("Remove avatar produced no command")
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
}

func TestAvatarMenuRemoveByKeyboard(t *testing.T) {
	pub := &fakeAvatarPublisher{}
	m := avatarMenuModel(t, pub)
	m.openAvatarMenu()
	m.contextMenu.cursor = avatarMenuItem(t, m, "Remove avatar")

	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter produced no command")
	}
	if got := next.(Model); got.contextMenu != nil {
		t.Error("the menu stayed open after running an item")
	}
	if !containsAvatarPublished(cmd) {
		t.Error("enter produced no AvatarPublishedMsg")
	}
	if pub.removeCall != 1 {
		t.Errorf("RemoveOwnAvatar called %d times, want 1", pub.removeCall)
	}
}

// Picking "Set from file" hands off to the attachment file picker; its
// selection has to publish rather than stage an attachment.
func TestAvatarMenuSetGoesThroughFilePicker(t *testing.T) {
	pub := &fakeAvatarPublisher{}
	m := avatarMenuModel(t, pub)
	m.openAvatarMenu()

	idx := avatarMenuItem(t, m, "Set from file…")
	m.contextMenu.items[idx].run(&m)
	if !m.pickingFile || !m.pickingAvatar {
		t.Fatalf("pickingFile=%v pickingAvatar=%v, want both true", m.pickingFile, m.pickingAvatar)
	}

	cmd := m.setOwnAvatarCmd(0, "/tmp/face.png")
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

// The result is reported the same way every other async result is.
func TestAvatarPublishResultIsANotice(t *testing.T) {
	m := avatarMenuModel(t, &fakeAvatarPublisher{})

	next := updated(t, m, AvatarPublishedMsg{AccountIdx: 0, Err: errors.New("only PNG and JPEG can be published")})
	if !strings.Contains(next.noticeText, "PNG") {
		t.Errorf("notice = %q, want the publisher's reason", next.noticeText)
	}

	next = updated(t, next, AvatarPublishedMsg{AccountIdx: 0, Removed: true})
	if !strings.Contains(next.noticeText, "avatar removed") {
		t.Errorf("notice = %q, want the removal reported", next.noticeText)
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

// containsAvatarPublished reports whether cmd yields an AvatarPublishedMsg,
// looking inside a batch since Update batches its commands.
func containsAvatarPublished(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	switch msg := cmd().(type) {
	case AvatarPublishedMsg:
		return true
	case tea.BatchMsg:
		for _, inner := range msg {
			if containsAvatarPublished(inner) {
				return true
			}
		}
	}
	return false
}
