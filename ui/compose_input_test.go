package ui

import (
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	zone "github.com/lrstanley/bubblezone/v2"
)

func shiftEnterKey() tea.KeyMsg {
	return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}
}

func altEnterKey() tea.KeyMsg {
	return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}
}

func ctrlLeftKey() tea.KeyMsg  { return tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModCtrl} }
func ctrlRightKey() tea.KeyMsg { return tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModCtrl} }

// TestComposeCtrlLeftRightJumpsWords checks ctrl+left/right move the compose
// cursor a word at a time, in addition to the textarea's default alt+left/
// right (see defaultInputAreaKeys in keybinds.go).
func TestComposeCtrlLeftRightJumpsWords(t *testing.T) {
	m := newTestModel(&fakeAccountAdder{})
	m.selectedView = viewChat
	m.accounts = []Account{{Name: "me", Chats: nil, Messages: map[int][]Message{}}}

	next, _ := m.Update(keyText("foo bar"))
	m = next.(Model)
	if got := m.input.Column(); got != len("foo bar") {
		t.Fatalf("cursor column after typing = %d, want %d", got, len("foo bar"))
	}

	next, _ = m.Update(ctrlLeftKey())
	m = next.(Model)
	if got, want := m.input.Column(), len("foo "); got != want {
		t.Fatalf("cursor column after ctrl+left = %d, want %d (start of \"bar\")", got, want)
	}

	next, _ = m.Update(ctrlLeftKey())
	m = next.(Model)
	if got := m.input.Column(); got != 0 {
		t.Fatalf("cursor column after second ctrl+left = %d, want 0 (start of \"foo\")", got)
	}

	next, _ = m.Update(ctrlRightKey())
	m = next.(Model)
	if got, want := m.input.Column(), len("foo"); got != want {
		t.Fatalf("cursor column after ctrl+right = %d, want %d (end of \"foo\")", got, want)
	}
}

// TestComposeShiftEnterInsertsNewline checks that shift+enter breaks the
// compose box to a new line instead of sending, while plain enter still
// sends — the two must not race for the same keypress (see
// defaultInputAreaKeys in keybinds.go). shift+enter only arrives as its own
// key on terminals with Kitty keyboard protocol support; alt+enter is kept
// as a fallback that works everywhere else.
func TestComposeShiftEnterInsertsNewline(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tea.KeyMsg
	}{
		{"shift+enter", shiftEnterKey()},
		{"alt+enter", altEnterKey()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(&fakeAccountAdder{})
			m.selectedView = viewChat
			m.accounts = []Account{{Name: "me", Chats: nil, Messages: map[int][]Message{}}}

			next, _ := m.Update(keyText("a"))
			m = next.(Model)
			next, _ = m.Update(tc.key)
			m = next.(Model)
			next, _ = m.Update(keyText("b"))
			m = next.(Model)

			if got, want := m.input.Value(), "a\nb"; got != want {
				t.Fatalf("input value after %s = %q, want %q", tc.name, got, want)
			}
			if lines := m.input.LineCount(); lines != 2 {
				t.Fatalf("input LineCount() = %d, want 2", lines)
			}
			if !strings.Contains(m.input.View(), "\n") {
				t.Fatalf("input View() should render on multiple lines, got %q", m.input.View())
			}
		})
	}
}

// TestComposeManyNewlinesNotBlocked checks that alt+enter keeps inserting
// newlines well past inputMaxHeight logical lines. The compose box's
// MaxContentHeight must be set (see composeMaxContentHeight in layout.go):
// left at 0, textarea.Model falls back to blocking InsertNewline once the
// logical line count reaches MaxHeight (inputMaxHeight, the *visible*
// viewport cap) — alt+enter would silently stop working after a handful of
// manual newlines even though a single long line keeps soft-wrapping and
// scrolling forever.
func TestComposeManyNewlinesNotBlocked(t *testing.T) {
	m := newTestModel(&fakeAccountAdder{})
	m.selectedView = viewChat
	m.accounts = []Account{{Name: "me", Chats: nil, Messages: map[int][]Message{}}}

	const lines = inputMaxHeight * 3
	for i := 0; i < lines; i++ {
		next, _ := m.Update(altEnterKey())
		m = next.(Model)
	}
	next, _ := m.Update(keyText("x"))
	m = next.(Model)

	if got, want := m.input.LineCount(), lines+1; got != want {
		t.Fatalf("input LineCount() after %d alt+enter = %d, want %d (newlines must not be blocked past inputMaxHeight)", lines, got, want)
	}
}

// TestComposeDownNotStuckOnExactWidthWrap checks that the down arrow can
// still reach the second line when the first line is a single long
// space-free run (a pasted token/URL/keysmash, common — real prose almost
// always has a space near the wrap boundary) that happens to wrap so its
// first row exactly fills the field width with no natural trailing space.
// That combination trips a bubbles v2.1.0 textarea.Model bug
// (setCursorLineRelative's `len(line)-1` boundary clamp): CursorDown
// recomputes from the same (line, column) every time and can never advance,
// no matter how many times it's pressed — see fixStuckComposeCursorDown in
// layout.go for the full mechanism and the workaround.
func TestComposeDownNotStuckOnExactWidthWrap(t *testing.T) {
	m := newTestModel(&fakeAccountAdder{})
	m.selectedView = viewChat
	m.accounts = []Account{{Name: "me", Chats: nil, Messages: map[int][]Message{}}}
	m.termHeight = 40
	m.updateSizes()

	next, _ := m.Update(keyText("ijadaspodksaodpaskdposakdpokdpoaskdpos"))
	m = next.(Model)
	next, _ = m.Update(altEnterKey())
	m = next.(Model)
	next, _ = m.Update(keyText("adkpasodksa"))
	m = next.(Model)

	m.input.MoveToBegin()

	for i := 0; i < 3; i++ {
		next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		m = next.(Model)
		if m.input.Line() == 1 {
			return
		}
	}
	t.Fatalf("down arrow never reached the second line, stuck at line=%d col=%d", m.input.Line(), m.input.Column())
}

// TestComposeUpDownCursorVsMessageNav checks that plain up/down move the
// compose textarea's cursor while it holds more than one line, but fall
// back to their usual job of moving the selected-message highlight once the
// input is back to a single line (or empty).
func TestComposeUpDownCursorVsMessageNav(t *testing.T) {
	chat := Chat{Address: "bob@localhost"}
	m := newTestModel(&fakeAccountAdder{})
	m.selectedView = viewChat
	m.accounts = []Account{{Name: "me", Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.chats.Select(0)
	m.setCurrentMessages([]Message{{Author: "bob", Content: "one"}, {Author: "bob", Content: "two"}})
	m.selectedMsg = 1

	// Single line: up/down navigate messages, untouched by the input.
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = next.(Model)
	if m.selectedMsg != 0 {
		t.Fatalf("selectedMsg after up (single-line input) = %d, want 0", m.selectedMsg)
	}
	if m.input.Value() != "" {
		t.Fatalf("input should be untouched by message-nav up, got %q", m.input.Value())
	}

	// Multiple lines: up/down move the textarea cursor, not the message
	// selection.
	next, _ = m.Update(keyText("a"))
	m = next.(Model)
	next, _ = m.Update(shiftEnterKey())
	m = next.(Model)
	next, _ = m.Update(keyText("b"))
	m = next.(Model)
	if !m.composeMultiline() {
		t.Fatal("expected composeMultiline() true after inserting a newline")
	}

	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = next.(Model)
	if m.selectedMsg != 0 {
		t.Fatalf("selectedMsg changed by up while composing multiline, got %d", m.selectedMsg)
	}
	if m.input.Line() != 0 {
		t.Fatalf("input cursor line after up = %d, want 0 (moved onto first line)", m.input.Line())
	}
}

// TestComposeUpDownSoftWrapCountsAsMultiline checks that a single long line
// with no explicit newline, once it's word-wrapped onto multiple visible
// rows, is treated the same as an explicit multi-line message for up/down
// purposes — it looks multiline on screen, so it should traverse like one.
func TestComposeUpDownSoftWrapCountsAsMultiline(t *testing.T) {
	chat := Chat{Address: "bob@localhost"}
	m := newTestModel(&fakeAccountAdder{})
	m.selectedView = viewChat
	m.accounts = []Account{{Name: "me", Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.chats.Select(0)
	m.setCurrentMessages([]Message{{Author: "bob", Content: "one"}, {Author: "bob", Content: "two"}})
	m.selectedMsg = 1

	// A single logical line, but long enough to wrap across several visual
	// rows at the input's (narrow, test-fixture) width.
	next, _ := m.Update(keyText(strings.Repeat("a", 300)))
	m = next.(Model)

	if lines := m.input.LineCount(); lines != 1 {
		t.Fatalf("input LineCount() = %d, want 1 (still one logical line)", lines)
	}
	if !m.composeMultiline() {
		t.Fatal("expected composeMultiline() true once the single line wraps onto multiple rows")
	}

	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = next.(Model)
	if m.selectedMsg != 1 {
		t.Fatalf("selectedMsg changed by up while the wrapped line is being edited, got %d", m.selectedMsg)
	}
}

// TestComposeEnterSendsNotNewline checks a plain enter still submits the
// message (via SelectSend) rather than inserting a newline into the input.
func TestComposeEnterSendsNotNewline(t *testing.T) {
	chat := Chat{Address: "bob@localhost"}
	m := newTestModelWithSender(newFakeDraftSaver(), &fakeAccountAdder{})
	m.selectedView = viewChat
	m.accounts = []Account{{Name: "me", Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.chats.Select(0)

	next, _ := m.Update(keyText("hi"))
	m = next.(Model)
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)

	if got := m.input.Value(); got != "" {
		t.Fatalf("input should be cleared after send, got %q", got)
	}
}

// TestComposeInputHeightOverride checks that dragging the compose box
// taller (inputHeightOverride, set by the mouse handlers in ui/mouse.go)
// actually grows the box beyond its default auto-grow cap, and that the
// drag is clamped to leave the viewport some room rather than being able to
// swallow the whole chat pane.
func TestComposeInputHeightOverride(t *testing.T) {
	m := newTestModel(&fakeAccountAdder{})
	m.selectedView = viewChat
	// newTestModel's m.height comes out of updateSizes() using termHeight,
	// which the fixture leaves at 0 — give it real room so
	// inputHeightMaxDrag has space to work with instead of clamping to 1.
	m.termHeight = 40
	m.updateSizes()

	if got := m.input.Height(); got != 1 {
		t.Fatalf("input Height() before any override = %d, want 1 (empty box)", got)
	}

	m.inputHeightOverride = inputMaxHeight + 3
	m.updateSizes()
	if got, want := m.input.Height(), inputMaxHeight+3; got != want {
		t.Fatalf("input Height() after override = %d, want %d (past the auto-grow cap)", got, want)
	}

	// A drag past inputHeightMaxDrag clamps down instead of eating the
	// whole chat pane.
	m.inputHeightOverride = m.inputHeightMaxDrag() + 100
	m.updateSizes()
	if got, want := m.inputHeightOverride, m.inputHeightMaxDrag(); got != want {
		t.Fatalf("inputHeightOverride after an over-large drag = %d, want clamped to %d", got, want)
	}
}

func ctrlBacktickKey() tea.KeyMsg { return tea.KeyPressMsg{Code: '`', Mod: tea.ModCtrl} }

// TestComposeToggleExpand checks ctrl+` grows the compose box to roughly
// half the chat pane and a second press shrinks it back — and that it
// restores a pre-existing user drag (inputHeightOverride) rather than always
// dropping to the default, without ever calling inputHeightSetter (this is
// transient view state, not a persisted config value).
func TestComposeToggleExpand(t *testing.T) {
	m := newTestModel(&fakeAccountAdder{})
	m.selectedView = viewChat
	m.termHeight = 40
	m.updateSizes()

	next, _ := m.Update(ctrlBacktickKey())
	m = next.(Model)
	if !m.composeExpanded {
		t.Fatal("composeExpanded should be true after ctrl+~")
	}
	if got, want := m.input.Height(), m.expandedComposeHeight(); got != want {
		t.Fatalf("input Height() after expand = %d, want %d", got, want)
	}
	if want := m.height / 2; m.input.Height() < want-2 || m.input.Height() > want {
		t.Fatalf("expanded input Height() = %d, want roughly half of m.height (%d)", m.input.Height(), m.height)
	}

	next, _ = m.Update(ctrlBacktickKey())
	m = next.(Model)
	if m.composeExpanded {
		t.Fatal("composeExpanded should be false after a second ctrl+~")
	}
	if got := m.input.Height(); got != 1 {
		t.Fatalf("input Height() after collapsing = %d, want 1 (back to default)", got)
	}

	// A prior drag is restored, not clobbered by the default.
	m.inputHeightOverride = 3
	m.updateSizes()
	next, _ = m.Update(ctrlBacktickKey())
	m = next.(Model)
	next, _ = m.Update(ctrlBacktickKey())
	m = next.(Model)
	if got, want := m.inputHeightOverride, 3; got != want {
		t.Fatalf("inputHeightOverride after expand+collapse = %d, want restored to %d", got, want)
	}
}

func ctrlShiftYKey() tea.KeyMsg { return tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl | tea.ModShift} }

// TestComposeYankDraft checks ctrl+shift+y copies the compose box's draft —
// a no-op (no notification) on an empty draft, and a notification cmd
// (success or "copy failed", depending on whether the test sandbox has a
// clipboard) once there's something to copy.
func TestComposeYankDraft(t *testing.T) {
	chat := Chat{Address: "bob@localhost"}
	m := newTestModel(&fakeAccountAdder{})
	m.selectedView = viewChat
	m.accounts = []Account{{Name: "me", Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.chats.Select(0)

	next, _ := m.Update(ctrlShiftYKey())
	m = next.(Model)
	if m.noticeID != 0 {
		t.Fatal("ctrl+shift+y on an empty draft should be a no-op, got a notification")
	}

	next, _ = m.Update(keyText("hi"))
	m = next.(Model)
	next, _ = m.Update(ctrlShiftYKey())
	m = next.(Model)
	if m.noticeID == 0 {
		t.Fatal("ctrl+shift+y on a non-empty draft should show a notification")
	}
	if got, want := m.input.Value(), "hi"; got != want {
		t.Fatalf("draft should be untouched by yanking, got %q, want %q", got, want)
	}
}

// TestComposeMouseWheelScrollsOverflowingInput checks that a wheel event
// over the compose box moves its cursor (and so its internal viewport, per
// textarea's DynamicHeight/MaxHeight-clamped scrolling) once the message is
// too tall to fit — mirroring the wheel scroll already wired up for the
// chat list and message viewport panes.
func TestComposeMouseWheelScrollsOverflowingInput(t *testing.T) {
	m := newTestModel(&fakeAccountAdder{})
	m.selectedView = viewChat
	m.termHeight = 40
	m.updateSizes()

	// More lines than inputMaxHeight, so the box is capped and scrolling
	// internally rather than still growing.
	for i := 0; i < 10; i++ {
		if i > 0 {
			next, _ := m.Update(shiftEnterKey())
			m = next.(Model)
		}
		next, _ := m.Update(keyText("line"))
		m = next.(Model)
	}
	if got := m.input.Height(); got != inputMaxHeight {
		t.Fatalf("input Height() with 10 lines = %d, want capped at inputMaxHeight (%d)", got, inputMaxHeight)
	}
	startLine := m.input.Line()

	// Render so the input pane's zone bounds are populated. bubblezone's
	// Scan() buffers the update through a channel/worker goroutine (its own
	// docs warn Get() right after Scan() may not see it yet), so poll
	// briefly instead of asserting immediately.
	_ = m.View()
	var z *zone.ZoneInfo
	deadline := time.Now().Add(time.Second)
	for {
		z = m.zone.Get(zonePaneInput)
		if !z.IsZero() || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if z.IsZero() {
		t.Fatal("zonePaneInput has no bounds after View()")
	}

	next, _ := m.Update(tea.MouseWheelMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseWheelUp})
	m = next.(Model)
	if want := startLine - inputWheelScrollLines; m.input.Line() != want {
		t.Fatalf("input cursor line after wheel-up = %d, want %d", m.input.Line(), want)
	}

	next, _ = m.Update(tea.MouseWheelMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseWheelDown})
	m = next.(Model)
	if m.input.Line() != startLine {
		t.Fatalf("input cursor line after wheel-down = %d, want back to %d", m.input.Line(), startLine)
	}
}

// TestComposeClickPositionsCursor checks that a left click inside the
// compose box places the cursor under the clicked character, not just
// focuses the box — mirroring native terminal apps' click-to-place-caret.
func TestComposeClickPositionsCursor(t *testing.T) {
	m := newTestModel(&fakeAccountAdder{})
	chat := Chat{Address: "bob@example.com"}
	m.accounts = []Account{{Name: "me", Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
		_ = cmd()
	}
	m.chats.Select(0)
	m.termHeight = 40
	m.updateSizes()
	// Open the chat first, same as a real click on the chat list would —
	// swapComposeDraft only skips its draft swap (which would wipe whatever
	// is typed below) once openChatAccountIdx/openChatAddress already match.
	tm, _ := m.openCurrentChat()
	m = tm.(Model)

	next, _ := m.Update(keyText("hello world"))
	m = next.(Model)

	_ = m.View()
	var z *zone.ZoneInfo
	deadline := time.Now().Add(time.Second)
	for {
		z = m.zone.Get(zoneInputTextarea)
		if !z.IsZero() || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if z.IsZero() {
		t.Fatal("zoneInputTextarea has no bounds after View()")
	}

	// Click on the "w" of "world" (index 6 in "hello world"), accounting for
	// the "› " prompt drawn before the text on screen.
	click := tea.MouseClickMsg{X: z.StartX + lipgloss.Width(inputPrompt) + 6, Y: z.StartY, Button: tea.MouseLeft}
	next, _ = m.Update(click)
	m = next.(Model)

	if got, want := m.input.Column(), 6; got != want {
		t.Fatalf("cursor column after click = %d, want %d", got, want)
	}
}
