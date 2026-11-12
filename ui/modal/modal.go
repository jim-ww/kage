// Package modal is one overlay system for a Bubble Tea program: a Stack of
// Modals, each a frame (a title, a body, a row of hints) around a
// Component - a text input, a yes/no question, a calendar, tabs of other
// components, anything implementing the interface. The Stack routes all
// input to the top modal, draws it, and hands its result back through the
// modal's OnSubmit, so the program embedding it only says what to show and
// what to do with the answer.
//
// Everything works with the keyboard alone and with the mouse alone: the
// hint row's hints are buttons that send their key when clicked, esc (and
// its always-shown hint, and a click outside) closes, and components take
// both keys and clicks. And it's built for small terminals: title and hints
// sit in the frame's border, the border goes away when space is short, and
// a body taller than the space scrolls.
//
// It knows nothing about the program embedding it beyond the H type its
// callbacks receive (the program's model, typically a pointer to it).
package modal

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/lrstanley/bubblezone/v2"
)

// Component is what a modal shows. Update gets every message meant for it
// - keys, mouse, paste, and the program's other messages (a text input's
// cursor blink) - and says through its Outcome whether it's done.
type Component interface {
	Update(msg tea.Msg) (tea.Cmd, Outcome)
	View() string
}

// Optional Component interfaces.
type (
	// Sizer is told the space its View has: width columns, height rows.
	Sizer interface{ SetSize(width, height int) }
	// Hinter lists the keys it takes, most important first, for the hint
	// row (which adds esc itself).
	Hinter interface{ Hints() []Hint }
	// Binder is given the zone manager and a zone ID prefix of its own,
	// to mark what it draws for mouse hit-testing.
	Binder interface {
		Bind(z *zone.Manager, id string)
	}
	// Focuser takes focus when its modal (or its tab) is showing.
	Focuser interface {
		Focus() tea.Cmd
		Blur()
	}
	// Widther says how wide it would like to be; the frame is otherwise as
	// wide as the body's View.
	Widther interface{ PreferredWidth() int }
	// Busier is busy with something it started that can't be walked away
	// from: while it is, esc and a click outside don't close its modal.
	Busier interface{ Busy() bool }
)

// Outcome is what a Component's Update says about the modal: keep going
// (the zero value), submit a value, or cancel.
type Outcome struct {
	kind  outcomeKind
	value any
}

type outcomeKind int

const (
	keepGoing outcomeKind = iota
	submitted
	canceled
	acted
)

// Continue keeps the modal open.
var Continue = Outcome{}

// Submit closes the modal and hands value to its OnSubmit.
func Submit(value any) Outcome { return Outcome{kind: submitted, value: value} }

// Cancel closes the modal as esc does.
func Cancel() Outcome { return Outcome{kind: canceled} }

// Acted is the value an Act outcome acts with - for a component wrapping
// another, to handle some of its actions itself.
func (o Outcome) Acted() (any, bool) { return o.value, o.kind == acted }

// Act hands value to the modal's OnAct and keeps it open: an action taken
// from inside it (copy this, toggle that), as opposed to its answer.
func Act(value any) Outcome { return Outcome{kind: acted, value: value} }

// Hint is one entry of the hint row: a key and what it does. Clicking it
// sends Key to the modal as if pressed, so every key shown is also a
// button.
type Hint struct {
	Key   tea.KeyPressMsg
	Label string
}

// KeyHint is a Hint for key, labeled label.
func KeyHint(key tea.KeyPressMsg, label string) Hint { return Hint{Key: key, Label: label} }

// Modal is one overlay: Body in a frame titled Title.
type Modal[H any] struct {
	// ID, if set, finds the modal again (Stack.Find) - to update it when
	// something it shows changes, or close it when an answer it waits on
	// arrives.
	ID    string
	Title string
	Body  Component
	// MaxWidth caps the frame's width (0: 60 columns); it's never wider
	// than the space it's shown in.
	MaxWidth int
	// Fill makes the frame take all the space it's shown in, whatever its
	// body asks for: for something read or browsed - a reference, a list -
	// rather than answered.
	Fill bool
	// OnSubmit gets the value the body submitted, after the modal closed.
	OnSubmit func(h H, value any) tea.Cmd
	// OnCancel, if set, runs when the modal is closed without a value.
	OnCancel func(h H) tea.Cmd
	// OnAct gets the values the body acts with (see Act); the modal stays
	// open.
	OnAct func(h H, value any) tea.Cmd

	scroll int // first body line shown, when the body is taller than the space
	width  int // outer width, fixed when shown or resized - see sizeTop
}

const defaultMaxWidth = 60

// Styles are the frame's styles.
type Styles struct {
	Border    lipgloss.Style
	Title     lipgloss.Style
	HintKey   lipgloss.Style
	HintLabel lipgloss.Style
	HintHover lipgloss.Style
}

// DefaultStyles are plain, theme-agnostic styles.
func DefaultStyles() Styles {
	return Styles{
		Border:    lipgloss.NewStyle().Faint(true),
		Title:     lipgloss.NewStyle().Bold(true),
		HintKey:   lipgloss.NewStyle().Bold(true),
		HintLabel: lipgloss.NewStyle().Faint(true),
		HintHover: lipgloss.NewStyle().Reverse(true),
	}
}

// Stack holds the open modals, the top one receiving input. The zero value
// is usable; set Zone for mouse support.
type Stack[H any] struct {
	Zone   *zone.Manager
	ID     string  // zone ID prefix; "modal" if empty
	Styles *Styles // DefaultStyles if nil

	modals        []*Modal[H]
	width, height int
	hoverHint     int // index into the top modal's hints under the pointer, or -1
}

var (
	escKey     = tea.KeyPressMsg{Code: tea.KeyEscape}
	scrollUp   = tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	scrollDown = tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
)

// Open reports whether any modal is open.
func (s Stack[H]) Open() bool { return len(s.modals) > 0 }

// Top is the modal receiving input, or nil.
func (s Stack[H]) Top() *Modal[H] {
	if len(s.modals) == 0 {
		return nil
	}
	return s.modals[len(s.modals)-1]
}

// Find is the topmost open modal with id, or nil.
func (s Stack[H]) Find(id string) *Modal[H] {
	for i := len(s.modals) - 1; i >= 0; i-- {
		if s.modals[i].ID == id {
			return s.modals[i]
		}
	}
	return nil
}

// Remove closes the modal with id wherever it is in the stack, without
// running its callbacks - when what it shows is gone.
func (s *Stack[H]) Remove(id string) tea.Cmd {
	for i := len(s.modals) - 1; i >= 0; i-- {
		if s.modals[i].ID != id {
			continue
		}
		if i == len(s.modals)-1 {
			return s.Close()
		}
		s.modals = append(s.modals[:i], s.modals[i+1:]...)
		return nil
	}
	return nil
}

// Push opens m on top of whatever is open.
func (s *Stack[H]) Push(m *Modal[H]) tea.Cmd {
	if top := s.Top(); top != nil {
		if f, ok := top.Body.(Focuser); ok {
			f.Blur()
		}
	}
	s.modals = append(s.modals, m)
	s.hoverHint = -1
	if b, ok := m.Body.(Binder); ok && s.Zone != nil {
		b.Bind(s.Zone, s.id()+"-"+strconv.Itoa(len(s.modals)))
	}
	s.sizeTop()
	if f, ok := m.Body.(Focuser); ok {
		return f.Focus()
	}
	return nil
}

// Close closes the top modal without running its callbacks.
func (s *Stack[H]) Close() tea.Cmd {
	if len(s.modals) == 0 {
		return nil
	}
	s.modals = s.modals[:len(s.modals)-1]
	s.hoverHint = -1
	if top := s.Top(); top != nil {
		s.sizeTop()
		if f, ok := top.Body.(Focuser); ok {
			return f.Focus()
		}
	}
	return nil
}

// SetSize is the space modals are shown in.
func (s *Stack[H]) SetSize(width, height int) {
	s.width, s.height = width, height
	s.sizeTop()
}

func (s *Stack[H]) id() string {
	if s.ID == "" {
		return "modal"
	}
	return s.ID
}

func (s *Stack[H]) styles() Styles {
	if s.Styles == nil {
		return DefaultStyles()
	}
	return *s.Styles
}

// Update routes msg to the top modal. Input - keys, mouse, paste - is
// consumed (true): nothing under a modal sees it. Anything else also
// reaches the program, which still has to handle its own messages.
func (s *Stack[H]) Update(h H, msg tea.Msg) (tea.Cmd, bool) {
	top := s.Top()
	if top == nil {
		return nil, false
	}
	busy := false
	if b, ok := top.Body.(Busier); ok {
		busy = b.Busy()
	}
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if busy && msg.String() == escKey.String() {
			return nil, true
		}
		return s.key(h, top, msg), true
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			return nil, true
		}
		if i := s.hintAt(msg); i >= 0 {
			hints, _ := s.hints(top)
			return s.key(h, top, hints[i].Key), true
		}
		// Outside the frame closes - when where the frame is is known: a
		// click can land before a fresh frame's zone is recorded.
		if s.Zone != nil {
			if z := s.Zone.Get(s.frameID()); !busy && !z.IsZero() && !z.InBounds(msg) {
				return s.finish(h, Cancel()), true
			}
		}
		return s.body(h, top, msg), true
	case tea.MouseMotionMsg:
		s.hoverHint = s.hintAt(msg)
		return s.body(h, top, msg), true
	case tea.MouseWheelMsg:
		if s.overflows(top) {
			if msg.Button == tea.MouseWheelUp {
				s.scrollBy(top, -1)
			} else if msg.Button == tea.MouseWheelDown {
				s.scrollBy(top, 1)
			}
			return nil, true
		}
		return s.body(h, top, msg), true
	case tea.MouseMsg, tea.PasteMsg:
		return s.body(h, top, msg), true
	}
	cmd, outcome := top.Body.Update(msg)
	return tea.Batch(cmd, s.finish(h, outcome)), false
}

func (s *Stack[H]) key(h H, top *Modal[H], msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case msg.String() == escKey.String():
		return s.finish(h, Cancel())
	case s.overflows(top) && msg.String() == scrollUp.String():
		s.scrollBy(top, -s.bodyHeight()/2)
		return nil
	case s.overflows(top) && msg.String() == scrollDown.String():
		s.scrollBy(top, s.bodyHeight()/2)
		return nil
	}
	return s.body(h, top, msg)
}

func (s *Stack[H]) body(h H, top *Modal[H], msg tea.Msg) tea.Cmd {
	cmd, outcome := top.Body.Update(msg)
	return tea.Batch(cmd, s.finish(h, outcome))
}

// finish closes the top modal on a submit or cancel and runs its callback
// - after closing, so the callback can open another modal.
func (s *Stack[H]) finish(h H, o Outcome) tea.Cmd {
	top := s.Top()
	switch o.kind {
	case keepGoing:
		return nil
	case acted:
		if top.OnAct != nil {
			return top.OnAct(h, o.value)
		}
		return nil
	}
	focus := s.Close()
	switch {
	case o.kind == submitted && top.OnSubmit != nil:
		return tea.Batch(focus, top.OnSubmit(h, o.value))
	case o.kind == canceled && top.OnCancel != nil:
		return tea.Batch(focus, top.OnCancel(h))
	}
	return focus
}

// ── Layout ──────────────────────────────────────────────────────────────

// bordered reports whether there's room for the border: without it, the
// frame is a title row, the body and the hint row, edge to edge.
func (s *Stack[H]) bordered() bool { return s.width >= 30 && s.height >= 8 }

func (s *Stack[H]) chrome() (cols, rows int) {
	if s.bordered() {
		return 4, 2 // "│ " … " │"; title and hints sit in the border rows
	}
	return 0, 2
}

func (s *Stack[H]) bodyHeight() int {
	_, rows := s.chrome()
	return max(1, s.height-rows)
}

// frameWidth is the top modal's outer width: wide enough for its body (or
// what the body asks for) and its title, within MaxWidth and the space.
func (s *Stack[H]) frameWidth(m *Modal[H]) int {
	cols, _ := s.chrome()
	if !s.bordered() || m.Fill {
		return max(1, s.width)
	}
	limit := m.MaxWidth
	if limit <= 0 {
		limit = defaultMaxWidth
	}
	want := 0
	if w, ok := m.Body.(Widther); ok {
		want = w.PreferredWidth()
	} else {
		want = lipgloss.Width(m.Body.View())
	}
	// Wide enough for its hints too, where the space allows: dropping
	// them is for small screens, not for short bodies.
	hints, kept := s.hints(m)
	want = max(want, lipgloss.Width(m.Title)+4, lipgloss.Width(ansi.Strip(s.hintRow(hints, kept, 1<<16)))+2, 20)
	return max(1, min(want+cols, limit, s.width))
}

// sizeTop fits the top modal to the space. Its width is decided here, not
// on every draw: what it's wide for (its hints, say) changes as it's used -
// another tab - and a frame resizing then would move what's under the
// pointer.
func (s *Stack[H]) sizeTop() {
	top := s.Top()
	if top == nil {
		return
	}
	top.width = s.frameWidth(top)
	if sz, ok := top.Body.(Sizer); ok {
		cols, _ := s.chrome()
		sz.SetSize(max(1, top.width-cols), s.bodyHeight())
		// How wide a body wants to be can depend on how tall it may be
		// (Columns): asked again now that it knows.
		if w := s.frameWidth(top); w != top.width {
			top.width = w
			sz.SetSize(max(1, w-cols), s.bodyHeight())
		}
	}
	s.scrollBy(top, 0)
}

func (s *Stack[H]) overflows(m *Modal[H]) bool {
	return strings.Count(m.Body.View(), "\n")+1 > s.bodyHeight()
}

func (s *Stack[H]) scrollBy(m *Modal[H], n int) {
	lines := strings.Count(m.Body.View(), "\n") + 1
	m.scroll = max(0, min(m.scroll+n, lines-s.bodyHeight()))
}

// ── View ────────────────────────────────────────────────────────────────

func (s *Stack[H]) frameID() string { return s.id() + "-frame" }

func (s *Stack[H]) hintID(i int) string { return s.id() + "-hint-" + strconv.Itoa(i) }

// hints is the top modal's hint row: its body's, then scrolling's when the
// body overflows, then esc. kept is how many of the last are never dropped
// for space: esc, so the modal can always be closed, and scrolling, the
// only way to the rest of an overflowing body.
func (s *Stack[H]) hints(m *Modal[H]) (hints []Hint, kept int) {
	if h, ok := m.Body.(Hinter); ok {
		hints = append(hints, h.Hints()...)
	}
	kept = 1
	if s.overflows(m) {
		hints = append(hints, KeyHint(scrollDown, "scroll"))
		kept = 2
	}
	if b, ok := m.Body.(Busier); ok && b.Busy() {
		return hints, min(kept-1, len(hints)) // nothing closes it while busy
	}
	return append(hints, KeyHint(escKey, "close")), kept
}

func (s *Stack[H]) hintAt(msg tea.MouseMsg) int {
	if s.Zone == nil || s.Top() == nil {
		return -1
	}
	hints, _ := s.hints(s.Top())
	for i := range hints {
		if s.Zone.Get(s.hintID(i)).InBounds(msg) {
			return i
		}
	}
	return -1
}

// hintRow renders as many of hints as fit in width, keeping the last kept
// whatever happens: the others are dropped from their end, least important
// first.
func (s *Stack[H]) hintRow(hints []Hint, kept int, width int) string {
	st := s.styles()
	short := false // keys only, when even the kept hints don't fit
	render := func(i int) string {
		h := hints[i]
		key := keyName(h.Key)
		text := st.HintKey.Render(key) + " " + st.HintLabel.Render(h.Label)
		plain := key + " " + h.Label
		if short {
			text, plain = st.HintKey.Render(key), key
		}
		if i == s.hoverHint {
			text = st.HintHover.Render(plain)
		}
		if s.Zone != nil {
			text = s.Zone.Mark(s.hintID(i), text)
		}
		return text
	}
	sep := st.HintLabel.Render(" · ")
	first := len(hints) - kept
	var tail []string
	used := 0
	for _, short = range []bool{false, true} {
		tail, used = nil, 0
		for i := first; i < len(hints); i++ {
			tail = append(tail, render(i))
			used += lipgloss.Width(tail[len(tail)-1]) + lipgloss.Width(sep)
		}
		if used-lipgloss.Width(sep) <= width {
			break
		}
	}
	var parts []string
	for i := range first {
		part := render(i)
		if used+lipgloss.Width(part)+lipgloss.Width(sep) > width+lipgloss.Width(sep) {
			break
		}
		parts = append(parts, part)
		used += lipgloss.Width(part) + lipgloss.Width(sep)
	}
	return strings.Join(append(parts, tail...), sep)
}

// View draws the top modal, centered in the space set by SetSize.
func (s *Stack[H]) View() string {
	top := s.Top()
	if top == nil {
		return ""
	}
	st := s.styles()
	width := top.width
	cols, _ := s.chrome()
	inner := max(1, width-cols)

	var body []string // none at all for an empty body: a question needing no detail
	if view := top.Body.View(); view != "" {
		body = strings.Split(view, "\n")
	}
	if len(body) > s.bodyHeight() {
		end := min(len(body), top.scroll+s.bodyHeight())
		body = body[top.scroll:end]
	}
	for top.Fill && len(body) < s.bodyHeight() {
		body = append(body, "")
	}
	title := st.Title.Render(ansi.Truncate(top.Title, max(1, inner-2), "…"))
	hintList, kept := s.hints(top)
	hintWidth := inner
	if s.bordered() {
		hintWidth = inner - 2 // what the border's corner dashes leave
	}
	hints := s.hintRow(hintList, kept, max(1, hintWidth))

	var lines []string
	if s.bordered() {
		edge := func(left, text, right string) string {
			fill := max(0, width-lipgloss.Width(text)-5)
			return st.Border.Render(left+"─ ") + text + st.Border.Render(" "+strings.Repeat("─", fill)+right)
		}
		lines = append(lines, edge("╭", title, "╮"))
		for _, l := range body {
			lines = append(lines, st.Border.Render("│ ")+padTo(l, inner)+st.Border.Render(" │"))
		}
		lines = append(lines, edge("╰", hints, "╯"))
	} else {
		lines = append(lines, padTo(title, inner))
		for _, l := range body {
			lines = append(lines, padTo(l, inner))
		}
		lines = append(lines, padTo(hints, inner))
	}
	frame := strings.Join(lines, "\n")
	if s.Zone != nil {
		frame = s.Zone.Mark(s.frameID(), frame)
	}
	return lipgloss.Place(s.width, s.height, lipgloss.Center, lipgloss.Center, frame)
}

// keyName is how a hint shows key: "^D" for ctrl+d, as terminals do,
// otherwise its usual name ("enter", "y").
func keyName(key tea.KeyPressMsg) string {
	name := key.String()
	if rest, ok := strings.CutPrefix(name, "ctrl+"); ok && len(rest) == 1 {
		return "^" + strings.ToUpper(rest)
	}
	return name
}

// padTo pads (or cuts) a rendered line to exactly width columns.
func padTo(line string, width int) string {
	if w := lipgloss.Width(line); w > width {
		return ansi.Truncate(line, width, "…")
	} else if w < width {
		return line + strings.Repeat(" ", width-w)
	}
	return line
}

// truncate cuts line to width columns, with an ellipsis.
func truncate(line string, width int) string { return ansi.Truncate(line, width, "…") }
