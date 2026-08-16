package ui

import (
	"errors"
	"fmt"
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
	// uploadQueued, if true, makes UploadFile report the account as offline
	// (Queued: true, no URL/Err) instead of actually uploading.
	uploadQueued   bool
	uploadCalls    int
	lastUploadText string
	lastUploadOpts SendOptions

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
func (f *fakeFileSender) DeleteQueued(int, string) error          { return nil }
func (f *fakeFileSender) SendFile(_ int, to, path string, opts SendOptions) tea.Msg {
	f.path = path
	f.opts = opts
	return FileSendResultMsg{To: to, Path: path, URL: "https://upload.example.test/report.pdf", ID: "file-id", ReplyToID: opts.ReplyToID}
}
func (f *fakeFileSender) UploadFile(_ int, to, path, text string, opts SendOptions) tea.Msg {
	f.path = path
	f.uploadCalls++
	f.lastUploadText = text
	f.lastUploadOpts = opts
	if f.uploadQueued {
		return FileUploadResultMsg{Path: path, Queued: true}
	}
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
// before the message is sent, and re-selecting the same file toggles it back
// off (deselects it) instead of adding a duplicate.
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

	// Select the file, then select it again — should toggle off.
	next, cmd := m.Update(keyCode(tea.KeyEnter))
	m = next.(Model)
	if msg := nonIdleCmd(cmd); msg != nil {
		t.Fatalf("staging a file must not start any command — nothing uploads until send, got %T", msg)
	}
	if !m.pickingFile {
		t.Fatal("picker closed after selecting a file, want it to stay open for attaching more")
	}
	if len(m.pendingAttachments) != 1 || m.pendingAttachments[0].name != "report.pdf" {
		t.Fatalf("pendingAttachments = %#v, want report.pdf staged exactly once", m.pendingAttachments)
	}

	next, cmd = m.Update(keyCode(tea.KeyEnter))
	m = next.(Model)
	if msg := nonIdleCmd(cmd); msg != nil {
		t.Fatalf("staging a file must not start any command — nothing uploads until send, got %T", msg)
	}
	if len(m.pendingAttachments) != 0 {
		t.Fatalf("pendingAttachments = %#v, want re-selecting the same file to deselect it", m.pendingAttachments)
	}
	if m.selectedAttachment != -1 {
		t.Fatalf("selectedAttachment = %d, want -1 (list empty after deselect)", m.selectedAttachment)
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

// TestAttachedSendQueuesWhenOffline verifies that when the account is
// offline, startAttachedSend stops after the first UploadFile reports
// Queued (rather than trying every staged file and calling Send with no
// URL), never calls Send, and the resulting ComposedSendResultMsg surfaces a
// distinct "queued" notification instead of "send failed".
func TestAttachedSendQueuesWhenOffline(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.jpg")
	pathB := filepath.Join(dir, "b.png")
	for _, p := range []string{pathA, pathB} {
		if err := os.WriteFile(p, []byte("contents"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sender := &fakeFileSender{uploadQueued: true}
	m := newTestModelWithSender(sender, nil)
	chat := Chat{Name: "Bob", Address: "bob@example.test"}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{0: nil}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.stageAttachment(pathA)
	m.stageAttachment(pathB)

	m.input.SetValue("check these out")
	sendCmd := m.sendCurrentInput()
	if sendCmd == nil {
		t.Fatal("sendCurrentInput returned nil, want the async upload+send command")
	}

	result := sendCmd().(ComposedSendResultMsg)
	if !result.Queued {
		t.Fatal("ComposedSendResultMsg.Queued = false, want true")
	}
	if result.Err != nil {
		t.Fatalf("ComposedSendResultMsg.Err = %v, want nil", result.Err)
	}
	if len(sender.sendCalls) != 0 {
		t.Fatalf("got %d Send calls, want 0 (nothing uploaded, nothing to send)", len(sender.sendCalls))
	}
	if sender.uploadCalls != 1 {
		t.Fatalf("got %d UploadFile calls, want 1 (stop at the first offline result)", sender.uploadCalls)
	}
	if sender.lastUploadText != "check these out" {
		t.Fatalf("first UploadFile text = %q, want the typed text", sender.lastUploadText)
	}
	if result.QueuedLocalID == "" {
		t.Fatal("QueuedLocalID is empty, want a LocalID so the placeholder can later be resolved by MessageSendResolvedMsg")
	}
	if result.QueuedPath != pathA {
		t.Fatalf("QueuedPath = %q, want %q (the file that actually got queued)", result.QueuedPath, pathA)
	}

	next, cmd := m.Update(result)
	m = next.(Model)
	if cmd == nil {
		t.Fatal("Update(ComposedSendResultMsg{Queued: true}) returned nil cmd, want a notification")
	}
	got := m.accounts[0].Messages[0]
	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1 (a local Pending placeholder for the queued attachment)", len(got))
	}
	if !got[0].Pending || !got[0].IsMe || got[0].LocalID != result.QueuedLocalID {
		t.Fatalf("placeholder = %+v, want Pending=true IsMe=true LocalID=%q", got[0], result.QueuedLocalID)
	}
}

// TestQueuedAttachmentResolvesPlaceholderWithoutDuplicating guards the
// "non-sent local-only message" bug: adapter.flushOutbox used to resolve a
// successfully-sent queued attachment by broadcasting a brand new
// IncomingMessageMsg instead of patching the Pending placeholder
// TestAttachedSendQueuesWhenOffline shows in place — leaving that
// placeholder stuck Pending forever (never cleared, since nothing ever
// referenced its LocalID) while a second, separate message for the same
// send appeared next to it. MessageSendResolvedMsg with Content/Attachments
// set (what flushOutbox now sends) must instead patch the existing
// placeholder by LocalID, in place, with no duplicate.
func TestQueuedAttachmentResolvesPlaceholderWithoutDuplicating(t *testing.T) {
	m := newTestModelWithSender(&fakeFileSender{uploadQueued: true}, nil)
	chat := Chat{Name: "Bob", Address: "bob@example.test"}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{0: nil}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}

	next, _, handled := m.handleEventMsg(ComposedSendResultMsg{
		AccountIdx: 0, To: chat.Address,
		Queued: true, QueuedLocalID: "local-file-1", QueuedPath: "/tmp/photo.jpg", QueuedText: "check this out",
	})
	if !handled {
		t.Fatal("ComposedSendResultMsg was not handled")
	}
	m = next
	if got := len(m.accounts[0].Messages[0]); got != 1 {
		t.Fatalf("after queuing: got %d messages, want 1 (the placeholder)", got)
	}

	// The upload+send actually happens later, on reconnect - flushOutbox
	// resolves it by the same LocalID, with the real content/URL now known.
	next, _, handled = m.handleEventMsg(MessageSendResolvedMsg{
		AccountIdx: 0, To: chat.Address,
		LocalID: "local-file-1", ID: "real-stanza-id",
		Content:     "check this out\nhttps://upload.example/photo.jpg",
		Attachments: []string{"https://upload.example/photo.jpg"},
	})
	if !handled {
		t.Fatal("MessageSendResolvedMsg was not handled")
	}
	m = next

	got := m.accounts[0].Messages[0]
	if len(got) != 1 {
		t.Fatalf("after resolving: got %d messages, want 1 (patched in place, not duplicated): %+v", len(got), got)
	}
	if got[0].Pending {
		t.Error("Pending = true after resolution, want false")
	}
	if got[0].ID != "real-stanza-id" {
		t.Errorf("ID = %q, want %q", got[0].ID, "real-stanza-id")
	}
	if len(got[0].Attachments) != 1 || got[0].Attachments[0] != "https://upload.example/photo.jpg" {
		t.Errorf("Attachments = %v, want the real uploaded URL", got[0].Attachments)
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

	// Regression: a staged attachment is a local file path, not a URL - even
	// though it's passed as isAttachment=true, it must go straight to
	// xdg-open, never through the HTTP-download path (which would fail
	// trying to GET a bare filesystem path as if it were a URL).
	got := cmd()
	result, ok := got.(openResultMsg)
	if !ok {
		t.Fatalf("open command returned %#v, want openResultMsg", got)
	}
	if result.err != nil && strings.Contains(result.err.Error(), "download") {
		t.Fatalf("opening a local staged attachment went through the download path: %v", result.err)
	}
}

// TestStartOpenSkipsDuplicateDownloadInFlight verifies mashing the open key
// on the same attachment before the first download finishes doesn't start a
// second concurrent download of the same file.
func TestStartOpenSkipsDuplicateDownloadInFlight(t *testing.T) {
	m := newTestModel(nil)
	target := "https://upload.example.test/report.pdf"

	first := m.startOpen(target, true)
	if first == nil {
		t.Fatal("first startOpen returned nil, want a download Cmd")
	}
	if !m.downloadsInFlight[target] {
		t.Fatal("startOpen did not mark the target as in-flight")
	}

	second := m.startOpen(target, true)
	if second != nil {
		t.Fatal("second startOpen while the first is in flight should return nil, not start a duplicate download")
	}

	// Once the transfer's terminal message arrives, the in-flight flag
	// clears and a fresh open is allowed again.
	next, _ := m.Update(openResultMsg{target: target, isAttachment: true})
	m = next.(Model)
	if m.downloadsInFlight[target] {
		t.Fatal("downloadsInFlight entry not cleared after openResultMsg")
	}
	if third := m.startOpen(target, true); third == nil {
		t.Fatal("startOpen after completion should allow a new download")
	}
}

// TestStartOpenNeverDedupesPlainLinks verifies a plain link (not an
// attachment, no download involved) is never gated by downloadsInFlight -
// each press should just reopen the browser, which is cheap and expected.
func TestStartOpenNeverDedupesPlainLinks(t *testing.T) {
	m := newTestModel(nil)
	target := "https://example.com/some/page"
	if cmd := m.startOpen(target, false); cmd == nil {
		t.Fatal("startOpen for a plain link returned nil")
	}
	if m.downloadsInFlight[target] {
		t.Fatal("a plain link should never be tracked in downloadsInFlight")
	}
	if cmd := m.startOpen(target, false); cmd == nil {
		t.Fatal("a second startOpen for the same plain link should still return a Cmd (not deduped)")
	}
}

// TestActionOpenMessageMarksAttachmentVsPlainLink verifies actionOpenMessage
// correctly distinguishes a real attachment from a plain link found in
// Content when there's more than one openable item — this determines
// whether openWithXDGOpen downloads the file first (attachment) or just
// hands the URL to xdg-open, i.e. the browser (plain link). See
// openableItems: attachments always come first.
func TestActionOpenMessageMarksAttachmentVsPlainLink(t *testing.T) {
	m := newTestModel(nil)
	m.selectedView = viewChat
	chat := Chat{Name: "Bob", Address: "bob@example.test"}
	msgs := []Message{{
		Author:      "Bob",
		Content:     "here's the file: https://upload.example.test/report.pdf\nalso see https://example.com/info",
		Attachments: []string{"https://upload.example.test/report.pdf"},
	}}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{0: msgs}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.selectedMsg = 0

	cmd := m.actionOpenMessage()
	if cmd != nil {
		t.Fatal("expected actionOpenMessage to open the picker (2 items), not return a direct-open Cmd")
	}
	if len(m.openItems) != 2 {
		t.Fatalf("openItems = %v, want 2 entries", m.openItems)
	}
	if m.openItemsAttachCount != 1 {
		t.Fatalf("openItemsAttachCount = %d, want 1 (only the real attachment)", m.openItemsAttachCount)
	}
	if m.openItems[0] != "https://upload.example.test/report.pdf" {
		t.Fatalf("openItems[0] = %q, want the attachment first", m.openItems[0])
	}
}

// TestActionOpenMessageSingleAttachmentTreatedAsAttachment verifies the
// single-item shortcut path (no picker) also gets the isAttachment
// distinction right, by checking it doesn't panic/misbehave and returns a
// Cmd (the actual download-vs-browser choice is exercised at the
// openWithXDGOpen level, not asserted here to avoid a real network/exec
// dependency in this test).
func TestActionOpenMessageSingleAttachmentTreatedAsAttachment(t *testing.T) {
	m := newTestModel(nil)
	m.selectedView = viewChat
	chat := Chat{Name: "Bob", Address: "bob@example.test"}
	msgs := []Message{{
		Author:      "Bob",
		Content:     "https://upload.example.test/report.pdf",
		Attachments: []string{"https://upload.example.test/report.pdf"},
	}}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{0: msgs}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.selectedMsg = 0

	if cmd := m.actionOpenMessage(); cmd == nil {
		t.Fatal("expected a direct-open Cmd for the single-attachment case")
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

func TestDeletePromptShowsFilenameNotRawURLForAttachment(t *testing.T) {
	m := newTestModel(nil)
	chat := Chat{Name: "Bob", Address: "bob@example.test"}
	msgs := []Message{{Author: "Bob", Content: "https://upload.example.test/files/x/photo.jpg", Attachments: []string{"https://upload.example.test/files/x/photo.jpg"}}}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{0: msgs}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.selectedMsg = 0
	m.confirmTarget = confirmDeleteMessage

	prompt := m.deletePrompt(60)
	if strings.Contains(prompt, "https://") {
		t.Fatalf("delete prompt leaked raw URL: %q", prompt)
	}
	if !strings.Contains(prompt, "photo.jpg") {
		t.Fatalf("delete prompt = %q, want it to mention the filename", prompt)
	}
}

func TestOpenPopupShowsFilenameForAttachmentAndRawURLForPlainLink(t *testing.T) {
	m := newTestModel(nil)
	m.openItems = []string{
		"https://upload.example.test/files/x/photo.jpg",
		"https://example.com/some/page",
	}
	m.openItemsAttachCount = 1 // only the first entry is a real attachment
	m.openMode = pickerModeOpen

	popup := m.renderOpenPopup()
	if strings.Contains(popup, "https://upload.example.test") {
		t.Fatalf("open popup leaked raw attachment URL: %q", popup)
	}
	if !strings.Contains(popup, "photo.jpg") {
		t.Fatalf("open popup = %q, want the attachment's decoded filename", popup)
	}
	if !strings.Contains(popup, "https://example.com/some/page") {
		t.Fatalf("open popup = %q, want the plain link shown as-is", popup)
	}
}

func TestOpenResultNotificationShowsFilenameNotRawURLForAttachment(t *testing.T) {
	m := newTestModel(nil)
	// Anchor must be exactly 88 hex chars (12-byte IV + 32-byte key) for
	// aesgcm.ParseAESGCMURL to accept it - a too-short fake anchor would
	// make attachmentDisplayName silently fall back to the raw URL,
	// producing a false pass here.
	key := strings.Repeat("ab", 44)
	next, _ := m.Update(openResultMsg{target: "aesgcm://host/photo.jpg#" + key, isAttachment: true})
	m = next.(Model)
	if strings.Contains(m.noticeText, "aesgcm://") || strings.Contains(m.noticeText, key) {
		t.Fatalf("open notification leaked raw aesgcm URL/key: %q", m.noticeText)
	}
	if !strings.Contains(m.noticeText, "photo.jpg") {
		t.Fatalf("open notification = %q, want the decoded filename", m.noticeText)
	}
}

func TestOpenResultNotificationShowsRawURLForPlainLink(t *testing.T) {
	m := newTestModel(nil)
	next, _ := m.Update(openResultMsg{target: "https://example.com/some/page", isAttachment: false})
	m = next.(Model)
	if !strings.Contains(m.noticeText, "https://example.com/some/page") {
		t.Fatalf("open notification = %q, want the plain link shown as-is", m.noticeText)
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

// TestDraggedFilePastedIntoChatStagesAttachment verifies a single-file drop
// stages it as a pending attachment (like the file picker) instead of
// uploading and sending it immediately - nothing touches the network until
// the message is actually sent.
func TestDraggedFilePastedIntoChatStagesAttachment(t *testing.T) {
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
	if cmd != nil {
		t.Fatal("dropping a file must not start any command — nothing uploads until send")
	}
	if len(m.pendingAttachments) != 1 || m.pendingAttachments[0].path != path {
		t.Fatalf("pendingAttachments = %#v, want %q staged", m.pendingAttachments, path)
	}
	if sender.path != "" {
		t.Fatalf("UploadFile/SendFile was called on drop (path=%q), want no upload before send", sender.path)
	}
	if strings.Contains(m.input.Value(), path) {
		t.Fatal("dropped file path leaked into the compose input")
	}
}

// TestMultiFileDroppedIntoChatStagesEachAttachment verifies a multi-file drop
// (delivered as either newline-separated paths, or space-separated
// individually-quoted paths on one line, depending on the terminal) stages
// every file rather than only the first.
func TestMultiFileDroppedIntoChatStagesEachAttachment(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.jpg")
	pathB := filepath.Join(dir, "b png.png") // exercises the quoted-token split
	for _, p := range []string{pathA, pathB} {
		if err := os.WriteFile(p, []byte("contents"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	newModel := func() Model {
		m := newTestModelWithSender(&fakeFileSender{}, nil)
		m.selectedView = viewChat
		chat := Chat{Name: "Bob", Address: "bob@example.test"}
		m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
		if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
			_ = cmd()
		}
		return m
	}

	assertBothStaged := func(t *testing.T, m Model) {
		t.Helper()
		if len(m.pendingAttachments) != 2 {
			t.Fatalf("pendingAttachments = %#v, want 2 staged", m.pendingAttachments)
		}
		if m.pendingAttachments[0].path != pathA || m.pendingAttachments[1].path != pathB {
			t.Fatalf("pendingAttachments = %#v, want %q then %q", m.pendingAttachments, pathA, pathB)
		}
	}

	t.Run("newline separated", func(t *testing.T) {
		m := newModel()
		next, cmd := m.Update(tea.PasteMsg{Content: pathA + "\n" + pathB})
		m = next.(Model)
		if cmd != nil {
			t.Fatal("multi-file drop must not start any command — nothing uploads until send")
		}
		assertBothStaged(t, m)
	})

	t.Run("space separated, quoted", func(t *testing.T) {
		m := newModel()
		next, cmd := m.Update(tea.PasteMsg{Content: pathA + " '" + pathB + "'"})
		m = next.(Model)
		if cmd != nil {
			t.Fatal("multi-file drop must not start any command — nothing uploads until send")
		}
		assertBothStaged(t, m)
	})
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

func TestLongPastedTextIsStagedAsAttachment(t *testing.T) {
	m := newTestModel(nil)
	m.selectedView = viewChat
	chat := Chat{Name: "Bob", Address: "bob@example.test"}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}

	content := strings.Repeat("x", pasteAsFileThreshold+1)
	next, _ := m.Update(tea.PasteMsg{Content: content})
	m = next.(Model)

	if m.input.Value() != "" {
		t.Fatalf("input = %q, want empty (paste should not land in the textinput)", m.input.Value())
	}
	if len(m.pendingAttachments) != 1 {
		t.Fatalf("got %d pending attachments, want 1", len(m.pendingAttachments))
	}
	staged, err := os.ReadFile(m.pendingAttachments[0].path)
	if err != nil {
		t.Fatalf("reading staged attachment: %v", err)
	}
	if string(staged) != content {
		t.Fatalf("staged content = %q, want pasted text", string(staged))
	}
}

func TestBinaryPastedContentIsStagedAsAttachment(t *testing.T) {
	m := newTestModel(nil)
	m.selectedView = viewChat
	chat := Chat{Name: "Bob", Address: "bob@example.test"}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}

	// A minimal PNG header — short (well under pasteAsFileThreshold) but
	// invalid UTF-8, mimicking a "copy image" landing in the paste buffer
	// as raw bytes instead of a file path.
	content := "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\xff\xfe\xfd"
	next, _ := m.Update(tea.PasteMsg{Content: content})
	m = next.(Model)

	if m.input.Value() != "" {
		t.Fatalf("input = %q, want empty (binary paste should not land in the textinput)", m.input.Value())
	}
	if len(m.pendingAttachments) != 1 {
		t.Fatalf("got %d pending attachments, want 1", len(m.pendingAttachments))
	}
	if ext := filepath.Ext(m.pendingAttachments[0].path); ext != ".png" {
		t.Fatalf("staged attachment extension = %q, want .png", ext)
	}
	staged, err := os.ReadFile(m.pendingAttachments[0].path)
	if err != nil {
		t.Fatalf("reading staged attachment: %v", err)
	}
	if string(staged) != content {
		t.Fatalf("staged content = %q, want pasted bytes", string(staged))
	}
}

func TestPasteImageKeybindStartsClipboardRead(t *testing.T) {
	m := newTestModel(nil)
	m.selectedView = viewChat
	chat := Chat{Name: "Bob", Address: "bob@example.test"}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}

	_, cmd, handled := m.updateKeyMsg(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if !handled {
		t.Fatal("ctrl+g was not handled")
	}
	if cmd == nil {
		t.Fatal("ctrl+g did not start a clipboard read command")
	}
}

func TestClipboardImageResultStagesAttachment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.png")
	if err := os.WriteFile(path, []byte("fake png bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newTestModel(nil)

	next, _ := m.Update(clipboardImageResultMsg{path: path})
	m = next.(Model)

	if len(m.pendingAttachments) != 1 {
		t.Fatalf("got %d pending attachments, want 1", len(m.pendingAttachments))
	}
	if m.pendingAttachments[0].path != path {
		t.Fatalf("staged path = %q, want %q", m.pendingAttachments[0].path, path)
	}
}

func TestClipboardImageResultErrorShowsNotification(t *testing.T) {
	m := newTestModel(nil)

	next, _ := m.Update(clipboardImageResultMsg{err: fmt.Errorf("no image on clipboard")})
	m = next.(Model)

	if len(m.pendingAttachments) != 0 {
		t.Fatal("error result should not stage an attachment")
	}
	if !strings.Contains(m.noticeText, "no image on clipboard") {
		t.Fatalf("notice = %q, want it to mention the error", m.noticeText)
	}
}

func TestFirstImageMIMEType(t *testing.T) {
	list := "text/plain\nimage/jpeg\nimage/png\nUTF8_STRING\n"
	got, ok := firstImageMIMEType(list)
	if !ok || got != "image/png" {
		t.Fatalf("got (%q, %v), want (image/png, true) — png should be preferred", got, ok)
	}

	list = "text/plain\nimage/jpeg\n"
	got, ok = firstImageMIMEType(list)
	if !ok || got != "image/jpeg" {
		t.Fatalf("got (%q, %v), want (image/jpeg, true) when png isn't offered", got, ok)
	}

	if _, ok := firstImageMIMEType("text/plain\nUTF8_STRING\n"); ok {
		t.Fatal("want false when no image/* type is offered")
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
