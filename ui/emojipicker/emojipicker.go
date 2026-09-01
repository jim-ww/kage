// Package emojipicker is a self-contained Bubble Tea component for picking
// one or more emoji: a type-to-filter query box over a fixed-column grid,
// seeded with a caller-supplied "recent" ranking and falling back to a
// curated common-reaction list. It knows nothing about kage's UI/message
// types - callers seed it with plain strings and read plain strings back
// out (see Selection/DidConfirm) - so it can be dropped into any Bubble Tea
// program, not just this one.
package emojipicker

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/enescakir/emoji"
	"github.com/sahilm/fuzzy"
)

// entry is one grid cell: a literal emoji glyph plus the :shortcode: it came
// from (empty for a recent/common entry that wasn't reached via a search
// match - the grid doesn't need to show it then).
type entry struct {
	Emoji     string
	Shortcode string
}

// maxResults caps how many matches search ever ranks, so a very loose query
// (fuzzy subsequence matching is permissive) can't make one keystroke sort
// thousands of shortcodes - the grid itself scrolls (see VisibleRows) to
// reach anything beyond the first screenful.
const maxResults = 120

// queryWidth is the filter box's fixed width - wide enough to show the
// whole placeholder hint without truncation.
const queryWidth = 40

// defaultVisibleRows is how many grid rows show at once before scrolling.
const defaultVisibleRows = 4

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
	Footer      lipgloss.Style
	Placeholder lipgloss.Style
}

// DefaultStyles returns plain, theme-agnostic default styles - callers
// embedding this in a themed app will normally override Styles after New.
//
// CellPicked deliberately carries no text attributes (bold/underline/etc) -
// many terminals mis-measure or mis-render a wide emoji grapheme (e.g. a
// heart plus its U+FE0F emoji-presentation selector) when an SGR attribute
// like underline is applied on top of it, silently falling back to the
// narrow text-presentation glyph. renderCell instead marks a picked cell by
// bracketing the glyph, which can't have that failure mode.
func DefaultStyles() Styles {
	return Styles{
		Title:       lipgloss.NewStyle().Bold(true),
		Query:       lipgloss.NewStyle(),
		Cell:        lipgloss.NewStyle().Padding(0, 1),
		CellCursor:  lipgloss.NewStyle().Padding(0, 1).Reverse(true),
		CellPicked:  lipgloss.NewStyle().Padding(0, 1),
		PickedRow:   lipgloss.NewStyle(),
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

	query     textinput.Model
	cursor    int
	scrollRow int // index of the first grid row currently shown, follows cursor (see ensureCursorVisible)
	visible   []entry
	picked    []string // toggled emoji, in the order picked (Toggle), independent of visible/cursor - always shown in full via the "Picked:" row regardless of scroll/query
	touched   bool     // true once Toggle or ClearPicked has been used at least once, even if picked ended up empty again - see DidConfirm
	recent    []string
	common    []string
}

// New returns a ready-to-use Model. recent is this user's own most-used
// emoji, ranked best-first (may be nil/empty); it's shown ahead of the
// built-in common list until a search query narrows the grid.
func New(recent []string) Model {
	q := textinput.New()
	q.Placeholder = "type to search, e.g. fire, +1, idk..."
	q.Prompt = "🔍 "
	// Without an explicit width, textinput's placeholderView renders only
	// the placeholder's first rune (it allocates len(Placeholder)+1 runes
	// only when Width is set - unset defaults to 0, sizing the buffer to
	// just 1) - the placeholder would show as a single stray "t" instead of
	// the full hint text.
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

// Resize sets Columns/VisibleRows/Width (each left unchanged if <= 0),
// shrinks the query box to fit, and re-clamps the cursor/scroll to stay
// valid under the new grid shape - call this whenever the space available
// to render the picker changes (e.g. a terminal resize), not just once at
// construction, so the popup adapts instead of a fixed size that can
// overflow a small terminal.
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
func (m Model) Selection() []string {
	return m.picked
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return textinput.Blink
}

// Update implements tea.Model. Confirm/Cancel are deliberately no-ops here -
// callers check DidConfirm/DidCancel against the same msg right after
// calling Update (mirroring ui/filepicker's DidSelectFile convention) and
// close the picker themselves; Update never closes itself.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch {
		case key.Matches(keyMsg, m.KeyMap.Up):
			m.moveCursor(-m.Columns)
			return m, nil
		case key.Matches(keyMsg, m.KeyMap.Down):
			m.moveCursor(m.Columns)
			return m, nil
		case key.Matches(keyMsg, m.KeyMap.Left):
			m.moveCursor(-1)
			return m, nil
		case key.Matches(keyMsg, m.KeyMap.Right):
			m.moveCursor(1)
			return m, nil
		case key.Matches(keyMsg, m.KeyMap.Toggle):
			m.toggleCursor()
			return m, nil
		case key.Matches(keyMsg, m.KeyMap.ClearPicked):
			m.picked = nil
			m.touched = true
			return m, nil
		case key.Matches(keyMsg, m.KeyMap.Confirm), key.Matches(keyMsg, m.KeyMap.Cancel):
			return m, nil
		}
	}

	oldQuery := m.query.Value()
	var cmd tea.Cmd
	m.query, cmd = m.query.Update(msg)
	if m.query.Value() != oldQuery {
		m.cursor = 0
		m.refresh()
	}
	return m, cmd
}

// DidConfirm reports whether msg is the Confirm key, and if so the emoji set
// it confirms: the toggled multi-select set (including an explicitly
// cleared-to-empty one, once ClearPicked/Toggle has been touched at least
// once - see touched), or just the highlighted cell if the picker was never
// touched at all (so a single pick needs no Tab first).
func (m Model) DidConfirm(msg tea.Msg) ([]string, bool) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok || !key.Matches(keyMsg, m.KeyMap.Confirm) {
		return nil, false
	}
	if len(m.picked) > 0 || m.touched {
		return m.picked, true
	}
	if m.cursor >= 0 && m.cursor < len(m.visible) {
		return []string{m.visible[m.cursor].Emoji}, true
	}
	return []string{}, true
}

// DidCancel reports whether msg is the Cancel key.
func (m Model) DidCancel(msg tea.Msg) bool {
	keyMsg, ok := msg.(tea.KeyMsg)
	return ok && key.Matches(keyMsg, m.KeyMap.Cancel)
}

// moveCursor moves the cursor by delta cells (±1 for left/right, ±Columns
// for up/down), clamped to the visible list's bounds - no wraparound, since
// combined with scrolling that would be disorienting (jumping from the last
// match straight back to the first, several scroll pages away). Always
// re-follows the cursor's row into view afterward.
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
func (m Model) View() string {
	var b strings.Builder
	if m.Title != "" {
		b.WriteString(m.line(m.Styles.Title, m.Title))
		b.WriteString("\n\n")
	}
	b.WriteString(m.line(m.Styles.Query, m.query.View()))
	b.WriteString("\n\n")

	// Always shown, separate from the (possibly filtered) grid below - a
	// search query can easily hide cells that are already picked, and
	// without this line there'd be no way to see the full multi-select set
	// at all while it's narrowed out of view.
	b.WriteString(m.line(m.Styles.PickedRow, m.pickedRowText()))
	b.WriteString("\n\n")

	totalRows := 0
	if len(m.visible) > 0 && m.Columns > 0 {
		totalRows = (len(m.visible) + m.Columns - 1) / m.Columns
	}

	if len(m.visible) == 0 {
		b.WriteString(m.Styles.Placeholder.Render("no matches"))
		b.WriteString("\n")
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
			b.WriteString(strings.Join(row, ""))
			b.WriteString("\n")
		}
	}

	if totalRows > m.VisibleRows {
		b.WriteString(m.line(m.Styles.Footer, fmt.Sprintf("rows %d-%d of %d", min(m.scrollRow+1, totalRows), min(m.scrollRow+m.VisibleRows, totalRows), totalRows)))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(m.line(m.Styles.Footer, "↑↓←→ move · tab multi · ctrl+x clear · enter confirm · esc cancel"))
	return b.String()
}

// line truncates text to m.Width (with an ellipsis) before rendering it
// with style - keeps a single-line row (title, picked summary, footer) from
// ever overflowing the popup regardless of terminal size or how long a
// message preview/hint happens to be.
func (m Model) line(style lipgloss.Style, text string) string {
	if m.Width > 0 {
		text = ansi.Truncate(text, m.Width, "…")
	}
	return style.Render(text)
}

// pickedRowText renders the "Picked: ..." summary line's content.
func (m Model) pickedRowText() string {
	if len(m.picked) == 0 {
		return "Picked: (none)"
	}
	return "Picked: " + strings.Join(m.picked, " ")
}

func (m Model) renderCell(i int) string {
	e := m.visible[i].Emoji
	// Picked is marked by bracketing the glyph rather than a text attribute
	// (see DefaultStyles' CellPicked doc) - applied to the plain glyph
	// before any style wraps it, so cursor+picked together just brackets
	// inside the reversed cell instead of stacking attributes on the glyph.
	text := e
	if m.isPicked(e) {
		text = "[" + e + "]"
	}
	if i == m.cursor {
		return m.Styles.CellCursor.Render(text)
	}
	if m.isPicked(e) {
		return m.Styles.CellPicked.Render(text)
	}
	return m.Styles.Cell.Render(text)
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

	// Many shortcodes are aliases for the same glyph (":thumbsup:" and
	// ":+1:" both render 👍) - without deduping here, a query would surface
	// the same emoji as multiple grid cells, and since picking/highlighting
	// match by glyph (see isPicked/toggleCursor), toggling one would
	// visually light up its "duplicate" too. Keep only the
	// highest-scoring shortcode per glyph.
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
