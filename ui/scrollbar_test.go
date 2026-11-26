package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// withUnloaded sets the open chat's counts of unloaded history.
func withUnloaded(m Model, before, after int) Model {
	chat := m.accounts[0].Chats[0].(Chat)
	chat.HistoryBefore, chat.HistoryAfter = before, after
	m.accounts[0].Chats[0] = chat
	return m
}

func TestScrollbarHiddenWhenAllFits(t *testing.T) {
	m := newTestModelWithManyMessages(2)
	if _, _, ok := m.scrollbarThumb(m.viewport.Height()); ok {
		t.Error("thumb shown for a chat that fits on screen")
	}
}

func TestScrollbarShownForUnloadedHistory(t *testing.T) {
	m := withUnloaded(newTestModelWithManyMessages(2), 100, 0)
	h := m.viewport.Height()
	start, end, ok := m.scrollbarThumb(h)
	if !ok {
		t.Fatal("no thumb with older history unloaded")
	}
	if end != h || start == 0 {
		t.Errorf("thumb [%d,%d) of %d, want it at the bottom only", start, end, h)
	}
}

func TestScrollbarFollowsScroll(t *testing.T) {
	m := newTestModelWithManyMessages(50)
	h := m.viewport.Height()
	start, end, _ := m.scrollbarThumb(h)
	if end != h {
		t.Errorf("at the bottom: thumb [%d,%d), want it to end at %d", start, end, h)
	}
	m.viewport.GotoTop()
	if start, end, _ = m.scrollbarThumb(h); start != 0 || end == h {
		t.Errorf("at the top of all history: thumb [%d,%d), want it to start at 0", start, end)
	}

	// With older history unloaded, the top of the loaded part isn't the top.
	m = withUnloaded(m, 500, 0)
	if start, _, _ = m.scrollbarThumb(h); start == 0 {
		t.Error("thumb at the top with older history unloaded")
	}
	// Nor is the bottom the bottom with newer history unloaded.
	m = withUnloaded(m, 0, 500)
	m.viewport.GotoBottom()
	if _, end, _ = m.scrollbarThumb(h); end == h {
		t.Error("thumb at the bottom with newer history unloaded")
	}
}

func TestHistoryWindowSetsUnloadedCounts(t *testing.T) {
	m := newTestModelWithManyMessages(5)
	msgs := m.accounts[0].Messages[0]
	m = updated(t, m, HistoryWindowMsg{From: "bob@example.test", Messages: msgs, HasOlder: true, HasNewer: true, Older: 30, Total: 50})
	chat := m.accounts[0].Chats[0].(Chat)
	if chat.HistoryBefore != 30 || chat.HistoryAfter != 15 {
		t.Errorf("before/after = %d/%d, want 30/15", chat.HistoryBefore, chat.HistoryAfter)
	}
}

func TestTrimCountsDroppedAsUnloaded(t *testing.T) {
	m := newTestModelWithManyMessages(5)
	m.maxMessagesPerChat = 3
	m.appendAndTrim(0, 0, Message{Content: "new"})
	if got := m.accounts[0].Chats[0].(Chat).HistoryBefore; got != 3 {
		t.Errorf("HistoryBefore = %d after trimming 3, want 3", got)
	}
}

// scrollbarModel is a chat of n two-line messages, scrolled to the bottom
// and rendered so the scrollbar's zone is known, with loader as daemon.
func scrollbarModel(t *testing.T, n int, loader Daemon) (Model, int, int, int) {
	t.Helper()
	msgs := make([]Message, n)
	for i := range msgs {
		msgs[i] = Message{ID: fmt.Sprint(i), StoreID: int64(i + 1), Content: "message body"}
	}
	m := newTestModelWithMessages(msgs, loader)
	m.width, m.termHeight = 80, 20
	m.updateSizes()
	m.refreshViewport()
	m.viewport.GotoBottom()
	_ = m.View()
	x, y, h, ok := m.scrollbarTrack()
	if !ok {
		t.Fatal("test setup: no scrollbar")
	}
	return m, x, y, h
}

func TestScrollbarDragScrollsLoadedHistory(t *testing.T) {
	m, x, y, h := scrollbarModel(t, 50, stubDaemon{})
	m = updated(t, m, tea.MouseClickMsg{X: x, Y: y + h - 1, Button: tea.MouseLeft})
	if m.scrollDrag == nil {
		t.Fatal("click on the scrollbar didn't grab it")
	}
	m = updated(t, m, tea.MouseMotionMsg{X: x, Y: y, Button: tea.MouseLeft})
	if m.viewport.YOffset() != 0 {
		t.Errorf("dragged to the top: offset %d, want 0", m.viewport.YOffset())
	}
	m = updated(t, m, tea.MouseMotionMsg{X: x, Y: y + h - 1, Button: tea.MouseLeft})
	if !m.viewport.AtBottom() {
		t.Error("dragged back to the bottom: not at the bottom")
	}
	m = updated(t, m, tea.MouseReleaseMsg{X: x, Y: y + h - 1, Button: tea.MouseLeft})
	if m.scrollDrag != nil {
		t.Error("drag still on after release")
	}
}

func TestScrollbarGrabbedThumbDoesNotJump(t *testing.T) {
	m, x, y, h := scrollbarModel(t, 50, stubDaemon{})
	before := m.viewport.YOffset()
	start, _, _ := m.scrollbarThumb(h)
	m = updated(t, m, tea.MouseClickMsg{X: x, Y: y + start, Button: tea.MouseLeft})
	if m.viewport.YOffset() != before {
		t.Errorf("grabbing the thumb scrolled from %d to %d", before, m.viewport.YOffset())
	}
}

func TestScrollbarDragIntoUnloadedHistoryLoadsIt(t *testing.T) {
	loader := &anchorLoader{}
	m, x, y, h := scrollbarModel(t, 50, loader)
	m = withUnloaded(m, 1000, 0)
	_ = m.View()

	m = updated(t, m, tea.MouseClickMsg{X: x, Y: y + h - 1, Button: tea.MouseLeft})
	m = updated(t, m, tea.MouseMotionMsg{X: x, Y: y, Button: tea.MouseLeft})
	if len(loader.anchors) != 0 {
		t.Fatal("history loaded mid-drag")
	}
	// Drawn where dragged, though nothing moved yet.
	if !strings.HasPrefix(ansi.Strip(m.renderScrollbar(h)), "┃") {
		t.Error("thumb not drawn where it's dragged")
	}
	m = updated(t, m, tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	if len(loader.anchors) != 1 || loader.anchors[0] == nil || loader.anchors[0].StoreID != 0 || loader.anchors[0].Index != 0 {
		t.Fatalf("loads %v, want one around the first message", loader.anchors)
	}

	window := []Message{{ID: "old0", StoreID: 900, Content: "a"}, {ID: "old1", StoreID: 901, Content: "b"}}
	for i := range 30 {
		window = append(window, Message{ID: fmt.Sprint("w", i), StoreID: int64(902 + i), Content: "c"})
	}
	m = updated(t, m, HistoryWindowMsg{From: "bob@example.test", Messages: window, HasNewer: true, Older: 0, Total: 1050, AnchorStoreID: 900})
	if m.viewport.YOffset() != 0 || m.selectedMsg != 0 {
		t.Errorf("landed at offset %d on message %d, want the anchor at the top", m.viewport.YOffset(), m.selectedMsg)
	}
}

func TestScrollbarClickNotAMessage(t *testing.T) {
	m, x, y, h := scrollbarModel(t, 50, stubDaemon{})
	if _, on := m.messageIndexAtMouse(tea.MouseClickMsg{X: x, Y: y + h/2}); on {
		t.Error("the scrollbar column hit a message")
	}
}

func TestScrollbarDisabled(t *testing.T) {
	m := newTestModelWithManyMessages(50)
	full := m.messageAreaWidth()
	m.scrollbarDisabled = true
	m.updateSizes()
	m.refreshViewport()
	_ = m.View()
	if m.messageAreaWidth() != full+1 {
		t.Errorf("message width %d with the scrollbar off, want its column back (%d)", m.messageAreaWidth(), full+1)
	}
	if _, _, ok := m.scrollbarThumb(m.viewport.Height()); ok {
		t.Error("thumb shown with the scrollbar off")
	}
	if _, _, _, ok := m.scrollbarTrack(); ok {
		t.Error("scrollbar grabbable with it off")
	}
	if got := ansi.StringWidth(strings.Split(m.renderChatArea(m.styles.colors), "\n")[1]); got != m.chatAreaWidth() {
		t.Errorf("chat area row %d wide, want %d", got, m.chatAreaWidth())
	}
}
