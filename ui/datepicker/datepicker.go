// Package datepicker is a self-contained Bubble Tea component for picking a
// calendar day: a month grid driven by the keyboard (arrows/hjkl move by
// day and week, pgup/pgdown by month) and, given a bubblezone manager, by
// the mouse too (hover highlights a day, click picks it, ‹ › and the wheel
// turn the month). It knows nothing about the program embedding it: it
// takes and hands back plain time.Time days, and reports a pick as a
// PickedMsg.
package datepicker

import (
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/lrstanley/bubblezone/v2"
)

// Width is the rendered width of the calendar in columns: seven 2-column
// day cells with a 1-column gap between them.
const Width = 7*2 + 6

// Height is the rendered height in rows: the month header, the weekday
// names and six weeks - always six, so the calendar never changes size as
// months change.
const Height = 2 + 6

// Hints lists the default keys, for an embedder's footer.
const Hints = "←→↑↓ day · pgup/pgdn month · t today · enter pick"

// PickedMsg reports a picked day: midnight at its start, in the location
// the picker was created with. ID is the picking Model's ID.
type PickedMsg struct {
	ID   string
	Date time.Time
}

// KeyMap is the picker's key bindings.
type KeyMap struct {
	PrevDay   key.Binding
	NextDay   key.Binding
	PrevWeek  key.Binding
	NextWeek  key.Binding
	PrevMonth key.Binding
	NextMonth key.Binding
	PrevYear  key.Binding
	NextYear  key.Binding
	Today     key.Binding
	Pick      key.Binding
}

// DefaultKeyMap returns the default key bindings.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		PrevDay:   key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←", "previous day")),
		NextDay:   key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→", "next day")),
		PrevWeek:  key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑", "previous week")),
		NextWeek:  key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓", "next week")),
		PrevMonth: key.NewBinding(key.WithKeys("pgup", "["), key.WithHelp("pgup", "previous month")),
		NextMonth: key.NewBinding(key.WithKeys("pgdown", "]"), key.WithHelp("pgdn", "next month")),
		PrevYear:  key.NewBinding(key.WithKeys("{"), key.WithHelp("{", "previous year")),
		NextYear:  key.NewBinding(key.WithKeys("}"), key.WithHelp("}", "next year")),
		Today:     key.NewBinding(key.WithKeys("t", "home"), key.WithHelp("t", "today")),
		Pick:      key.NewBinding(key.WithKeys("enter", "space"), key.WithHelp("enter", "pick")),
	}
}

// Styles are the lipgloss styles the picker renders with. Day styles are
// applied to the 2-column day number, so a background fills the cell.
type Styles struct {
	Header   lipgloss.Style // month and year
	Nav      lipgloss.Style // the ‹ › buttons
	NavHover lipgloss.Style
	Weekday  lipgloss.Style // the weekday names row
	Day      lipgloss.Style
	Today    lipgloss.Style
	Cursor   lipgloss.Style
	Hover    lipgloss.Style
	Disabled lipgloss.Style // outside Min/Max
}

// DefaultStyles returns plain, theme-agnostic styles - an embedder with a
// theme will normally override them after New.
func DefaultStyles() Styles {
	return Styles{
		Header:   lipgloss.NewStyle().Bold(true),
		Nav:      lipgloss.NewStyle().Faint(true),
		NavHover: lipgloss.NewStyle().Reverse(true),
		Weekday:  lipgloss.NewStyle().Faint(true),
		Day:      lipgloss.NewStyle(),
		Today:    lipgloss.NewStyle().Bold(true).Underline(true),
		Cursor:   lipgloss.NewStyle().Reverse(true),
		Hover:    lipgloss.NewStyle().Underline(true),
		Disabled: lipgloss.NewStyle().Faint(true),
	}
}

// Model is the picker's state. Construct with New.
type Model struct {
	KeyMap KeyMap
	Styles Styles

	// WeekStart is the weekday the grid's columns start on; Monday from New.
	WeekStart time.Weekday
	// Min and Max bound the days that can be moved to and picked
	// (inclusive); a zero value leaves that side unbounded.
	Min, Max time.Time
	// Today is the day marked as today; time.Now() from New.
	Today time.Time

	// Zone/ID opt into mouse support: with Zone set, View marks the whole
	// calendar, each day and the ‹ › buttons as zones (named by ID, so
	// several pickers, or other zone users, don't collide) and Update
	// hit-tests mouse messages against them. The embedder must pass its
	// final frame through Zone.Scan, as with any bubblezone use.
	Zone *zone.Manager
	ID   string

	cursor   time.Time
	hover    time.Time // zero when no day is under the pointer
	hoverNav int       // -1 / +1 while ‹ / › is under the pointer
	picked   bool      // the last Update picked the cursor's day
}

// Picked is the day the last Update picked, if it picked one - the same
// day its PickedMsg carries, for an embedder that wants the answer right
// away rather than as a message.
func (m Model) Picked() (time.Time, bool) { return m.cursor, m.picked }

// New returns a picker with its cursor on day.
func New(day time.Time) Model {
	return Model{
		KeyMap:    DefaultKeyMap(),
		Styles:    DefaultStyles(),
		WeekStart: time.Monday,
		Today:     time.Now(),
		ID:        "datepicker",
		cursor:    startOfDay(day),
	}
}

// Cursor is the day under the keyboard cursor.
func (m Model) Cursor() time.Time { return m.cursor }

// SetCursor moves the cursor to day, kept within Min/Max.
func (m *Model) SetCursor(day time.Time) { m.cursor = m.clamp(startOfDay(day)) }

// Init implements tea.Model.
func (m Model) Init() tea.Cmd { return nil }

// Update handles keys and, with Zone set, mouse messages. A pick (enter, or
// a click on a day) returns a command producing a PickedMsg.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	m.picked = false
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, m.KeyMap.PrevDay):
			m.SetCursor(m.cursor.AddDate(0, 0, -1))
		case key.Matches(msg, m.KeyMap.NextDay):
			m.SetCursor(m.cursor.AddDate(0, 0, 1))
		case key.Matches(msg, m.KeyMap.PrevWeek):
			m.SetCursor(m.cursor.AddDate(0, 0, -7))
		case key.Matches(msg, m.KeyMap.NextWeek):
			m.SetCursor(m.cursor.AddDate(0, 0, 7))
		case key.Matches(msg, m.KeyMap.PrevMonth):
			m.SetCursor(addMonths(m.cursor, -1))
		case key.Matches(msg, m.KeyMap.NextMonth):
			m.SetCursor(addMonths(m.cursor, 1))
		case key.Matches(msg, m.KeyMap.PrevYear):
			m.SetCursor(addMonths(m.cursor, -12))
		case key.Matches(msg, m.KeyMap.NextYear):
			m.SetCursor(addMonths(m.cursor, 12))
		case key.Matches(msg, m.KeyMap.Today):
			m.SetCursor(m.Today)
		case key.Matches(msg, m.KeyMap.Pick):
			cmd := m.pick(m.cursor)
			m.picked = cmd != nil
			return m, cmd
		}
	case tea.MouseMotionMsg:
		m.hover, m.hoverNav = time.Time{}, 0
		if m.Zone == nil {
			break
		}
		if day, ok := m.dayAt(msg); ok {
			m.hover = day
		} else {
			m.hoverNav = m.navAt(msg)
		}
	case tea.MouseClickMsg:
		if m.Zone == nil || msg.Button != tea.MouseLeft {
			break
		}
		if day, ok := m.dayAt(msg); ok {
			m.cursor = day
			m.picked = true
			return m, m.pick(day)
		}
		if dir := m.navAt(msg); dir != 0 {
			m.SetCursor(addMonths(m.cursor, dir))
		}
	case tea.MouseWheelMsg:
		if m.Zone == nil || !m.Zone.Get(m.ID).InBounds(msg) {
			break
		}
		switch msg.Button {
		case tea.MouseWheelUp:
			m.SetCursor(addMonths(m.cursor, -1))
		case tea.MouseWheelDown:
			m.SetCursor(addMonths(m.cursor, 1))
		}
	}
	return m, nil
}

func (m Model) pick(day time.Time) tea.Cmd {
	if !m.enabled(day) {
		return nil
	}
	id := m.ID
	return func() tea.Msg { return PickedMsg{ID: id, Date: day} }
}

// dayAt is the enabled day of the shown month under the pointer, if any.
func (m Model) dayAt(msg tea.MouseMsg) (time.Time, bool) {
	first := firstOfMonth(m.cursor)
	for d := first; d.Month() == first.Month(); d = d.AddDate(0, 0, 1) {
		if m.enabled(d) && m.Zone.Get(m.dayZoneID(d)).InBounds(msg) {
			return d, true
		}
	}
	return time.Time{}, false
}

// navAt is -1 or +1 when ‹ or › is under the pointer, else 0.
func (m Model) navAt(msg tea.MouseMsg) int {
	switch {
	case m.Zone.Get(m.ID + "-prev").InBounds(msg):
		return -1
	case m.Zone.Get(m.ID + "-next").InBounds(msg):
		return 1
	}
	return 0
}

func (m Model) dayZoneID(d time.Time) string {
	return m.ID + "-day-" + d.Format("20060102")
}

func (m Model) mark(id, s string) string {
	if m.Zone == nil {
		return s
	}
	return m.Zone.Mark(id, s)
}

// View renders the calendar: Width columns by Height rows.
func (m Model) View() string {
	first := firstOfMonth(m.cursor)

	prevStyle, nextStyle := m.Styles.Nav, m.Styles.Nav
	switch m.hoverNav {
	case -1:
		prevStyle = m.Styles.NavHover
	case 1:
		nextStyle = m.Styles.NavHover
	}
	prev := m.mark(m.ID+"-prev", prevStyle.Render(" ‹ "))
	next := m.mark(m.ID+"-next", nextStyle.Render(" › "))
	title := lipgloss.PlaceHorizontal(Width-6, lipgloss.Center, m.Styles.Header.Render(first.Format("January 2006")))
	rows := []string{prev + title + next}

	names := make([]string, 7)
	for i := range names {
		names[i] = time.Weekday((int(m.WeekStart) + i) % 7).String()[:2]
	}
	rows = append(rows, m.Styles.Weekday.Render(strings.Join(names, " ")))

	lead := (int(first.Weekday()) - int(m.WeekStart) + 7) % 7
	day := first.AddDate(0, 0, -lead)
	for range 6 {
		cells := make([]string, 7)
		for i := range cells {
			cells[i] = m.renderDay(day, first.Month())
			day = day.AddDate(0, 0, 1)
		}
		rows = append(rows, strings.Join(cells, " "))
	}
	return m.mark(m.ID, strings.Join(rows, "\n"))
}

// renderDay is one 2-column cell: blank for a day outside the shown month.
func (m Model) renderDay(d time.Time, month time.Month) string {
	if d.Month() != month {
		return "  "
	}
	label := strconv.Itoa(d.Day())
	if len(label) == 1 {
		label = " " + label
	}
	style := m.Styles.Day
	switch {
	case !m.enabled(d):
		return m.Styles.Disabled.Render(label)
	case d.Equal(m.cursor):
		style = m.Styles.Cursor
	case d.Equal(m.hover):
		style = m.Styles.Hover
	case sameDay(d, m.Today):
		style = m.Styles.Today
	}
	return m.mark(m.dayZoneID(d), style.Render(label))
}

func (m Model) enabled(d time.Time) bool {
	if !m.Min.IsZero() && d.Before(startOfDay(m.Min)) {
		return false
	}
	if !m.Max.IsZero() && d.After(startOfDay(m.Max)) {
		return false
	}
	return true
}

func (m Model) clamp(d time.Time) time.Time {
	if !m.Min.IsZero() && d.Before(startOfDay(m.Min)) {
		return startOfDay(m.Min)
	}
	if !m.Max.IsZero() && d.After(startOfDay(m.Max)) {
		return startOfDay(m.Max)
	}
	return d
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func firstOfMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.In(a.Location()).Date()
	return ay == by && am == bm && ad == bd
}

// addMonths moves t by n months, keeping its day of month where that month
// has it and clamping to the month's last day where it doesn't (31 Jan + 1
// month is 28 or 29 Feb, not 3 Mar as time.AddDate gives).
func addMonths(t time.Time, n int) time.Time {
	first := time.Date(t.Year(), t.Month()+time.Month(n), 1, 0, 0, 0, 0, t.Location())
	last := first.AddDate(0, 1, -1).Day()
	return time.Date(first.Year(), first.Month(), min(t.Day(), last), 0, 0, 0, 0, t.Location())
}
