// Package emojipicker is a self-contained Bubble Tea component for picking
// one or more emoji: a type-to-filter query box over a fixed-column grid,
// seeded with a caller-supplied "recent" ranking and falling back to a
// curated common-reaction list. It knows nothing about kage's UI/message
// types - callers seed it with plain strings and read plain strings back
// out (see Selection/DidConfirm) - so it can be dropped into any Bubble Tea
// program, not just this one.
package emojipicker

import (
	"sort"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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

// maxResults caps how many cells the grid ever shows at once, search or
// default - keeps the popup a fixed, predictable size instead of growing
// with the underlying shortcode table.
const maxResults = 32

// KeyMap is the picker's keybindings. Deliberately arrow-keys-only for
// navigation (not vim h/j/k/l) since the query box is always "focused" -
// any letter typed is a filter character, not a nav key.
type KeyMap struct {
	Up, Down, Left, Right key.Binding
	Toggle                key.Binding // add/remove the highlighted cell from the multi-select set without closing
	Confirm               key.Binding // close, returning the multi-select set (or just the highlighted cell if nothing was toggled)
	Cancel                key.Binding
}

// DefaultKeyMap returns the picker's default keybindings.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Up:      key.NewBinding(key.WithKeys("up"), key.WithHelp("↑", "up")),
		Down:    key.NewBinding(key.WithKeys("down"), key.WithHelp("↓", "down")),
		Left:    key.NewBinding(key.WithKeys("left"), key.WithHelp("←", "left")),
		Right:   key.NewBinding(key.WithKeys("right"), key.WithHelp("→", "right")),
		Toggle:  key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "toggle")),
		Confirm: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm")),
		Cancel:  key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
	}
}

// Styles are the lipgloss styles the picker renders with.
type Styles struct {
	Title       lipgloss.Style
	Query       lipgloss.Style
	Cell        lipgloss.Style
	CellCursor  lipgloss.Style
	CellPicked  lipgloss.Style
	Footer      lipgloss.Style
	Placeholder lipgloss.Style
}

// DefaultStyles returns plain, theme-agnostic default styles - callers
// embedding this in a themed app will normally override Styles after New.
func DefaultStyles() Styles {
	return Styles{
		Title:       lipgloss.NewStyle().Bold(true),
		Query:       lipgloss.NewStyle(),
		Cell:        lipgloss.NewStyle().Padding(0, 1),
		CellCursor:  lipgloss.NewStyle().Padding(0, 1).Reverse(true),
		CellPicked:  lipgloss.NewStyle().Padding(0, 1).Bold(true).Underline(true),
		Footer:      lipgloss.NewStyle().Faint(true),
		Placeholder: lipgloss.NewStyle().Faint(true),
	}
}

// Model is the emoji picker's Bubble Tea sub-model. Zero value isn't
// usable - construct with New.
type Model struct {
	KeyMap  KeyMap
	Styles  Styles
	Columns int // grid width in cells; rows follow from len(visible)

	Title string // e.g. "react to \"...\"" - shown above the query box; empty renders no title row

	query   textinput.Model
	cursor  int
	visible []entry
	picked  []string // toggled emoji, in the order picked (Toggle), independent of visible/cursor
	recent  []string
	common  []string
}

// New returns a ready-to-use Model. recent is this user's own most-used
// emoji, ranked best-first (may be nil/empty); it's shown ahead of the
// built-in common list until a search query narrows the grid.
func New(recent []string) Model {
	q := textinput.New()
	q.Placeholder = "type to search, e.g. fire, +1, idk..."
	q.Prompt = "🔍 "
	q.Focus()

	m := Model{
		KeyMap:  DefaultKeyMap(),
		Styles:  DefaultStyles(),
		Columns: 8,
		query:   q,
		recent:  recent,
		common:  commonEmoji,
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
// it confirms: the toggled multi-select set, or just the highlighted cell if
// nothing was toggled (so a single pick needs no Tab at all).
func (m Model) DidConfirm(msg tea.Msg) ([]string, bool) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok || !key.Matches(keyMsg, m.KeyMap.Confirm) {
		return nil, false
	}
	if len(m.picked) > 0 {
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

func (m *Model) moveCursor(delta int) {
	if len(m.visible) == 0 {
		return
	}
	n := len(m.visible)
	m.cursor = ((m.cursor+delta)%n + n) % n
}

func (m *Model) toggleCursor() {
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return
	}
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
		b.WriteString(m.Styles.Title.Render(m.Title))
		b.WriteString("\n\n")
	}
	b.WriteString(m.Styles.Query.Render(m.query.View()))
	b.WriteString("\n\n")

	if len(m.visible) == 0 {
		b.WriteString(m.Styles.Placeholder.Render("no matches"))
	} else {
		for i := 0; i < len(m.visible); i += m.Columns {
			end := min(i+m.Columns, len(m.visible))
			row := make([]string, 0, end-i)
			for j := i; j < end; j++ {
				row = append(row, m.renderCell(j))
			}
			b.WriteString(strings.Join(row, ""))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(m.Styles.Footer.Render("↑↓←→ move · tab toggle multi · enter confirm · esc cancel"))
	return b.String()
}

func (m Model) renderCell(i int) string {
	e := m.visible[i].Emoji
	style := m.Styles.Cell
	switch {
	case i == m.cursor && m.isPicked(e):
		style = m.Styles.CellCursor.Bold(true)
	case i == m.cursor:
		style = m.Styles.CellCursor
	case m.isPicked(e):
		style = m.Styles.CellPicked
	}
	return style.Render(e)
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

	n := min(len(ranked), maxResults)
	out := make([]entry, n)
	for i := range n {
		out[i] = entry{Shortcode: ranked[i].code, Emoji: emoji.Parse(ranked[i].code)}
	}
	return out
}
