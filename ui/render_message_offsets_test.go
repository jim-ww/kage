package ui

import (
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	zone "github.com/lrstanley/bubblezone/v2"
)

// TestRenderMessagesWithOffsetsMatchesLineSplit guards against a regression
// where renderMessagesWithOffsets double-counted the newline separator
// between messages, inflating each offset by its index and causing
// m.msgOffsets to drift away from the real line positions in
// m.viewportLines (built via strings.Split on the same content). That drift
// made refreshViewportScrollTo request an out-of-range viewport Y-offset,
// which bubbles' viewport clamps to maxYOffset — so keyboard message
// navigation stopped scrolling the pane once the drift exceeded the visible
// window, leaving the selected message off-screen.
func TestRenderMessagesWithOffsetsMatchesLineSplit(t *testing.T) {
	styles := newUIStyles(DefaultTheme())

	msgs := []Message{
		{ID: "1", Author: "bob", Content: "hello", SentAt: time.Now()},
		{ID: "2", Author: "alice", Content: "hi there", SentAt: time.Now()},
		{ID: "3", Author: "bob", Content: "how are you", SentAt: time.Now()},
		{ID: "4", Author: "alice", Content: "doing well, thanks", SentAt: time.Now()},
		{ID: "5", Author: "bob", Content: "great to hear", SentAt: time.Now()},
	}

	chat := Chat{Name: "bob", Address: "bob@example.com"}
	zm := zone.New()
	delegate := newChatListDelegate(styles.colors, zm, false, &hoverState{})
	l := list.New([]list.Item{chat}, delegate, 0, 0)
	l.Select(0)

	m := Model{
		styles:        styles,
		width:         80,
		height:        24,
		sidebarHidden: true,
		zone:          zm,
		chats:         &l,
		accounts: []Account{
			{
				Chats:    []list.Item{chat},
				Messages: map[int][]Message{0: msgs},
			},
		},
	}

	content, offsets := m.renderMessagesWithOffsets()
	lines := strings.Split(content, "\n")

	if len(offsets) != len(msgs) {
		t.Fatalf("got %d offsets, want %d", len(offsets), len(msgs))
	}

	// Each offset must point at a line that actually starts the
	// corresponding message's rendered content within the split lines,
	// not drift past it as the message index grows.
	for i, off := range offsets {
		if off < 0 || off >= len(lines) {
			t.Fatalf("offset[%d]=%d out of range (len(lines)=%d)", i, off, len(lines))
		}
		if !strings.Contains(lines[off], msgs[i].Content) {
			t.Fatalf("offset[%d]=%d does not point at message %d's content line, got %q", i, off, i, lines[off])
		}
	}
}
