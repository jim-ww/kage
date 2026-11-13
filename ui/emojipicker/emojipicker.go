// Package emojipicker is a Bubble Tea component for picking one or more
// emoji: a query box over a grid, seeded with a "recent" ranking, else
// common reactions. It deals in plain strings, knowing nothing of kage.
package emojipicker

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/enescakir/emoji"
	"github.com/lrstanley/bubblezone/v2"
	"github.com/sahilm/fuzzy"
)

// entry is one grid cell: a literal emoji glyph plus the :shortcode: it came
// from (empty for a recent/common entry that wasn't reached via a search
// match - the grid doesn't need to show it then).
type entry struct {
	Emoji     string
	Shortcode string
}

// maxResults caps the matches ranked, so a loose fuzzy query doesn't sort
// thousands per keystroke.
const maxResults = 120

// queryWidth is the filter box's fixed width - wide enough to show the
// whole placeholder hint without truncation.
const queryWidth = 40

// defaultVisibleRows is how many grid rows show at once before scrolling.
const defaultVisibleRows = 4

// CellWidth is every grid cell's width, forced so the grid stays aligned
// whatever width terminals give a glyph (flags and ZWJ sequences vary).
// Exactly a picked "[emoji]": narrower, it would wrap onto another line.
const CellWidth = 4

// KeyMap is the picker's keybindings. Deliberately arrow-keys-only for
// navigation (not vim h/j/k/l) since the query box is always "focused" -
// any letter typed is a filter character, not a nav key.
type KeyMap struct {
	Up, Down, Left, Right key.Binding
	Toggle                key.Binding // add/remove the highlighted cell from the multi-select set without closing
	ClearPicked           key.Binding // empty the multi-select set entirely
	Confirm               key.Binding // close, returning the multi-select set (or just the highlighted cell if nothing was toggled)
	Cancel                key.Binding
}

// DefaultKeyMap returns the picker's default keybindings.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Up:          key.NewBinding(key.WithKeys("up"), key.WithHelp("↑", "up")),
		Down:        key.NewBinding(key.WithKeys("down"), key.WithHelp("↓", "down")),
		Left:        key.NewBinding(key.WithKeys("left"), key.WithHelp("←", "left")),
		Right:       key.NewBinding(key.WithKeys("right"), key.WithHelp("→", "right")),
		Toggle:      key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "toggle")),
		ClearPicked: key.NewBinding(key.WithKeys("ctrl+x"), key.WithHelp("ctrl+x", "clear picked")),
		Confirm:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm")),
		Cancel:      key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
	}
}

// Styles are the lipgloss styles the picker renders with.
type Styles struct {
	Title       lipgloss.Style
	Query       lipgloss.Style
	Cell        lipgloss.Style
	CellCursor  lipgloss.Style
	CellPicked  lipgloss.Style
	PickedRow   lipgloss.Style // the "Picked: ..." summary line, always shown regardless of query
	PickedHover lipgloss.Style // a single picked chip within PickedRow, under the pointer (see SetPickedHover)
	Footer      lipgloss.Style
	Placeholder lipgloss.Style
}

// DefaultStyles returns plain default styles. CellPicked has no text
// attributes: on an emoji, many terminals then render the narrow text
// glyph. A picked cell is bracketed instead.
func DefaultStyles() Styles {
	return Styles{
		Title:       lipgloss.NewStyle().Bold(true),
		Query:       lipgloss.NewStyle(),
		Cell:        lipgloss.NewStyle().Width(CellWidth),
		CellCursor:  lipgloss.NewStyle().Width(CellWidth).Reverse(true),
		CellPicked:  lipgloss.NewStyle().Width(CellWidth),
		PickedRow:   lipgloss.NewStyle(),
		PickedHover: lipgloss.NewStyle().Reverse(true),
		Footer:      lipgloss.NewStyle().Faint(true),
		Placeholder: lipgloss.NewStyle().Faint(true),
	}
}

// Model is the emoji picker's Bubble Tea sub-model. Zero value isn't
// usable - construct with New.
type Model struct {
	KeyMap      KeyMap
	Styles      Styles
	Columns     int // grid width in cells; rows follow from len(visible)
	VisibleRows int // how many grid rows show at once before scrolling
	Width       int // content width budget in columns - single-line rows (title, picked summary, footer) longer than this are truncated with an ellipsis rather than overflowing the popup

	Title string // e.g. "react to \"...\"" - shown above the query box; empty renders no title row

	// Compact drops the blank lines, scroll indicator and key hints (see
	// Hints), and the picked row until something is, for a small popup.
	Compact bool

	// Zone, if set, marks every cell (namespaced by ID) for the embedder to
	// hit-test with CellZoneID/VisibleCells and ClickConfirm.
	Zone *zone.Manager
	ID   string

	query       textinput.Model
	cursor      int
	scrollRow   int // index of the first grid row currently shown, follows cursor (see ensureCursorVisible)
	visible     []entry
	picked      []string // toggled emoji, in the order picked (Toggle), independent of visible/cursor - always shown in full via the "Picked:" row regardless of scroll/query
	touched     bool     // true once Toggle or ClearPicked has been used at least once, even if picked ended up empty again - see DidConfirm
	recent      []string
	common      []string
	pickedHover int // index into picked currently under the pointer, or -1 - see SetPickedHover
}

// New returns a ready-to-use Model. recent is this user's own most-used
// emoji, ranked best-first (may be nil/empty); it's shown ahead of the
// built-in common list until a search query narrows the grid.
func New(recent []string) Model {
	q := textinput.New()
	q.Placeholder = "type to search, e.g. fire, +1, idk..."
	q.Prompt = "🔍 "
	// Without a width, textinput shows only the placeholder's first rune.
	q.SetWidth(queryWidth)
	q.Focus()

	m := Model{
		KeyMap:      DefaultKeyMap(),
		Styles:      DefaultStyles(),
		Columns:     8,
		VisibleRows: defaultVisibleRows,
		Width:       queryWidth,
		query:       q,
		recent:      recent,
		common:      commonEmoji,
		pickedHover: -1,
	}
	m.refresh()
	return m
}

// SetPicked seeds the initial multi-select set (e.g. a message's existing
// reactions), so reopening the picker on something already reacted to shows
// those cells as picked instead of starting empty.
func (m *Model) SetPicked(emojis []string) {
	m.picked = append([]string(nil), emojis...)
}

// Resize sets Columns/VisibleRows/Width (each unchanged if <= 0), refitting
// the query box, cursor and scroll.
func (m *Model) Resize(columns, visibleRows, width int) {
	if columns > 0 {
		m.Columns = columns
	}
	if visibleRows > 0 {
		m.VisibleRows = visibleRows
	}
	if width > 0 {
		m.Width = width
		// The query prompt "🔍 " eats a few columns of its own; textinput's
		// Width is the value field alone, not the prompt+field total.
		m.query.SetWidth(max(1, width-ansi.StringWidth(m.query.Prompt)))
	}
	if m.cursor >= len(m.visible) {
		m.cursor = max(0, len(m.visible)-1)
	}
	m.ensureCursorVisible()
}

// Selection returns the emoji currently toggled on, in pick order.
func (m *Model) Selection() []string {
	return m.picked
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	return textinput.Blink
}

// Update implements tea.Model. Confirm and Cancel do nothing here: callers
// check DidConfirm/DidCancel with the same msg and close the picker.
func (m *Model) Update(msg tea.Msg) tea.Cmd {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch {
		case key.Matches(keyMsg, m.KeyMap.Up):
			m.moveCursor(-m.Columns)
			return nil
		case key.Matches(keyMsg, m.KeyMap.Down):
			m.moveCursor(m.Columns)
			return nil
		case key.Matches(keyMsg, m.KeyMap.Left):
			m.moveCursor(-1)
			return nil
		case key.Matches(keyMsg, m.KeyMap.Right):
			m.moveCursor(1)
			return nil
		case key.Matches(keyMsg, m.KeyMap.Toggle):
			m.toggleCursor()
			return nil
		case key.Matches(keyMsg, m.KeyMap.ClearPicked):
			m.picked = nil
			m.touched = true
			return nil
		case key.Matches(keyMsg, m.KeyMap.Confirm), key.Matches(keyMsg, m.KeyMap.Cancel):
			return nil
		}
	}

	oldQuery := m.query.Value()
	var cmd tea.Cmd
	m.query, cmd = m.query.Update(msg)
	if m.query.Value() != oldQuery {
		m.cursor = 0
		m.refresh()
	}
	return cmd
}

// DidConfirm reports whether msg is the Confirm key, and the set it confirms
// (see confirmFrom).
func (m *Model) DidConfirm(msg tea.Msg) ([]string, bool) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok || !key.Matches(keyMsg, m.KeyMap.Confirm) {
		return nil, false
	}
	return m.confirmFrom(m.cursor), true
}

// ClickConfirm is the set a click on cell i confirms, as Confirm on it
// would.
func (m *Model) ClickConfirm(index int) []string {
	return m.confirmFrom(index)
}

// confirmFrom is the set confirmed on cell index: the picked set once
// touched, else that cell toggled onto the picked one.
func (m *Model) confirmFrom(index int) []string {
	// Not by picked being non-empty: SetPicked seeds it with the message's
	// reactions, and confirming a cell must still toggle it.
	if m.touched {
		return m.picked
	}
	if index >= 0 && index < len(m.visible) {
		e := m.visible[index].Emoji
		// Toggled on the picked set: added if new, removed if picked.
		if m.isPicked(e) {
			out := make([]string, 0, len(m.picked)-1)
			for _, p := range m.picked {
				if p != e {
					out = append(out, p)
				}
			}
			return out
		}
		return append(append([]string(nil), m.picked...), e)
	}
	return []string{}
}

// CellZoneID is cell i's zone mark ID, once Zone is set.
func (m *Model) CellZoneID(i int) string {
	return m.ID + "-cell-" + strconv.Itoa(i)
}

// VisibleCells is the indices of the cells shown, which have zone marks.
func (m *Model) VisibleCells() []int {
	if len(m.visible) == 0 || m.Columns <= 0 {
		return nil
	}
	totalRows := (len(m.visible) + m.Columns - 1) / m.Columns
	startRow := m.scrollRow
	endRow := min(startRow+m.VisibleRows, totalRows)
	start := startRow * m.Columns
	end := min(endRow*m.Columns, len(m.visible))
	out := make([]int, 0, end-start)
	for i := start; i < end; i++ {
		out = append(out, i)
	}
	return out
}

// SetCursor highlights cell i, clamped, scrolling it into view.
func (m *Model) SetCursor(i int) {
	if len(m.visible) == 0 {
		return
	}
	m.cursor = clampInt(i, 0, len(m.visible)-1)
	m.ensureCursorVisible()
}

// SetPickedHover marks picked chip i hovered (see PickedZoneID);
// ClearPickedHover undoes it.
func (m *Model) SetPickedHover(i int) {
	if i < 0 || i >= len(m.picked) {
		return
	}
	m.pickedHover = i
}

// ClearPickedHover reports no picked chip is under the pointer.
func (m *Model) ClearPickedHover() {
	m.pickedHover = -1
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// DidCancel reports whether msg is the Cancel key.
func (m *Model) DidCancel(msg tea.Msg) bool {
	keyMsg, ok := msg.(tea.KeyMsg)
	return ok && key.Matches(keyMsg, m.KeyMap.Cancel)
}

// moveCursor moves the cursor by delta cells, clamped, without wrapping.
func (m *Model) moveCursor(delta int) {
	if len(m.visible) == 0 {
		return
	}
	next := m.cursor + delta
	if next < 0 {
		next = 0
	}
	if next >= len(m.visible) {
		next = len(m.visible) - 1
	}
	m.cursor = next
	m.ensureCursorVisible()
}

// ensureCursorVisible scrolls the minimum amount needed to bring the
// cursor's row back within [scrollRow, scrollRow+VisibleRows).
func (m *Model) ensureCursorVisible() {
	if m.Columns <= 0 {
		return
	}
	row := m.cursor / m.Columns
	switch {
	case row < m.scrollRow:
		m.scrollRow = row
	case m.VisibleRows > 0 && row >= m.scrollRow+m.VisibleRows:
		m.scrollRow = row - m.VisibleRows + 1
	}
}

func (m *Model) toggleCursor() {
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return
	}
	m.touched = true
	e := m.visible[m.cursor].Emoji
	for i, p := range m.picked {
		if p == e {
			m.picked = append(m.picked[:i], m.picked[i+1:]...)
			return
		}
	}
	m.picked = append(m.picked, e)
}

func (m *Model) refresh() {
	if q := strings.TrimSpace(m.query.Value()); q != "" {
		m.visible = search(q)
	} else {
		m.visible = defaultEntries(m.recent, m.common)
	}
	if m.cursor >= len(m.visible) {
		m.cursor = max(0, len(m.visible)-1)
	}
	m.scrollRow = 0
}

func (m *Model) isPicked(e string) bool {
	for _, p := range m.picked {
		if p == e {
			return true
		}
	}
	return false
}

// View implements tea.Model.
func (m *Model) View() string {
	var b strings.Builder
	if m.Title != "" {
		b.WriteString(m.line(m.Styles.Title, m.Title))
		b.WriteString(m.gap())
	}
	b.WriteString(m.line(m.Styles.Query, m.query.View()))
	b.WriteString(m.gap())
	if !m.Compact || len(m.picked) > 0 {
		// Compact shows it with the first pick; there's nothing to show
		// before that.
		m.writePickedRow(&b)
	}

	totalRows := 0
	if len(m.visible) > 0 && m.Columns > 0 {
		totalRows = (len(m.visible) + m.Columns - 1) / m.Columns
	}

	if len(m.visible) == 0 {
		b.WriteString(m.Styles.Placeholder.Render("no matches"))
	} else {
		startRow := m.scrollRow
		endRow := min(startRow+m.VisibleRows, totalRows)
		for r := startRow; r < endRow; r++ {
			start := r * m.Columns
			end := min(start+m.Columns, len(m.visible))
			row := make([]string, 0, end-start)
			for j := start; j < end; j++ {
				row = append(row, m.renderCell(j))
			}
			if r > startRow {
				b.WriteString("\n")
			}
			b.WriteString(strings.Join(row, ""))
		}
	}
	if m.Compact {
		return b.String()
	}
	b.WriteString("\n")

	if totalRows > m.VisibleRows {
		b.WriteString(m.line(m.Styles.Footer, fmt.Sprintf("rows %d-%d of %d", min(m.scrollRow+1, totalRows), min(m.scrollRow+m.VisibleRows, totalRows), totalRows)))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(m.line(m.Styles.Footer, Hints))
	return b.String()
}

// Hints are the picker's keys, as its footer lists them.
const Hints = "↑↓←→ move · tab multi · ctrl+x clear · enter confirm · esc cancel"

// gap separates View's rows: a blank line, or just the line break when
// Compact.
func (m *Model) gap() string {
	if m.Compact {
		return "\n"
	}
	return "\n\n"
}

// writePickedRow writes the "Picked: ..." row and the gap after it.
func (m *Model) writePickedRow(b *strings.Builder) {
	// The picked set, which a query may filter out of the grid. Not
	// truncated with zone marks, which cutting would corrupt.
	if m.Zone != nil {
		b.WriteString(m.pickedRowView())
	} else {
		b.WriteString(m.line(m.Styles.PickedRow, m.pickedRowText()))
	}
	b.WriteString(m.gap())
}

// line renders text with style, truncated to m.Width.
func (m *Model) line(style lipgloss.Style, text string) string {
	if m.Width > 0 {
		text = ansi.Truncate(text, m.Width, "…")
	}
	return style.Render(text)
}

// pickedRowText renders the "Picked: ..." summary line's content.
func (m *Model) pickedRowText() string {
	if len(m.picked) == 0 {
		return "Picked: (none)"
	}
	return "Picked: " + strings.Join(m.picked, " ")
}

// pickedRowView is pickedRowText with each chip zone-marked (PickedZoneID),
// for RemovePickedAt.
func (m *Model) pickedRowView() string {
	label := m.Styles.PickedRow.Render("Picked: ")
	if len(m.picked) == 0 {
		return label + m.Styles.PickedRow.Render("(none)")
	}
	chips := make([]string, len(m.picked))
	for i, e := range m.picked {
		style := m.Styles.PickedRow
		if i == m.pickedHover {
			style = m.Styles.PickedHover
		}
		chips[i] = m.Zone.Mark(m.PickedZoneID(i), style.Render(e))
	}
	return label + strings.Join(chips, " ")
}

// PickedZoneID is picked chip i's zone mark ID.
func (m *Model) PickedZoneID(i int) string {
	return m.ID + "-picked-" + strconv.Itoa(i)
}

// RemovePickedAt unpicks the i'th picked emoji, marking the picker touched.
func (m *Model) RemovePickedAt(i int) {
	if i < 0 || i >= len(m.picked) {
		return
	}
	m.touched = true
	m.picked = append(m.picked[:i], m.picked[i+1:]...)
}

func (m *Model) renderCell(i int) string {
	e := m.visible[i].Emoji
	// Picked is bracketed (see DefaultStyles), before any style.
	text := e
	if m.isPicked(e) {
		text = "[" + e + "]"
	}
	var rendered string
	switch {
	case i == m.cursor:
		rendered = m.Styles.CellCursor.Render(text)
	case m.isPicked(e):
		rendered = m.Styles.CellPicked.Render(text)
	default:
		rendered = m.Styles.Cell.Render(text)
	}
	if m.Zone != nil {
		rendered = m.Zone.Mark(m.CellZoneID(i), rendered)
	}
	return rendered
}

// defaultEntries builds the grid shown before any query is typed: recent
// first (deduplicated), padded out with common, capped at maxResults.
func defaultEntries(recent, common []string) []entry {
	seen := make(map[string]bool, maxResults)
	out := make([]entry, 0, maxResults)
	add := func(e string) {
		if len(out) >= maxResults || seen[e] {
			return
		}
		seen[e] = true
		out = append(out, entry{Emoji: e})
	}
	for _, e := range recent {
		add(e)
	}
	for _, e := range common {
		add(e)
	}
	return out
}

// search fuzzy-matches query against both shortcode names and the curated
// synonym table (see synonyms.go), so e.g. "idk" finds :shrug: even though
// the word "idk" never appears in the shortcode itself.
func search(query string) []entry {
	type scored struct {
		code  string
		score int
	}
	best := make(map[string]int)
	consider := func(code string, score int) {
		if s, ok := best[code]; !ok || score > s {
			best[code] = score
		}
	}

	for _, m := range fuzzy.Find(query, shortcodes) {
		consider(m.Str, m.Score)
	}
	// Synonym matches are curated, deliberate associations ("idk" -> shrug)
	// rather than incidental character overlap, so they rank above whatever
	// noise plain fuzzy shortcode matching turns up.
	const synonymBonus = 1000
	for _, m := range fuzzy.Find(query, synonymWords) {
		for _, code := range synonyms[m.Str] {
			consider(code, m.Score+synonymBonus)
		}
	}

	ranked := make([]scored, 0, len(best))
	for code, score := range best {
		ranked = append(ranked, scored{code, score})
	}
	// Break score ties by shortcode so results are deterministic - map
	// iteration order above is randomized, and equal scores are common.
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].code < ranked[j].code
	})

	// Shortcodes alias glyphs (":+1:", ":thumbsup:"): one cell per glyph.
	seen := make(map[string]bool, maxResults)
	out := make([]entry, 0, min(len(ranked), maxResults))
	for _, r := range ranked {
		if len(out) >= maxResults {
			break
		}
		e := emoji.Parse(r.code)
		if seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, entry{Shortcode: r.code, Emoji: e})
	}
	return out
}
