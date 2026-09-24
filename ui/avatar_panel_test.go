package ui

import (
	"fmt"
	"image/color"
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func avatarPanelModel(t *testing.T, width, height int) Model {
	t.Helper()
	m := newTestModel(nil)
	m.width, m.height = width, height
	m.termHeight = height
	m.updateSizes()
	return m
}

func TestAvatarPanelSuppressedWithoutAvatars(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)

	m := avatarPanelModel(t, 100, 40)
	if cols, rows := m.avatarPanelSize(); cols != 0 || rows != 0 {
		t.Errorf("avatarPanelSize = (%d, %d) with no avatars loaded, want (0, 0)", cols, rows)
	}
	if got := m.avatarPanelHeight(); got != 0 {
		t.Errorf("avatarPanelHeight = %d, want 0", got)
	}
	if got := m.renderAvatarPanel(m.sidebarContentWidth()); got != "" {
		t.Errorf("renderAvatarPanel = %q, want empty", got)
	}
}

func TestAvatarPanelSquareAndCapped(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)
	SetFallbackAvatarImage(solidImage(64, 64, color.RGBA{40, 120, 200, 255}, 0))

	m := avatarPanelModel(t, 200, 60)
	cols, rows := m.avatarPanelSize()
	if rows != cols/2 {
		t.Errorf("rows = %d, want cols/2 = %d — a half-block cell is two pixels tall", rows, cols/2)
	}
	if cols%2 != 0 {
		t.Errorf("cols = %d, want even so it splits into whole pixel rows", cols)
	}
	if cols > m.sidebarContentWidth() {
		t.Errorf("cols = %d, wider than the sidebar's %d columns", cols, m.sidebarContentWidth())
	}
	if limit := m.height * avatarPanelMaxHeightPct / 100; rows > limit {
		t.Errorf("rows = %d, want no more than %d%% of the sidebar's %d rows", rows, avatarPanelMaxHeightPct, m.height)
	}
}

// Dragging the sidebar wider must actually buy a bigger picture — a cap
// that ignores the new width makes the drag look broken.
func TestAvatarPanelGrowsWithSidebarWidth(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)
	SetFallbackAvatarImage(solidImage(64, 64, color.RGBA{40, 120, 200, 255}, 0))

	prev := 0
	for _, sidebar := range []int{20, 26, 32, 40, 52, 64} {
		m := avatarPanelModel(t, 200, 80)
		m.sidebarWidthOverride = sidebar
		m.updateSizes()
		cols, _ := m.avatarPanelSize()
		if prev != 0 && cols <= prev {
			t.Errorf("sidebar %d: picture stuck at %d columns despite the wider sidebar (was %d)", sidebar, cols, prev)
		}
		prev = cols
	}
}

// However wide the sidebar is dragged, the picture stops at its share of
// the sidebar and the chat list keeps the rest.
func TestAvatarPanelStopsAtHeightShare(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)
	SetFallbackAvatarImage(solidImage(64, 64, color.RGBA{40, 120, 200, 255}, 0))

	m := avatarPanelModel(t, 200, 40)
	m.sidebarWidthOverride = 120
	m.updateSizes()

	cols, rows := m.avatarPanelSize()
	if want := m.height * avatarPanelMaxHeightPct / 100; rows != want {
		t.Errorf("rows = %d at a very wide sidebar, want the %d%% share of %d rows = %d",
			rows, avatarPanelMaxHeightPct, m.height, want)
	}
	if rows != cols/2 {
		t.Errorf("rows = %d, want cols/2 = %d", rows, cols/2)
	}
	if got := m.chats.Height(); got <= avatarPanelMinListRows {
		t.Errorf("chat list has %d rows, want well above the floor of %d once the share caps the picture", got, avatarPanelMinListRows)
	}
}

// The panel's height is what the chat list's own height is computed
// against, so the rendered block must be exactly that many rows.
func TestAvatarPanelHeightMatchesRender(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)
	SetFallbackAvatarImage(solidImage(64, 64, color.RGBA{40, 120, 200, 255}, 0))

	for _, size := range [][2]int{{100, 40}, {120, 24}, {200, 60}, {90, 30}} {
		m := avatarPanelModel(t, size[0], size[1])
		want := m.avatarPanelHeight()
		panel := m.renderAvatarPanel(m.sidebarContentWidth())
		if want == 0 {
			if panel != "" {
				t.Errorf("%dx%d: height 0 but panel rendered %q", size[0], size[1], panel)
			}
			continue
		}
		if got := len(strings.Split(panel, "\n")); got != want {
			t.Errorf("%dx%d: rendered %d rows, avatarPanelHeight said %d", size[0], size[1], got, want)
		}
	}
}

// Reserving panel rows must come out of the chat list, or the sidebar
// overflows its box and every line wraps.
func TestAvatarPanelReservedInListHeight(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)

	m := avatarPanelModel(t, 120, 40)
	without := m.chats.Height()

	SetFallbackAvatarImage(solidImage(64, 64, color.RGBA{40, 120, 200, 255}, 0))
	m.updateSizes()
	with := m.chats.Height()

	if panel := m.avatarPanelHeight(); panel == 0 {
		t.Fatal("no panel at 120x40")
	} else if without-with != panel {
		t.Errorf("chat list lost %d rows, panel takes %d", without-with, panel)
	}
}

// The list keeps its floor on a short terminal: the panel shrinks, then
// disappears, rather than squeezing the chat list to nothing.
func TestAvatarPanelYieldsToShortTerminals(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)
	SetFallbackAvatarImage(solidImage(64, 64, color.RGBA{40, 120, 200, 255}, 0))

	prev := 0
	for _, height := range []int{60, 40, 30, 24, 18, 12} {
		m := avatarPanelModel(t, 120, height)
		h := m.avatarPanelHeight()
		if prev != 0 && h > prev {
			t.Errorf("height %d: panel grew to %d rows from %d on a shorter terminal", height, h, prev)
		}
		prev = h
		if h == 0 {
			continue
		}
		if listRows := m.chats.Height(); listRows < avatarPanelMinListRows {
			t.Errorf("height %d: chat list squeezed to %d rows, floor is %d", height, listRows, avatarPanelMinListRows)
		}
	}
	if m := avatarPanelModel(t, 120, 12); m.avatarPanelHeight() != 0 {
		t.Error("panel still drawn on a 12-row terminal")
	}
}

func TestAvatarPanelSuppressedInNarrowSidebar(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)
	SetFallbackAvatarImage(solidImage(64, 64, color.RGBA{40, 120, 200, 255}, 0))

	m := avatarPanelModel(t, 120, 40)
	m.sidebarHidden = true
	m.updateSizes()
	if got := m.avatarPanelHeight(); got != 0 {
		t.Errorf("avatarPanelHeight = %d with the sidebar hidden, want 0", got)
	}
}

func TestAvatarPanelLinesFitSidebarWidth(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)
	SetFallbackAvatarImage(solidImage(64, 64, color.RGBA{40, 120, 200, 255}, 0))

	m := avatarPanelModel(t, 120, 40)
	width := m.sidebarContentWidth()
	for i, line := range strings.Split(m.renderAvatarPanel(width), "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Errorf("panel line %d is %d columns wide, sidebar content is %d", i, got, width)
		}
	}
}

// renderAvatarLarge must fill exactly the box it was given even for a
// contact with no picture, or the reserved rows and the drawn rows drift.
func TestRenderAvatarLargeMonogramFallback(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)

	block := renderAvatarLarge("alice", "alice@localhost", 10, 5)
	lines := strings.Split(block, "\n")
	if len(lines) != 5 {
		t.Fatalf("got %d rows, want 5", len(lines))
	}
	for i, line := range lines {
		if got := lipgloss.Width(line); got != 10 {
			t.Errorf("row %d width = %d, want 10", i, got)
		}
	}
	if !strings.Contains(block, "A") {
		t.Error("monogram fallback lost the initial")
	}
}

// avatarPanelModelWithChats is avatarPanelModel with a populated chat list,
// so View() renders a real sidebar to composite the panel onto.
func avatarPanelModelWithChats(t *testing.T, width, height int) Model {
	t.Helper()
	m := newTestModel(nil)
	items := []list.Item{
		Chat{Name: "alice", Address: "alice@localhost", Presence: PresenceOnline, LastMessage: "hey"},
		Chat{Name: "bob", Address: "bob@localhost", Presence: PresenceAway, LastMessage: "patch"},
	}
	m.accounts = []Account{{Name: "me", Chats: items}}
	m.chats.SetItems(items)
	m.width, m.height, m.termHeight = width, height, height
	m.updateSizes()
	return m
}

// The panel is composited onto the finished frame rather than joined into
// the sidebar's content, so its position is arithmetic rather than layout —
// it has to land on exactly the rows reserved for it, directly below the
// chat list.
func TestAvatarPanelOverlaysBelowTheList(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)
	SetFallbackAvatarImage(solidImage(64, 64, color.RGBA{40, 120, 200, 255}, 0))

	m := avatarPanelModelWithChats(t, 100, 30)
	_, _, y, ok := m.avatarPanelOverlay()
	if !ok {
		t.Fatal("no overlay at 100x30")
	}
	if want := sidebarStatusHeight + m.chats.Height(); y != want {
		t.Fatalf("overlay y = %d, want %d (account bar + chat list rows)", y, want)
	}

	lines := strings.Split(ansi.Strip(fmt.Sprint(m.View())), "\n")
	height := m.avatarPanelHeight()
	if y+height > len(lines) {
		t.Fatalf("panel runs past the frame: y=%d height=%d frame=%d rows", y, height, len(lines))
	}
	for i := y; i < y+height-avatarPanelTextRows; i++ {
		if !strings.Contains(lines[i], upperHalfBlock) {
			t.Errorf("row %d has no picture: %q", i, lines[i])
		}
	}
	if above := lines[y-1]; strings.Contains(above, upperHalfBlock) {
		t.Errorf("row %d, above the panel, has picture in it: %q", y-1, above)
	}
	if !strings.Contains(lines[y+height-avatarPanelTextRows], "alice") {
		t.Errorf("caption row missing the name: %q", lines[y+height-avatarPanelTextRows])
	}
}

// Every panel line is padded to the sidebar's full width, so compositing it
// can't shorten a frame row and leave the chat pane ragged.
func TestAvatarPanelOverlayKeepsFrameWidth(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)

	plain := strings.Split(fmt.Sprint(avatarPanelModelWithChats(t, 100, 30).View()), "\n")

	SetFallbackAvatarImage(solidImage(64, 64, color.RGBA{40, 120, 200, 255}, 0))
	withPanel := strings.Split(fmt.Sprint(avatarPanelModelWithChats(t, 100, 30).View()), "\n")

	if len(plain) != len(withPanel) {
		t.Fatalf("frame is %d rows with the panel, %d without", len(withPanel), len(plain))
	}
	for i := range plain {
		// Columns, not bytes: a half-block is three bytes and one column.
		if got, want := ansi.StringWidth(withPanel[i]), ansi.StringWidth(plain[i]); got != want {
			t.Errorf("row %d is %d columns with the panel, %d without", i, got, want)
		}
	}
}

// The accounts panel replaces the chat list with its own content, which the
// panel's reserved rows say nothing about.
func TestAvatarPanelNotOverlaidOverAccounts(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)
	SetFallbackAvatarImage(solidImage(64, 64, color.RGBA{40, 120, 200, 255}, 0))

	m := avatarPanelModelWithChats(t, 100, 30)
	m.selectedView = viewAccounts
	if _, _, _, ok := m.avatarPanelOverlay(); ok {
		t.Error("panel composited over the accounts list")
	}
}
