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
	if cols, rows := m.avatarPanelSize(freeRowsOf(m)); cols != 0 || rows != 0 {
		t.Errorf("avatarPanelSize = (%d, %d) with no avatars loaded, want (0, 0)", cols, rows)
	}
	if got := m.avatarPanelHeight(freeRowsOf(m)); got != 0 {
		t.Errorf("avatarPanelHeight = %d, want 0", got)
	}
	if got := m.renderAvatarPanel(m.sidebarContentWidth(), freeRowsOf(m)); got != "" {
		t.Errorf("renderAvatarPanel = %q, want empty", got)
	}
}

func TestAvatarPanelSquareAndCapped(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)
	SetAvatarImage("alice@localhost", solidImage(64, 64, color.RGBA{40, 120, 200, 255}, 0))

	m := avatarPanelModel(t, 200, 60)
	cols, rows := m.avatarPanelSize(freeRowsOf(m))
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
	SetAvatarImage("alice@localhost", solidImage(64, 64, color.RGBA{40, 120, 200, 255}, 0))

	prev := 0
	for _, sidebar := range []int{20, 26, 32, 40, 52, 64} {
		m := avatarPanelModel(t, 200, 80)
		m.sidebarWidthOverride = sidebar
		m.updateSizes()
		cols, _ := m.avatarPanelSize(freeRowsOf(m))
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
	SetAvatarImage("alice@localhost", solidImage(64, 64, color.RGBA{40, 120, 200, 255}, 0))

	m := avatarPanelModel(t, 200, 40)
	m.sidebarWidthOverride = 120
	m.updateSizes()

	cols, rows := m.avatarPanelSize(freeRowsOf(m))
	if want := m.height * avatarPanelMaxHeightPct / 100; rows != want {
		t.Errorf("rows = %d at a very wide sidebar, want the %d%% share of %d rows = %d",
			rows, avatarPanelMaxHeightPct, m.height, want)
	}
	if rows != cols/2 {
		t.Errorf("rows = %d, want cols/2 = %d", rows, cols/2)
	}
	if got, want := m.chats.Height(), m.height-sidebarStatusHeight; got != want {
		t.Errorf("chat list has %d rows, want the full %d — the panel takes none", got, want)
	}
}

// The panel's height is what the chat list's own height is computed
// against, so the rendered block must be exactly that many rows.
func TestAvatarPanelHeightMatchesRender(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)
	SetAvatarImage("alice@localhost", solidImage(64, 64, color.RGBA{40, 120, 200, 255}, 0))

	for _, size := range [][2]int{{100, 40}, {120, 24}, {200, 60}, {90, 30}} {
		// With chats, so the panel has a contact to draw — and alice, the
		// selected one, is the contact whose avatar was just set.
		m := avatarPanelModelWithChats(t, size[0], size[1])
		want := m.avatarPanelHeight(freeRowsOf(m))
		panel := m.renderAvatarPanel(m.sidebarContentWidth(), freeRowsOf(m))
		if want == 0 {
			if panel != "" {
				t.Errorf("%dx%d: height 0 but panel rendered %q", size[0], size[1], panel)
			}
			continue
		}
		if panel == "" {
			t.Errorf("%dx%d: %d rows reserved but nothing drawn for a contact with an avatar", size[0], size[1], want)
			continue
		}
		if got := len(strings.Split(panel, "\n")); got != want {
			t.Errorf("%dx%d: rendered %d rows, avatarPanelHeight said %d", size[0], size[1], got, want)
		}
	}
}

// The panel draws into rows the list isn't using, so the list's own height
// never changes — nothing shifts under the cursor, and a contact without an
// avatar costs no chat rows.
func TestAvatarPanelTakesNoListRows(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)

	m := avatarPanelModelWithChats(t, 120, 40)
	without := m.chats.Height()

	SetAvatarImage("alice@localhost", solidImage(64, 64, color.RGBA{40, 120, 200, 255}, 0))
	m.updateSizes()

	if m.avatarPanelHeight(freeRowsOf(m)) == 0 {
		t.Fatal("no panel at 120x40")
	}
	if got := m.chats.Height(); got != without {
		t.Errorf("chat list went from %d rows to %d once an avatar existed", without, got)
	}
}

// And it never claims more rows than the list has left over.
func TestAvatarPanelFitsInFreeRows(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)
	SetAvatarImage("alice@localhost", solidImage(64, 64, color.RGBA{40, 120, 200, 255}, 0))

	for _, size := range [][2]int{{120, 40}, {100, 30}, {200, 60}, {80, 24}} {
		m := avatarPanelModelWithChats(t, size[0], size[1])
		free := freeRowsOf(m)
		if got := m.avatarPanelHeight(free); got > free {
			t.Errorf("%dx%d: picture takes %d rows, only %d are free", size[0], size[1], got, free)
		}
	}
}

// The list keeps its floor on a short terminal: the panel shrinks, then
// disappears, rather than squeezing the chat list to nothing.
func TestAvatarPanelYieldsToShortTerminals(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)
	SetAvatarImage("alice@localhost", solidImage(64, 64, color.RGBA{40, 120, 200, 255}, 0))

	prev := 0
	for _, height := range []int{60, 40, 30, 24, 18, 12} {
		m := avatarPanelModel(t, 120, height)
		h := m.avatarPanelHeight(freeRowsOf(m))
		if prev != 0 && h > prev {
			t.Errorf("height %d: panel grew to %d rows from %d on a shorter terminal", height, h, prev)
		}
		prev = h
		if h == 0 {
			continue
		}
		if got, want := m.chats.Height(), m.height-sidebarStatusHeight; got != want {
			t.Errorf("height %d: chat list has %d rows, want the full %d", height, got, want)
		}
	}
	if m := avatarPanelModel(t, 120, 12); m.avatarPanelHeight(freeRowsOf(m)) != 0 {
		t.Error("panel still drawn on a 12-row terminal")
	}
}

func TestAvatarPanelSuppressedInNarrowSidebar(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)
	SetAvatarImage("alice@localhost", solidImage(64, 64, color.RGBA{40, 120, 200, 255}, 0))

	m := avatarPanelModel(t, 120, 40)
	m.sidebarHidden = true
	m.updateSizes()
	if got := m.avatarPanelHeight(freeRowsOf(m)); got != 0 {
		t.Errorf("avatarPanelHeight = %d with the sidebar hidden, want 0", got)
	}
}

func TestAvatarPanelLinesFitSidebarWidth(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)
	SetAvatarImage("alice@localhost", solidImage(64, 64, color.RGBA{40, 120, 200, 255}, 0))

	m := avatarPanelModel(t, 120, 40)
	width := m.sidebarContentWidth()
	for i, line := range strings.Split(m.renderAvatarPanel(width, freeRowsOf(m)), "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Errorf("panel line %d is %d columns wide, sidebar content is %d", i, got, width)
		}
	}
}

// A contact with no avatar gets no panel at all. The rows stay reserved
// (the height can't depend on the selection), but nothing is drawn into
// them — a big colored block with a letter in it doesn't read as anybody.
func TestAvatarPanelBlankWithoutAPicture(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)
	// Somebody has an avatar, so the rows are reserved...
	SetAvatarImage("someone-else@localhost", solidImage(64, 64, color.RGBA{40, 120, 200, 255}, 0))

	m := avatarPanelModelWithChats(t, 100, 30)
	if m.avatarPanelHeight(freeRowsOf(m)) == 0 {
		t.Fatal("no rows reserved even though an avatar is known")
	}
	// ...but the selected contact is not that somebody.
	if got := m.renderAvatarPanel(m.sidebarContentWidth(), freeRowsOf(m)); got != "" {
		t.Errorf("renderAvatarPanel = %q for a contact with no picture, want nothing drawn", got)
	}
	if _, _, _, ok := m.avatarPanelOverlay(m.chats.View()); ok {
		t.Error("a panel was composited for a contact with no picture")
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
// the sidebar's content, so its position is arithmetic rather than layout.
// It sits at the bottom of the list's unused rows, so it stays put as the
// list grows down toward it.
func TestAvatarPanelOverlaysInTheListsFreeRows(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)
	SetAvatarImage("alice@localhost", solidImage(64, 64, color.RGBA{40, 120, 200, 255}, 0))

	m := avatarPanelModelWithChats(t, 100, 30)
	_, _, y, ok := m.avatarPanelOverlay(m.chats.View())
	if !ok {
		t.Fatal("no overlay at 100x30")
	}
	height := m.avatarPanelHeight(freeRowsOf(m))
	if want := sidebarStatusHeight + m.chats.Height() - height; y != want {
		t.Fatalf("overlay y = %d, want %d (the bottom of the list's rows)", y, want)
	}

	lines := strings.Split(ansi.Strip(fmt.Sprint(m.View().Content)), "\n")
	if y+height > len(lines) {
		t.Fatalf("panel runs past the frame: y=%d height=%d frame=%d rows", y, height, len(lines))
	}
	// Every row it covers is picture, and none of them repeats the name —
	// the panel carries no caption.
	for i := y; i < y+height; i++ {
		if !strings.Contains(lines[i], upperHalfBlock) {
			t.Errorf("row %d has no picture: %q", i, lines[i])
		}
		if strings.Contains(lines[i], "alice") {
			t.Errorf("row %d repeats the contact's name: %q", i, lines[i])
		}
	}
	// It landed in blank space, not on top of a chat — judged on the
	// sidebar's own columns, since the rest of the row is the chat pane.
	sidebarPart := func(line string) string {
		r := []rune(line)
		return string(r[:min(len(r), m.sidebarContentWidth())])
	}
	if above := sidebarPart(lines[y-1]); strings.TrimSpace(above) != "" {
		t.Errorf("row %d of the sidebar, just above the panel, is not blank: %q", y-1, above)
	}
}

// Every panel line is padded to the sidebar's full width, so compositing it
// can't shorten a frame row and leave the chat pane ragged.
func TestAvatarPanelOverlayKeepsFrameWidth(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)

	plain := strings.Split(fmt.Sprint(avatarPanelModelWithChats(t, 100, 30).View()), "\n")

	SetAvatarImage("alice@localhost", solidImage(64, 64, color.RGBA{40, 120, 200, 255}, 0))
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
	SetAvatarImage("alice@localhost", solidImage(64, 64, color.RGBA{40, 120, 200, 255}, 0))

	m := avatarPanelModelWithChats(t, 100, 30)
	m.selectedView = viewAccounts
	if _, _, _, ok := m.avatarPanelOverlay(m.chats.View()); ok {
		t.Error("panel composited over the accounts list")
	}
}

// With avatars off the chat list gets the panel's rows back, and no row
// carries a swatch — see DisplayOptions.AvatarsDisabled.
func TestAvatarsDisabled(t *testing.T) {
	ClearAvatarImages()
	SetAvatarImage("alice@localhost", solidImage(64, 64, color.RGBA{40, 120, 200, 255}, 0))
	t.Cleanup(func() {
		ClearAvatarImages()
		setAvatarsEnabled(true)
	})

	on := avatarPanelModelWithChats(t, 100, 30)
	// Captured now: these are live methods, and everything below turns
	// avatars off underneath them.
	onListRows, onPanelRows := on.chats.Height(), on.avatarPanelHeight(freeRowsOf(on))
	if onPanelRows == 0 {
		t.Fatal("no panel with avatars enabled")
	}

	off := avatarPanelModelWithChats(t, 100, 30)
	setAvatarsEnabled(false) // after New, which applies DisplayOptions itself
	off.updateSizes()

	if got := off.avatarPanelHeight(freeRowsOf(off)); got != 0 {
		t.Errorf("avatarPanelHeight = %d with avatars off, want 0", got)
	}
	if _, _, _, ok := off.avatarPanelOverlay(off.chats.View()); ok {
		t.Error("panel composited with avatars off")
	}
	// The list's height never depended on the panel, so turning avatars
	// off changes nothing about it — only the picture goes away.
	if got := off.chats.Height(); got != onListRows {
		t.Errorf("chat list has %d rows with avatars off, want the same %d as with them on", got, onListRows)
	}
	_ = onPanelRows
	if got := renderTintedName("alice", "alice@localhost"); got != "alice" {
		t.Errorf("renderTintedName = %q with avatars off, want the name unstyled", got)
	}
	title := Chat{Name: "alice", Address: "alice@localhost", Presence: PresenceOnline}.Title()
	if !strings.HasSuffix(ansi.Strip(title), " alice") {
		t.Errorf("Title() = %q, want it to still end in the name", ansi.Strip(title))
	}
}

// In narrow mode the sidebar is the entire terminal: a square picture as
// wide as the screen is half as tall as it again, leaving the chat list a
// strip at the top of the only pane there is.
func TestAvatarPanelSuppressedInNarrowMode(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)
	SetAvatarImage("alice@localhost", solidImage(64, 64, color.RGBA{40, 120, 200, 255}, 0))

	for _, size := range [][2]int{{40, 10}, {40, 50}, {24, 20}, {59, 40}} {
		m := avatarPanelModelWithChats(t, size[0], size[1])
		m.selectedView = viewChats // the list is the visible pane
		m.updateSizes()
		if !m.narrow() {
			t.Fatalf("%dx%d is not narrow; pick a width below %d", size[0], size[1], narrowWidth)
		}
		if got := m.avatarPanelHeight(freeRowsOf(m)); got != 0 {
			t.Errorf("%dx%d: panel takes %d rows in narrow mode, want none", size[0], size[1], got)
		}
		if _, _, _, ok := m.avatarPanelOverlay(m.chats.View()); ok {
			t.Errorf("%dx%d: panel composited in narrow mode", size[0], size[1])
		}
	}
}

// A picture too small to resolve into a face is worse than nothing — the
// same judgement that removed the large monogram. It is the free rows, not
// the terminal size, that decide: a short terminal whose list is nearly
// empty still has somewhere to put one.
func TestAvatarPanelSuppressedWhenTooSmallToRead(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)
	SetAvatarImage("alice@localhost", solidImage(64, 64, color.RGBA{40, 120, 200, 255}, 0))

	for _, free := range []int{0, 1, 3, avatarPanelMinFreeRows - 1} {
		m := avatarPanelModelWithChats(t, 200, 40)
		if cols, rows := m.avatarPanelSize(free); cols != 0 || rows != 0 {
			t.Errorf("%d free rows: drew a %dx%d picture, want none", free, cols, rows)
		}
	}

	// A sidebar too narrow for a readable picture gets none however much
	// vertical space is going spare.
	m := avatarPanelModelWithChats(t, 62, 40)
	m.sidebarWidthOverride = sidebarMinWidth
	m.updateSizes()
	if cols, _ := m.avatarPanelSize(40); cols != 0 && cols < avatarPanelMinCols {
		t.Errorf("picture is %d columns, below the %d minimum", cols, avatarPanelMinCols)
	}

	// And a terminal with the room keeps it.
	wide := avatarPanelModelWithChats(t, 120, 40)
	if wide.avatarPanelHeight(freeRowsOf(wide)) == 0 {
		t.Error("120x40 has room for a picture but drew none")
	}
}

// freeRowsOf is how many rows at the bottom of the chat list are unused —
// the space the avatar panel sizes itself to.
func freeRowsOf(m Model) int {
	return trailingBlankRows(m.chats.View())
}
