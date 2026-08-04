package ui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

type fakeFileSender struct {
	path string
}

func (f *fakeFileSender) Send(int, string, string, SendOptions) (string, error) { return "", nil }
func (f *fakeFileSender) SetTyping(int, string, bool) error                     { return nil }
func (f *fakeFileSender) SendFile(_ int, to, path string) tea.Msg {
	f.path = path
	return FileSendResultMsg{To: to, Path: path, URL: "https://upload.example.test/report.pdf", ID: "file-id"}
}

func TestFilePickerReceivesAsyncDirectoryRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(path, []byte("contents"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := newTestModel(nil)
	m.filePicker.CurrentDirectory = dir
	m.filePicker.SetHeight(8)
	m.pickingFile = true
	cmd := m.filePicker.Init()
	next, _ := m.Update(cmd())
	m = next.(Model)

	if got := m.filePicker.HighlightedPath(); got != path {
		t.Fatalf("highlighted path = %q, want %q", got, path)
	}
	if strings.Contains(m.filePicker.View(), "No Files Found") {
		t.Fatal("picker still reports an empty directory")
	}
}

func TestFilePickerEnterStartsFileSend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(path, []byte("contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	sender := &fakeFileSender{}
	m := newTestModelWithSender(sender, nil)
	chat := Chat{Name: "Bob", Address: "bob@example.test"}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.filePicker.CurrentDirectory = dir
	m.filePicker.SetHeight(8)
	m.pickingFile = true
	next, _ := m.Update(m.filePicker.Init()())
	m = next.(Model)

	next, cmd := m.Update(keyCode(tea.KeyEnter))
	m = next.(Model)
	if m.pickingFile {
		t.Fatal("picker stayed open after selecting a file")
	}
	if m.noticeText != "uploading report.pdf..." {
		t.Fatalf("notice = %q, want upload progress", m.noticeText)
	}
	if cmd == nil {
		t.Fatal("selecting a file did not start a send command")
	}
	_ = cmd()
	if sender.path != path {
		t.Fatalf("sent path = %q, want %q", sender.path, path)
	}
}

func TestFileSendResultAddsAttachmentToTargetChat(t *testing.T) {
	m := newTestModel(nil)
	chat := Chat{Name: "Bob", Address: "bob@example.test"}
	m.accounts = []Account{{
		Name:     "me@example.test",
		Chats:    []list.Item{chat},
		Messages: map[int][]Message{},
	}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}

	next, _ := m.Update(FileSendResultMsg{
		AccountIdx: 0,
		To:         "bob@example.test",
		Path:       "/tmp/report.pdf",
		URL:        "https://upload.example.test/a/report.pdf",
		ID:         "file-message-id",
	})
	m = next.(Model)
	msgs := m.accounts[0].Messages[0]
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].ID != "file-message-id" || !msgs[0].IsMe {
		t.Fatalf("unexpected sent message: %#v", msgs[0])
	}
	if len(msgs[0].Attachments) != 1 || msgs[0].Attachments[0] != "https://upload.example.test/a/report.pdf" {
		t.Fatalf("unexpected attachments: %#v", msgs[0].Attachments)
	}
}

func TestFileSendResultInitializesMissingMessageMap(t *testing.T) {
	m := newTestModel(nil)
	chat := Chat{Name: "Bob", Address: "bob@example.test"}
	m.accounts = []Account{{Chats: []list.Item{chat}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}

	next, _ := m.Update(FileSendResultMsg{
		AccountIdx: 0,
		To:         chat.Address,
		Path:       "/tmp/report.pdf",
		URL:        "https://upload.example.test/a/report.pdf",
	})
	m = next.(Model)
	if got := len(m.accounts[0].Messages[0]); got != 1 {
		t.Fatalf("got %d messages, want 1", got)
	}
}

func TestIncomingMessageInitializesMissingMessageMap(t *testing.T) {
	m := newTestModel(nil)
	chat := Chat{Name: "Bob", Address: "bob@example.test"}
	m.accounts = []Account{{Chats: []list.Item{chat}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}

	next, _ := m.Update(IncomingMessageMsg{
		AccountIdx: 0,
		From:       chat.Address,
		Message:    Message{Content: "hello"},
	})
	m = next.(Model)
	if got := len(m.accounts[0].Messages[0]); got != 1 {
		t.Fatalf("got %d messages, want 1", got)
	}
}

func TestDraggedFilePastedIntoChatStartsFileSend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(path, []byte("contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	sender := &fakeFileSender{}
	m := newTestModelWithSender(sender, nil)
	m.selectedView = viewChat
	chat := Chat{Name: "Bob", Address: "bob@example.test"}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}

	next, cmd := m.Update(tea.PasteMsg{Content: path})
	m = next.(Model)
	if m.noticeText != "uploading report.pdf..." {
		t.Fatalf("notice = %q, want upload progress", m.noticeText)
	}
	if cmd == nil {
		t.Fatal("dropping a file did not start a send command")
	}
	_ = cmd()
	if sender.path != path {
		t.Fatalf("sent path = %q, want %q", sender.path, path)
	}
	if strings.Contains(m.input.Value(), path) {
		t.Fatal("dropped file path leaked into the compose input")
	}
}

func TestPastedTextThatIsNotAFilePathGoesToInput(t *testing.T) {
	m := newTestModel(nil)
	m.selectedView = viewChat
	chat := Chat{Name: "Bob", Address: "bob@example.test"}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}

	next, _ := m.Update(tea.PasteMsg{Content: "hello world"})
	m = next.(Model)
	if m.input.Value() != "hello world" {
		t.Fatalf("input = %q, want pasted text", m.input.Value())
	}
}

func TestFileSendFailureDoesNotAddMessage(t *testing.T) {
	m := newTestModel(nil)
	chat := Chat{Name: "Bob", Address: "bob@example.test"}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}

	next, _ := m.Update(FileSendResultMsg{AccountIdx: 0, To: chat.Address, Path: "/tmp/report.pdf", Err: errors.New("upload unavailable")})
	m = next.(Model)
	if got := len(m.accounts[0].Messages[0]); got != 0 {
		t.Fatalf("got %d messages after failed upload, want 0", got)
	}
}
