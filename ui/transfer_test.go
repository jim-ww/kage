package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestRenderProgressBarFillsProportionally(t *testing.T) {
	tests := []struct {
		percent, width int
		wantFilled     int
	}{
		{0, 10, 0},
		{50, 10, 5},
		{100, 10, 10},
		{100, 0, 1}, // width clamped to >=1
	}
	for _, tt := range tests {
		bar := renderProgressBar(tt.percent, tt.width)
		gotFilled := strings.Count(bar, "#")
		if gotFilled != tt.wantFilled {
			t.Errorf("renderProgressBar(%d, %d) = %q, filled %d, want %d", tt.percent, tt.width, bar, gotFilled, tt.wantFilled)
		}
	}
}

func TestFormatTransferLineShowsPercentWhenTotalKnown(t *testing.T) {
	line := formatTransferLine(FileTransferProgressMsg{Label: "uploading a.jpg", Sent: 50, Total: 100})
	if !strings.Contains(line, "uploading a.jpg") || !strings.Contains(line, "50%") {
		t.Fatalf("formatTransferLine = %q, want label and 50%%", line)
	}
}

func TestFormatTransferLineShowsBytesWhenTotalUnknown(t *testing.T) {
	line := formatTransferLine(FileTransferProgressMsg{Label: "downloading b.png", Sent: 2048, Total: 0})
	if !strings.Contains(line, "downloading b.png") || !strings.Contains(line, "2.0 KiB") {
		t.Fatalf("formatTransferLine = %q, want label and byte count, no percent", line)
	}
	if strings.Contains(line, "%") {
		t.Fatalf("formatTransferLine = %q, should not show a percent with unknown total", line)
	}
}

func TestClearTransferBlocksLateProgressFromResurrectingIt(t *testing.T) {
	m := newTestModel(nil)
	m.setTransferProgress(FileTransferProgressMsg{ID: "a", Label: "uploading a", Sent: 5, Total: 10})
	m.clearTransfer("a") // terminal result msg arrives before the final progress msg

	// A late 100% progress event for the same ID, delivered after
	// clearTransfer, must not resurrect the entry - it would never be
	// cleared again since the terminal message already fired.
	m.setTransferProgress(FileTransferProgressMsg{ID: "a", Label: "uploading a", Sent: 10, Total: 10})

	if lines := m.renderTransferLines(); len(lines) != 0 {
		t.Fatalf("renderTransferLines = %v, want none - late progress after clearTransfer must be ignored", lines)
	}

	// Once genuinely restarted (e.g. re-sending the same file), progress
	// for the same ID must work again.
	delete(m.finishedTransfers, "a")
	m.setTransferProgress(FileTransferProgressMsg{ID: "a", Label: "uploading a", Sent: 1, Total: 10})
	if lines := m.renderTransferLines(); len(lines) != 1 {
		t.Fatalf("renderTransferLines = %v, want 1 after restart", lines)
	}
}

func TestModelTracksMultipleTransfersInStartOrder(t *testing.T) {
	m := newTestModel(nil)
	m.setTransferProgress(FileTransferProgressMsg{ID: "a", Label: "uploading a", Sent: 1, Total: 10})
	m.setTransferProgress(FileTransferProgressMsg{ID: "b", Label: "uploading b", Sent: 2, Total: 10})
	m.setTransferProgress(FileTransferProgressMsg{ID: "a", Label: "uploading a", Sent: 5, Total: 10}) // update, not a new entry

	lines := m.renderTransferLines()
	if len(lines) != 2 {
		t.Fatalf("got %d transfer lines, want 2: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "uploading a") || !strings.Contains(lines[1], "uploading b") {
		t.Fatalf("lines = %v, want a before b (start order preserved)", lines)
	}
	if !strings.Contains(lines[0], "50%") {
		t.Fatalf("lines[0] = %q, want the updated 50%% progress, not the stale first value", lines[0])
	}

	m.clearTransfer("a")
	lines = m.renderTransferLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "uploading b") {
		t.Fatalf("after clearing a, lines = %v, want only b", lines)
	}
}

func TestUpdateHandlesFileTransferProgressMsg(t *testing.T) {
	m := newTestModel(nil)
	next, _ := m.Update(FileTransferProgressMsg{ID: "path1", Label: "uploading x.jpg", Sent: 3, Total: 10})
	m = next.(Model)
	lines := m.renderTransferLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "30%") {
		t.Fatalf("after FileTransferProgressMsg, lines = %v, want one line at 30%%", lines)
	}
}

// TestUpdateHandlesFileTransferDoneMsg guards the second daemon-broadcast
// fix: a client that only ever saw FileTransferProgressMsg broadcasts for a
// transfer it didn't itself start (every attached TUI client besides the one
// that made the SendFile/UploadFile RPC - see adapter.go) must still clear
// that transfer once FileTransferDoneMsg arrives, instead of leaving its
// progress bar stuck at the last percentage it ever saw (in practice: 100%,
// the last chunk any successful upload reports).
func TestUpdateHandlesFileTransferDoneMsg(t *testing.T) {
	m := newTestModel(nil)
	next, _ := m.Update(FileTransferProgressMsg{ID: "path1", Label: "uploading x.jpg", Sent: 10, Total: 10})
	m = next.(Model)
	if lines := m.renderTransferLines(); len(lines) != 1 {
		t.Fatalf("before FileTransferDoneMsg, lines = %v, want one active transfer", lines)
	}

	next, _ = m.Update(FileTransferDoneMsg{ID: "path1"})
	m = next.(Model)
	if lines := m.renderTransferLines(); len(lines) != 0 {
		t.Fatalf("after FileTransferDoneMsg, lines = %v, want none", lines)
	}
}

func TestUpdateChannelProgressReArmsListening(t *testing.T) {
	m := newTestModel(nil)
	ch := make(chan tea.Msg, 2)
	next, cmd := m.Update(transferProgressChanMsg{
		FileTransferProgressMsg: FileTransferProgressMsg{ID: "url1", Label: "downloading y.png", Sent: 1, Total: 4},
		ch:                      ch,
	})
	m = next.(Model)
	if len(m.renderTransferLines()) != 1 {
		t.Fatalf("expected one active transfer after progress msg, got %v", m.renderTransferLines())
	}
	if cmd == nil {
		t.Fatal("expected a re-armed listen Cmd, got nil")
	}
	// The re-armed Cmd should read the next value off the same channel.
	ch <- openResultMsg{target: "url1"}
	got := cmd()
	if _, ok := got.(openResultMsg); !ok {
		t.Fatalf("re-armed Cmd returned %#v, want the queued openResultMsg", got)
	}
}

func TestOpenAndSaveResultClearTransferProgress(t *testing.T) {
	m := newTestModel(nil)
	m.setTransferProgress(FileTransferProgressMsg{ID: "aesgcm://host/a.jpg", Label: "downloading a.jpg", Sent: 1, Total: 2})
	next, _ := m.Update(openResultMsg{target: "aesgcm://host/a.jpg"})
	m = next.(Model)
	if lines := m.renderTransferLines(); len(lines) != 0 {
		t.Fatalf("expected transfer cleared after openResultMsg, got %v", lines)
	}

	m.setTransferProgress(FileTransferProgressMsg{ID: "https://host/b.png", Label: "downloading b.png", Sent: 1, Total: 2})
	next, _ = m.Update(saveResultMsg{target: "https://host/b.png", path: "/tmp/b.png"})
	m = next.(Model)
	if lines := m.renderTransferLines(); len(lines) != 0 {
		t.Fatalf("expected transfer cleared after saveResultMsg, got %v", lines)
	}
}
