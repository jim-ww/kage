package ui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
)

// TestAttachmentBatchDoesNotDuplicateBroadcastMessages covers the duplicate
// rows a multi-file send used to leave behind. The daemon broadcasts each
// message back to every attached client (including this one) the instant it
// goes out, but a batch's ComposedSendResultMsg only lands once every file
// in it has finished uploading - so the broadcasts for all but the last file
// arrive first. With no dedupe on the result, each of those files ended up
// in the chat twice: three sent files showed as five rows here, while the
// peer correctly received three.
func TestAttachmentBatchDoesNotDuplicateBroadcastMessages(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for _, name := range []string{"a.jpg", "b.png", "c.pdf"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("contents"), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}

	sender := &fakeFileSender{
		uploadURL: "https://upload.example.test/f",
		sendIDs:   []string{"file-1", "file-2", "file-3"},
	}
	m := newTestModelWithSender(sender, nil)
	chat := Chat{Name: "Bob", Address: "bob@example.test"}
	m.accounts = []Account{{
		Chats:    []list.Item{chat},
		Messages: map[int][]Message{0: {}},
	}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.selectedView = viewChat
	for _, p := range paths {
		m.stageAttachment(p)
	}

	sendCmd := m.sendCurrentInput()
	if sendCmd == nil {
		t.Fatal("sendCurrentInput returned nil, want the async upload+send command")
	}
	result := collectComposedSendResult(t, sendCmd)
	if len(result.Messages) != 3 {
		t.Fatalf("got %d sent messages in the batch result, want 3", len(result.Messages))
	}

	// The daemon's broadcasts for the files sent earlier in the batch beat
	// the batch result back to the client.
	for _, sent := range result.Messages[:2] {
		next, _, handled := m.handleEventMsg(IncomingMessageMsg{
			AccountIdx: 0, From: "bob@example.test",
			Message: Message{
				ID: sent.ID, LocalID: sent.LocalID, Author: "me", IsMe: true,
				Content: sent.Content, Attachments: sent.Attachments, SentAt: time.Now(),
			},
		})
		if !handled {
			t.Fatal("broadcast IncomingMessageMsg was not handled")
		}
		m = next
	}

	next, _ := m.Update(result)
	m = next.(Model)

	msgs := m.accounts[0].Messages[0]
	if len(msgs) != 3 {
		t.Fatalf("got %d messages for a 3-file send, want 3: %+v", len(msgs), msgs)
	}
	seen := map[string]int{}
	for _, mm := range msgs {
		seen[mm.ID]++
	}
	for _, id := range []string{"file-1", "file-2", "file-3"} {
		if seen[id] != 1 {
			t.Fatalf("message %q appears %d times, want exactly once", id, seen[id])
		}
	}
}

// TestSingleFileSendDoesNotDuplicateBroadcast is the same race on the
// one-file SendFile path, whose FileSendResultMsg is equally async.
func TestSingleFileSendDoesNotDuplicateBroadcast(t *testing.T) {
	m := newTestModelWithSender(&fakeFileSender{}, nil)
	chat := Chat{Name: "Bob", Address: "bob@example.test"}
	m.accounts = []Account{{
		Chats:    []list.Item{chat},
		Messages: map[int][]Message{0: {}},
	}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.selectedView = viewChat

	url := "https://upload.example.test/report.pdf"
	broadcast := IncomingMessageMsg{
		AccountIdx: 0, From: "bob@example.test",
		Message: Message{
			ID: "file-id", Author: "me", IsMe: true,
			Content: url, Attachments: []string{url}, SentAt: time.Now(),
		},
	}
	next, _, _ := m.handleEventMsg(broadcast)
	m = next

	next, _, handled := m.handleEventMsg(FileSendResultMsg{
		AccountIdx: 0, To: "bob@example.test", Path: "/tmp/report.pdf",
		URL: url, ID: "file-id",
	})
	if !handled {
		t.Fatal("FileSendResultMsg was not handled")
	}
	m = next

	if msgs := m.accounts[0].Messages[0]; len(msgs) != 1 {
		t.Fatalf("got %d messages for one sent file, want 1: %+v", len(msgs), msgs)
	}
}
