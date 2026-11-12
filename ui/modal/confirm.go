package modal

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Confirm is a yes/no question: Detail says what's being asked about, Yes
// submits true, No cancels. Both are in the hint row, so they're buttons
// too.
type Confirm struct {
	Detail string
	Yes    key.Binding
	No     key.Binding
	// Match decides whether a key press is a binding's (key.Matches if
	// nil) - for an embedder matching keys its own way, by physical key
	// position, say.
	Match func(tea.KeyMsg, key.Binding) bool

	width int
}

// NewConfirm asks about detail, y for yes and n for no.
func NewConfirm(detail string) *Confirm {
	return &Confirm{
		Detail: detail,
		Yes:    key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "yes")),
		No:     key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "no")),
	}
}

func (c *Confirm) Update(msg tea.Msg) (tea.Cmd, Outcome) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		match := c.Match
		if match == nil {
			match = func(k tea.KeyMsg, b key.Binding) bool { return key.Matches(k, b) }
		}
		switch {
		case match(k, c.Yes):
			return nil, Submit(true)
		case match(k, c.No):
			return nil, Cancel()
		}
	}
	return nil, Continue
}

func (c *Confirm) View() string {
	if c.width <= 0 {
		return c.Detail
	}
	return lipgloss.NewStyle().Width(c.width).Render(c.Detail)
}

func (c *Confirm) SetSize(width, _ int) { c.width = width }

func (c *Confirm) PreferredWidth() int { return min(lipgloss.Width(c.Detail), 56) }

func (c *Confirm) Hints() []Hint {
	return []Hint{bindingHint(c.Yes), bindingHint(c.No)}
}

// bindingHint is a Hint for b's first key, labeled with its help text.
func bindingHint(b key.Binding) Hint {
	return KeyHint(keyPress(b.Keys()[0]), b.Help().Desc)
}

// keyPress is the key press s (a key.Binding key string, like "y" or
// "enter") stands for - what a click on its hint sends.
func keyPress(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return enterKey
	case "esc":
		return escKey
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	}
	r := []rune(s)
	if len(r) == 1 {
		return tea.KeyPressMsg{Code: r[0], Text: s}
	}
	return tea.KeyPressMsg{Text: s}
}
