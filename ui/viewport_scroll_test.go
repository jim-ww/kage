package ui

import (
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	zone "github.com/lrstanley/bubblezone/v2"
)

// newScrollTestModel builds a Model with n one-line messages and a viewport
// sized to viewportHeight rows, wide enough that each message renders on a
// single line.
func newScrollTestModel(t *testing.T, n, viewportHeight int) *Model {
	t.Helper()
	styles := newUIStyles(DefaultTheme())

	msgs := make([]Message, n)
	for i := range msgs {
		msgs[i] = Message{ID: string(rune('a' + i)), Author: "bob", Content: "hi", SentAt: time.Now()}
	}

	chat := Chat{Name: "bob", Address: "bob@example.com"}
	zm := zone.New()
	delegate := newChatListDelegate(styles.colors, zm, false, &hoverState{})
	l := list.New([]list.Item{chat}, delegate, 0, 0)
	l.Select(0)

	m := &Model{
		styles:        styles,
		width:         80,
		height:        24,
		sidebarHidden: true,
		zone:          zm,
		chats:         l,
		accounts: []Account{
			{
				Chats:    []list.Item{chat},
				Messages: map[int][]Message{0: msgs},
			},
		},
	}
	m.viewport.SetWidth(80)
	m.viewport.SetHeight(viewportHeight)
	m.refreshViewport()
	return m
}

// TestRefreshViewportScrollToLeavesMargin guards against a regression where
// scrolling the selected message into view always snapped it to the exact
// top or bottom edge of the viewport, leaving no breathing room — the next
// keystroke's cursor blink or mouse hover felt like it was jammed against
// the pane border. Scrolling toward either edge should now leave a small
// margin instead of putting the message flush against it.
func TestRefreshViewportScrollToLeavesMargin(t *testing.T) {
	const viewportHeight = 10
	m := newScrollTestModel(t, 40, viewportHeight)

	// Scroll down past the bottom edge: selection lands with margin lines
	// still below it, not flush against the last visible row.
	m.selectedMsg = 30
	m.refreshViewportScrollTo(m.selectedMsg)

	top := m.viewport.YOffset()
	bottom := top + viewportHeight - 1
	msgLine := m.msgOffsets[m.selectedMsg]
	if msgLine == bottom {
		t.Fatalf("selected message landed flush on the bottom edge (line %d == bottom %d), want margin", msgLine, bottom)
	}
	if msgLine > bottom || msgLine < top {
		t.Fatalf("selected message line %d not visible in [%d,%d]", msgLine, top, bottom)
	}

	// Now scroll back up past the top edge: should land with margin above
	// it, not flush against the first visible row.
	m.selectedMsg = 2
	m.refreshViewportScrollTo(m.selectedMsg)

	top = m.viewport.YOffset()
	bottom = top + viewportHeight - 1
	msgLine = m.msgOffsets[m.selectedMsg]
	if msgLine == top && top != 0 {
		t.Fatalf("selected message landed flush on the top edge (line %d == top %d), want margin", msgLine, top)
	}
	if msgLine > bottom || msgLine < top {
		t.Fatalf("selected message line %d not visible in [%d,%d]", msgLine, top, bottom)
	}
}

// TestRefreshViewportScrollToPinsCursorNearEdge guards against a regression
// where, once the selection neared an edge, further moves in the same
// direction sat still (no scroll) until hitting the edge outright and then
// jumped the viewport by several lines at once — visually the cursor would
// "jump back" instead of holding a steady row. Once the message is within
// scrollMargin lines of an edge, each further step in that direction must
// scroll by exactly one line, keeping the message's row on screen constant.
func TestRefreshViewportScrollToPinsCursorNearEdge(t *testing.T) {
	const viewportHeight = 12
	m := newScrollTestModel(t, 40, viewportHeight)

	// Get near the bottom edge first.
	m.selectedMsg = 20
	m.refreshViewportScrollTo(m.selectedMsg)
	row := m.msgOffsets[m.selectedMsg] - m.viewport.YOffset()

	for i := 0; i < 5; i++ {
		m.selectedMsg++
		m.refreshViewportScrollTo(m.selectedMsg)
		gotRow := m.msgOffsets[m.selectedMsg] - m.viewport.YOffset()
		if gotRow != row {
			t.Fatalf("step %d: message row within viewport changed from %d to %d, want it pinned", i, row, gotRow)
		}
	}

	// Same check moving up toward the top edge.
	m.selectedMsg = 10
	m.refreshViewportScrollTo(m.selectedMsg)
	row = m.msgOffsets[m.selectedMsg] - m.viewport.YOffset()

	for i := 0; i < 5; i++ {
		m.selectedMsg--
		m.refreshViewportScrollTo(m.selectedMsg)
		gotRow := m.msgOffsets[m.selectedMsg] - m.viewport.YOffset()
		if gotRow != row {
			t.Fatalf("step %d: message row within viewport changed from %d to %d, want it pinned", i, row, gotRow)
		}
	}
}

// TestRefreshViewportScrollToNoopWhenAlreadyVisible guards against
// unnecessary scroll jumps: if the selected message is already fully
// visible, refreshViewportScrollTo must leave the current scroll position
// alone rather than re-centering it every keystroke.
func TestRefreshViewportScrollToNoopWhenAlreadyVisible(t *testing.T) {
	const viewportHeight = 20
	m := newScrollTestModel(t, 5, viewportHeight)

	m.selectedMsg = 2
	m.refreshViewportScrollTo(m.selectedMsg)
	before := m.viewport.YOffset()

	m.selectedMsg = 3
	m.refreshViewportScrollTo(m.selectedMsg)
	after := m.viewport.YOffset()

	if before != after {
		t.Fatalf("YOffset changed from %d to %d even though message was already visible", before, after)
	}
}
