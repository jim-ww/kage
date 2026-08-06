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

type sendCall struct {
	body string
	opts SendOptions
}

type fakeFileSender struct {
	path string
	opts SendOptions

	// uploadURL is returned by UploadFile for every call unless
	// uploadErr is set, in which case it's returned as the error instead.
	uploadURL string
	uploadErr error

	// sendCalls records every Send call, in order, for tests that need to
	// verify per-message behavior (e.g. one Send per staged attachment).
	sendCalls []sendCall
	sendIDs   []string // returned by Send, one per call, cycling if shorter than the number of calls
}

func (f *fakeFileSender) Send(_ int, _ string, body string, opts SendOptions) (string, error) {
	f.sendCalls = append(f.sendCalls, sendCall{body: body, opts: opts})
	if len(f.sendIDs) == 0 {
		return "", nil
	}
	return f.sendIDs[(len(f.sendCalls)-1)%len(f.sendIDs)], nil
}
func (f *fakeFileSender) SetTyping(int, string, bool) error       { return nil }
func (f *fakeFileSender) MarkRetracted(int, string, string) error { return nil }
func (f *fakeFileSender) SendFile(_ int, to, path string, opts SendOptions) tea.Msg {
	f.path = path
	f.opts = opts
	return FileSendResultMsg{To: to, Path: path, URL: "https://upload.example.test/report.pdf", ID: "file-id", ReplyToID: opts.ReplyToID}
}
func (f *fakeFileSender) UploadFile(_ int, to, path string) tea.Msg {
	f.path = path
	if f.uploadErr != nil {
		return FileUploadResultMsg{Path: path, Err: f.uploadErr}
	}
	url := f.uploadURL
	if url == "" {
		url = "https://upload.example.test/" + filepath.Base(path)
	}
	return FileUploadResultMsg{Path: path, URL: url}
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

// TestFilePickerEnterStagesAttachmentWithoutUploading verifies the ctrl+f
// flow: selecting a file only stages it locally (no network call — nothing
// uploads until send), the picker stays open so more files can be attached
// before the message is sent, and re-selecting the same file just re-selects
// its existing chip instead of adding a duplicate.
func TestFilePickerEnterStagesAttachmentWithoutUploading(t *testing.T) {
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

	// Select the same file twice.
	next, cmd := m.Update(keyCode(tea.KeyEnter))
	m = next.(Model)
	if cmd != nil {
		t.Fatal("staging a file must not start any command — nothing uploads until send")
	}
	next, cmd = m.Update(keyCode(tea.KeyEnter))
	m = next.(Model)
	if cmd != nil {
		t.Fatal("staging a file must not start any command — nothing uploads until send")
	}

	if !m.pickingFile {
		t.Fatal("picker closed after selecting a file, want it to stay open for attaching more")
	}
	if len(m.pendingAttachments) != 1 || m.pendingAttachments[0].name != "report.pdf" {
		t.Fatalf("pendingAttachments = %#v, want report.pdf staged exactly once (deduped)", m.pendingAttachments)
	}
	if m.selectedAttachment != 0 {
		t.Fatalf("selectedAttachment = %d, want 0", m.selectedAttachment)
	}
	if sender.path != "" {
		t.Fatalf("UploadFile was called during staging (path=%q), want no upload before send", sender.path)
	}
}

// TestFilePickerPopupShrinksWhenAttachmentsRowAppears guards against a
// rendering bug: staging the first attachment adds a row to
// inputAreaHeight() (the chip line above the compose box), but the file
// picker popup's own height was fixed when it opened. Without reshrinking
// it, the popup grows a row taller than before, pushing the input box — the
// only feedback that a file was actually attached — down by that same row
// (off screen in a terminal sized exactly to fit). This checks the popup's
// rendered height doesn't change across the 0-to-1-attachment transition;
// it deliberately doesn't assert an absolute "fits in m.height" bound,
// since the popup layout already runs a row or two tight even with no
// attachments staged — a separate, preexisting approximation this change
// doesn't touch.
func TestFilePickerPopupShrinksWhenAttachmentsRowAppears(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(path, []byte("contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newTestModelWithSender(&fakeFileSender{}, nil)
	// newTestModel's m.height comes out of updateSizes() using termHeight,
	// which the fixture leaves at 0 (see TestComposeInputHeightOverride) —
	// give it real room to work with.
	m.termHeight = 24
	m.updateSizes()
	chat := Chat{Name: "Bob", Address: "bob@example.test"}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.filePicker.CurrentDirectory = dir
	m.pickingFile = true
	m.filePicker.SetHeight(max(1, m.height-m.inputAreaHeight()-6))
	next, _ := m.Update(m.filePicker.Init()())
	m = next.(Model)

	before := m.renderChatArea(m.styles.colors)
	beforeLines := strings.Count(before, "\n") + 1

	next, _ = m.Update(keyCode(tea.KeyEnter))
	m = next.(Model)

	after := m.renderChatArea(m.styles.colors)
	afterLines := strings.Count(after, "\n") + 1
	if afterLines > beforeLines {
		t.Fatalf("chat area grew from %d to %d lines after staging an attachment — the extra row pushes the compose box further down instead of the popup shrinking to make room", beforeLines, afterLines)
	}
	if !strings.Contains(after, "report.pdf") {
		t.Fatal("attachment chip not visible while the file picker is still open")
	}
}

// TestTabCyclesSelectedAttachmentAndBackspaceRemovesIt covers the requested
// preview/selection UX: Tab moves the highlighted attachment forward,
// wrapping at the end, and Backspace on an empty compose box removes
// whichever one is currently highlighted (not just the last one staged).
func TestTabCyclesSelectedAttachmentAndBackspaceRemovesIt(t *testing.T) {
	dir := t.TempDir()
	m := newTestModelWithSender(&fakeFileSender{}, nil)
	m.selectedView = viewChat
	chat := Chat{Name: "Bob", Address: "bob@example.test"}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.stageAttachment(filepath.Join(dir, "a.txt"))
	m.stageAttachment(filepath.Join(dir, "b.txt"))
	m.stageAttachment(filepath.Join(dir, "c.txt"))
	if m.selectedAttachment != 2 {
		t.Fatalf("selectedAttachment = %d, want 2 (most recently staged)", m.selectedAttachment)
	}

	next, _ := m.Update(keyCode(tea.KeyTab))
	m = next.(Model)
	if m.selectedAttachment != 0 {
		t.Fatalf("selectedAttachment after tab = %d, want 0 (wrapped)", m.selectedAttachment)
	}

	next, _ = m.Update(keyCode(tea.KeyTab))
	m = next.(Model)
	if m.selectedAttachment != 1 {
		t.Fatalf("selectedAttachment after second tab = %d, want 1", m.selectedAttachment)
	}

	next, _ = m.Update(keyCode(tea.KeyBackspace))
	m = next.(Model)
	if len(m.pendingAttachments) != 2 {
		t.Fatalf("got %d pendingAttachments, want 2 after removing one", len(m.pendingAttachments))
	}
	for _, a := range m.pendingAttachments {
		if a.name == "b.txt" {
			t.Fatalf("b.txt (the highlighted one) was not removed: %#v", m.pendingAttachments)
		}
	}
}

// TestSendWithAttachmentAndReplyCombinesIntoOneMessage covers the requested
// flow: stage files, type a reply, hit enter — nothing uploads until this
// point, then one message goes out carrying both the text and every
// attachment, wired as a reply exactly like a text-only send.
func TestSendWithAttachmentAndReplyCombinesIntoOneMessage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(path, []byte("contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	sender := &fakeFileSender{uploadURL: "https://upload.example.test/report.pdf"}
	m := newTestModelWithSender(sender, nil)
	chat := Chat{Name: "Bob", Address: "bob@example.test"}
	m.accounts = []Account{{
		Chats: []list.Item{chat},
		Messages: map[int][]Message{
			0: {{ID: "orig-id", Author: "Bob", Content: "hi there"}},
		},
	}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.selectedMsg = 0
	m.replyToIdx = 0
	m.stageAttachment(path)

	m.input.SetValue("check this out")
	sendCmd := m.sendCurrentInput()
	if sendCmd == nil {
		t.Fatal("sendCurrentInput returned nil, want the async upload+send command")
	}
	if m.replyToIdx != -1 {
		t.Fatal("replyToIdx not cleared immediately on send")
	}
	if len(m.pendingAttachments) != 0 {
		t.Fatalf("pendingAttachments not cleared immediately on send: %#v", m.pendingAttachments)
	}
	if sender.path != "" {
		t.Fatal("upload started before the send command actually ran")
	}

	result := sendCmd()
	if sender.path != path {
		t.Fatalf("uploaded path = %q, want %q", sender.path, path)
	}
	next, _ := m.Update(result)
	m = next.(Model)

	msgs := m.accounts[0].Messages[0]
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	sent := msgs[1]
	if sent.ReplyTo == nil || *sent.ReplyTo != 0 {
		t.Fatalf("sent message ReplyTo = %v, want pointer to 0", sent.ReplyTo)
	}
	if len(sent.Attachments) != 1 || sent.Attachments[0] != "https://upload.example.test/report.pdf" {
		t.Fatalf("sent message Attachments = %#v, want the uploaded URL", sent.Attachments)
	}
	if !strings.Contains(sent.Content, "check this out") || !strings.Contains(sent.Content, "https://upload.example.test/report.pdf") {
		t.Fatalf("sent message Content = %q, want text and attachment URL both present", sent.Content)
	}
}

// TestMultiAttachmentSendSplitsIntoSeparateMessages verifies staging several
// files sends one message per file (not one message combining every
// attachment) - other clients (verified against Dino's source) only read
// the first attachment URL in a message and silently drop the rest, so a
// combined multi-attachment message would only ever show its first file
// elsewhere. Text goes on the first message only; every message shares the
// same reply target.
func TestMultiAttachmentSendSplitsIntoSeparateMessages(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.jpg")
	pathB := filepath.Join(dir, "b.png")
	for _, p := range []string{pathA, pathB} {
		if err := os.WriteFile(p, []byte("contents"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sender := &fakeFileSender{sendIDs: []string{"msg-a", "msg-b"}}
	m := newTestModelWithSender(sender, nil)
	chat := Chat{Name: "Bob", Address: "bob@example.test"}
	m.accounts = []Account{{
		Chats: []list.Item{chat},
		Messages: map[int][]Message{
			0: {{ID: "orig-id", Author: "Bob", Content: "hi there"}},
		},
	}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.selectedMsg = 0
	m.replyToIdx = 0
	m.stageAttachment(pathA)
	m.stageAttachment(pathB)

	m.input.SetValue("check these out")
	sendCmd := m.sendCurrentInput()
	if sendCmd == nil {
		t.Fatal("sendCurrentInput returned nil, want the async upload+send command")
	}

	result := sendCmd()
	if len(sender.sendCalls) != 2 {
		t.Fatalf("got %d Send calls, want 2 (one per staged attachment)", len(sender.sendCalls))
	}
	first, second := sender.sendCalls[0], sender.sendCalls[1]
	if !strings.Contains(first.body, "check these out") {
		t.Fatalf("first message body = %q, want the typed text", first.body)
	}
	if strings.Contains(second.body, "check these out") {
		t.Fatalf("second message body = %q, want no typed text (only the first message carries it)", second.body)
	}
	if first.opts.ReplyToID != "orig-id" || second.opts.ReplyToID != "orig-id" {
		t.Fatalf("ReplyToID = %q / %q, want both %q", first.opts.ReplyToID, second.opts.ReplyToID, "orig-id")
	}
	if len(first.opts.OOBURLs) != 1 || len(second.opts.OOBURLs) != 1 {
		t.Fatalf("each Send call should carry exactly one OOB URL, got %v / %v", first.opts.OOBURLs, second.opts.OOBURLs)
	}

	next, _ := m.Update(result)
	m = next.(Model)

	msgs := m.accounts[0].Messages[0]
	if len(msgs) != 3 { // original + 2 sent
		t.Fatalf("got %d messages, want 3", len(msgs))
	}
	for _, sent := range msgs[1:] {
		if sent.ReplyTo == nil || *sent.ReplyTo != 0 {
			t.Fatalf("sent message ReplyTo = %v, want pointer to 0", sent.ReplyTo)
		}
		if len(sent.Attachments) != 1 {
			t.Fatalf("sent message Attachments = %#v, want exactly one", sent.Attachments)
		}
	}
}

// TestComposedSendFailureDoesNotAddMessage mirrors
// TestFileSendFailureDoesNotAddMessage for the combined text+attachments
// send path.
func TestComposedSendFailureDoesNotAddMessage(t *testing.T) {
	m := newTestModel(nil)
	chat := Chat{Name: "Bob", Address: "bob@example.test"}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}

	next, _ := m.Update(ComposedSendResultMsg{AccountIdx: 0, To: chat.Address, Err: errors.New("upload unavailable")})
	m = next.(Model)
	if got := len(m.accounts[0].Messages[0]); got != 0 {
		t.Fatalf("got %d messages after failed send, want 0", got)
	}
}

// TestOpenMsgOpensSelectedPendingAttachment covers the requested preview
// affordance: ctrl+o with an attachment staged opens that local file
// (rather than falling through to actionOpenMessage's history lookup).
func TestOpenMsgOpensSelectedPendingAttachment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(path, []byte("contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newTestModelWithSender(&fakeFileSender{}, nil)
	m.selectedView = viewChat
	chat := Chat{Name: "Bob", Address: "bob@example.test"}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.stageAttachment(path)

	_, cmd, handled := m.updateKeyMsg(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if !handled {
		t.Fatal("ctrl+o was not handled while an attachment is staged")
	}
	if cmd == nil {
		t.Fatal("ctrl+o did not return an open command")
	}
}

func TestReplyPreviewShowsFilenameNotRawURLForAttachment(t *testing.T) {
	m := newTestModel(nil)
	msgs := []Message{
		{Author: "Bob", Content: "https://upload.example.test/files/x/photo.jpg", Attachments: []string{"https://upload.example.test/files/x/photo.jpg"}},
		{Author: "me", Content: "nice", ReplyTo: new(int)},
	}
	preview := m.replyPreview(0, msgs)
	if strings.Contains(preview, "https://") {
		t.Fatalf("reply preview leaked raw URL: %q", preview)
	}
	if !strings.Contains(preview, "photo.jpg") {
		t.Fatalf("reply preview = %q, want it to mention the filename", preview)
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
