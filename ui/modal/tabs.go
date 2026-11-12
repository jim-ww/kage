package modal

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/lrstanley/bubblezone/v2"
)

var tabKey = tea.KeyPressMsg{Code: tea.KeyTab}

// Tab is one tab of a Tabs: its title and what it shows.
type Tab struct {
	Title string
	Body  Component
}

// TabResult is what a Tabs submits: which tab, and what its body
// submitted.
type TabResult struct {
	Tab   int
	Value any
}

// Tabs shows one of several components, picked by its title row: tab
// cycles them, and each title is clickable. It's as tall as its tallest
// tab, so switching never moves the titles out from under the pointer.
type Tabs struct {
	Tabs   []Tab
	Styles TabStyles

	active int
	hover  int
	zone   *zone.Manager
	id     string
	width  int
	height int
}

// TabStyles are the title row's styles.
type TabStyles struct {
	Active   lipgloss.Style
	Inactive lipgloss.Style
	Hover    lipgloss.Style
}

// NewTabs shows tabs, the first one first.
func NewTabs(tabs ...Tab) *Tabs {
	return &Tabs{
		Tabs: tabs,
		Styles: TabStyles{
			Active:   lipgloss.NewStyle().Bold(true).Underline(true),
			Inactive: lipgloss.NewStyle().Faint(true),
			Hover:    lipgloss.NewStyle().Reverse(true),
		},
		hover: -1,
	}
}

// Active is the index of the tab showing.
func (t *Tabs) Active() int { return t.active }

// Select shows tab i.
func (t *Tabs) Select(i int) tea.Cmd {
	if i < 0 || i >= len(t.Tabs) || i == t.active {
		return nil
	}
	if f, ok := t.Tabs[t.active].Body.(Focuser); ok {
		f.Blur()
	}
	t.active = i
	if f, ok := t.Tabs[i].Body.(Focuser); ok {
		return f.Focus()
	}
	return nil
}

func (t *Tabs) titleID(i int) string { return t.id + "-tab-" + strconv.Itoa(i) }

// bodyID is tab i's body's zone ID prefix - apart from its title's, since a
// body may mark its whole area with exactly the prefix it's given.
func (t *Tabs) bodyID(i int) string { return t.id + "-body-" + strconv.Itoa(i) }

func (t *Tabs) titleAt(msg tea.MouseMsg) int {
	if t.zone == nil {
		return -1
	}
	for i := range t.Tabs {
		if t.zone.Get(t.titleID(i)).InBounds(msg) {
			return i
		}
	}
	return -1
}

func (t *Tabs) Update(msg tea.Msg) (tea.Cmd, Outcome) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if msg.String() == tabKey.String() {
			return t.Select((t.active + 1) % len(t.Tabs)), Continue
		}
		if msg.String() == "shift+tab" {
			return t.Select((t.active + len(t.Tabs) - 1) % len(t.Tabs)), Continue
		}
	case tea.MouseClickMsg:
		if i := t.titleAt(msg); i >= 0 {
			return t.Select(i), Continue
		}
	case tea.MouseMotionMsg:
		t.hover = t.titleAt(msg)
	}
	cmd, outcome := t.Tabs[t.active].Body.Update(msg)
	if outcome.kind == submitted {
		outcome = Submit(TabResult{Tab: t.active, Value: outcome.value})
	}
	return cmd, outcome
}

func (t *Tabs) View() string {
	titles := make([]string, len(t.Tabs))
	for i, tab := range t.Tabs {
		style := t.Styles.Inactive
		switch {
		case i == t.active:
			style = t.Styles.Active
		case i == t.hover:
			style = t.Styles.Hover
		}
		titles[i] = style.Render(tab.Title)
		if t.zone != nil {
			titles[i] = t.zone.Mark(t.titleID(i), titles[i])
		}
	}
	body := t.Tabs[t.active].Body.View()
	if pad := t.tallest() - (strings.Count(body, "\n") + 1); pad > 0 {
		body += strings.Repeat("\n", pad)
	}
	return strings.Join(titles, "   ") + "\n" + body
}

// tallest is the height of the tallest tab's body.
func (t *Tabs) tallest() int {
	h := 0
	for _, tab := range t.Tabs {
		h = max(h, strings.Count(tab.Body.View(), "\n")+1)
	}
	return h
}

func (t *Tabs) SetSize(width, height int) {
	t.width, t.height = width, height
	for _, tab := range t.Tabs {
		if s, ok := tab.Body.(Sizer); ok {
			s.SetSize(width, max(1, height-1))
		}
	}
}

func (t *Tabs) PreferredWidth() int {
	w := 0
	for _, tab := range t.Tabs {
		if p, ok := tab.Body.(Widther); ok {
			w = max(w, p.PreferredWidth())
		} else {
			w = max(w, lipgloss.Width(tab.Body.View()))
		}
	}
	return w
}

func (t *Tabs) Bind(z *zone.Manager, id string) {
	t.zone, t.id = z, id
	for i, tab := range t.Tabs {
		if b, ok := tab.Body.(Binder); ok {
			b.Bind(z, t.bodyID(i))
		}
	}
}

func (t *Tabs) Focus() tea.Cmd {
	if f, ok := t.Tabs[t.active].Body.(Focuser); ok {
		return f.Focus()
	}
	return nil
}

func (t *Tabs) Blur() {
	if f, ok := t.Tabs[t.active].Body.(Focuser); ok {
		f.Blur()
	}
}

func (t *Tabs) Hints() []Hint {
	var hints []Hint
	if h, ok := t.Tabs[t.active].Body.(Hinter); ok {
		hints = h.Hints()
	}
	next := t.Tabs[(t.active+1)%len(t.Tabs)].Title
	return append(hints, KeyHint(tabKey, strings.ToLower(next)))
}
