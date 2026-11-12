package modal

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/lrstanley/bubblezone/v2"
)

// MenuItem is one entry of a Menu. Key, if set, picks it straight away and
// is shown at the end of its row.
type MenuItem struct {
	Label string
	Key   rune
}

// Menu is a list to pick one entry from: ↑↓ (or k/j) and enter, an item's
// own key, or a click. It submits the picked item's index. Longer than the
// space it has, it scrolls, keeping the cursor in view.
type Menu struct {
	Items  []MenuItem
	Styles MenuStyles
	// MatchKey decides whether a key press is r (the press's text if nil)
	// - for an embedder matching letters its own way, by physical key
	// position, say.
	MatchKey func(msg tea.KeyMsg, r rune) bool
	// ActOnPick makes picking an item act with its index (see Act) and
	// keep the menu open, rather than submit it: a list whose items have
	// actions of their own, like opening a menu of options for one.
	ActOnPick bool
	// Empty is shown when there are no items.
	Empty string

	cursor int
	hover  int
	scroll int
	width  int
	height int
	zone   *zone.Manager
	id     string
}

// MenuStyles style a Menu's rows.
type MenuStyles struct {
	Item   lipgloss.Style
	Cursor lipgloss.Style
	Hover  lipgloss.Style
	Key    lipgloss.Style
}

// NewMenu lists items, the cursor on the first.
func NewMenu(items ...MenuItem) *Menu {
	return &Menu{
		Items: items,
		Styles: MenuStyles{
			Item:   lipgloss.NewStyle(),
			Cursor: lipgloss.NewStyle().Reverse(true),
			Hover:  lipgloss.NewStyle().Underline(true),
			Key:    lipgloss.NewStyle().Faint(true),
		},
		hover: -1,
	}
}

// Cursor is the index of the item under the keyboard cursor.
func (m *Menu) Cursor() int { return m.cursor }

// SetCursor puts the keyboard cursor on item i.
func (m *Menu) SetCursor(i int) {
	m.cursor = max(0, min(i, len(m.Items)-1))
	m.show(m.cursor)
}

// SetItems replaces the items - a menu that follows something changing
// while it's open - keeping the cursor in range.
func (m *Menu) SetItems(items ...MenuItem) {
	m.Items = items
	m.cursor = max(0, min(m.cursor, len(items)-1))
	m.show(m.cursor)
}

func (m *Menu) visible() int {
	if m.height <= 0 {
		return len(m.Items)
	}
	return m.height
}

// show scrolls item i into view.
func (m *Menu) show(i int) {
	if i < m.scroll {
		m.scroll = i
	} else if i >= m.scroll+m.visible() {
		m.scroll = i - m.visible() + 1
	}
	m.scroll = max(0, min(m.scroll, len(m.Items)-m.visible()))
}

func (m *Menu) itemID(i int) string { return m.id + "-item-" + strconv.Itoa(i) }

func (m *Menu) itemAt(msg tea.MouseMsg) int {
	if m.zone == nil {
		return -1
	}
	for i := m.scroll; i < min(len(m.Items), m.scroll+m.visible()); i++ {
		if m.zone.Get(m.itemID(i)).InBounds(msg) {
			return i
		}
	}
	return -1
}

func (m *Menu) matches(msg tea.KeyMsg, r rune) bool {
	if m.MatchKey != nil {
		return m.MatchKey(msg, r)
	}
	return msg.String() == string(r)
}

// pick is the outcome of picking item i.
func (m *Menu) pick(i int) Outcome {
	if m.ActOnPick {
		return Act(i)
	}
	return Submit(i)
}

func (m *Menu) Update(msg tea.Msg) (tea.Cmd, Outcome) {
	if len(m.Items) == 0 {
		return nil, Continue
	}
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		// An item's own key first: it may be a letter the cursor keys use.
		for i, item := range m.Items {
			if item.Key != 0 && m.matches(msg, item.Key) {
				return nil, m.pick(i)
			}
		}
		switch msg.String() {
		case "up", "k":
			m.cursor = (m.cursor - 1 + len(m.Items)) % len(m.Items)
			m.show(m.cursor)
		case "down", "j":
			m.cursor = (m.cursor + 1) % len(m.Items)
			m.show(m.cursor)
		case "enter":
			return nil, m.pick(m.cursor)
		}
	case tea.MouseMotionMsg:
		m.hover = m.itemAt(msg)
	case tea.MouseClickMsg:
		if i := m.itemAt(msg); i >= 0 && msg.Button == tea.MouseLeft {
			m.cursor = i
			return nil, m.pick(i)
		}
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.scroll = max(0, m.scroll-1)
		case tea.MouseWheelDown:
			m.scroll = max(0, min(m.scroll+1, len(m.Items)-m.visible()))
		}
	}
	return nil, Continue
}

func (m *Menu) View() string {
	if len(m.Items) == 0 {
		return m.Styles.Key.Render(m.Empty)
	}
	width := m.width
	if width <= 0 {
		width = m.PreferredWidth()
	}
	end := min(len(m.Items), m.scroll+m.visible())
	rows := make([]string, 0, end-m.scroll)
	for i := m.scroll; i < end; i++ {
		item := m.Items[i]
		key := ""
		if item.Key != 0 {
			key = string(item.Key)
		}
		label := item.Label
		if room := width - lipgloss.Width(key) - 1; lipgloss.Width(label) > room {
			label = truncate(label, max(1, room))
		}
		gap := max(1, width-lipgloss.Width(label)-lipgloss.Width(key))
		style := m.Styles.Item
		switch {
		case i == m.cursor:
			style = m.Styles.Cursor
		case i == m.hover:
			style = m.Styles.Hover
		}
		row := style.Render(label+strings.Repeat(" ", gap)) + m.Styles.Key.Render(key)
		if i == m.cursor {
			row = style.Render(label + strings.Repeat(" ", gap) + key)
		}
		if m.zone != nil {
			row = m.zone.Mark(m.itemID(i), row)
		}
		rows = append(rows, row)
	}
	return strings.Join(rows, "\n")
}

func (m *Menu) SetSize(width, height int) {
	m.width, m.height = width, height
	m.show(m.cursor)
}

// PreferredWidth fits the longest label with its key.
func (m *Menu) PreferredWidth() int {
	w := 0
	for _, item := range m.Items {
		w = max(w, lipgloss.Width(item.Label)+3)
	}
	return w
}

func (m *Menu) Bind(z *zone.Manager, id string) { m.zone, m.id = z, id }

func (m *Menu) Hints() []Hint {
	return []Hint{KeyHint(enterKey, "select")}
}
