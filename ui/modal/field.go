package modal

import (
	"strconv"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Field is one row of a Form: a text Input, a Toggle or a Choice. The
// form keeps tab, ↑↓ and enter; every other key goes to the focused field.
type Field interface {
	View() string
	// Value is the field's value as text: what's typed, "true"/"false",
	// the chosen option's Value.
	Value() string
	UpdateField(msg tea.Msg) tea.Cmd
	Focus() tea.Cmd
	Blur()
	SetSize(width, height int)
}

// Optional Field interfaces.
type (
	// Clicker does something on a click, beyond the focus the click gives
	// it: a toggle flips.
	Clicker interface{ Click() }
	// FieldHinter lists the keys a field takes while focused, for the
	// form's hint row.
	FieldHinter interface{ FieldHints() []Hint }
)

var (
	spaceKey = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	rightKey = tea.KeyPressMsg{Code: tea.KeyRight}
)

// FieldStyles style a Toggle's or Choice's row, by whether it has focus.
type FieldStyles struct {
	Blurred lipgloss.Style
	Focused lipgloss.Style
}

func defaultFieldStyles() FieldStyles {
	return FieldStyles{Focused: lipgloss.NewStyle().Bold(true)}
}

func (s FieldStyles) style(focused bool) lipgloss.Style {
	if focused {
		return s.Focused
	}
	return s.Blurred
}

// Toggle is an on/off field: "[x] Label". Space or a click flips it.
type Toggle struct {
	Label  string
	On     bool
	Styles FieldStyles

	focused bool
}

// NewToggle is a Toggle labeled label, on or off.
func NewToggle(label string, on bool) *Toggle {
	return &Toggle{Label: label, On: on, Styles: defaultFieldStyles()}
}

func (t *Toggle) View() string {
	box := "[ ]"
	if t.On {
		box = "[x]"
	}
	return t.Styles.style(t.focused).Render(box + " " + t.Label)
}

func (t *Toggle) Value() string { return strconv.FormatBool(t.On) }

func (t *Toggle) UpdateField(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == spaceKey.String() {
		t.On = !t.On
	}
	return nil
}

func (t *Toggle) Click()             { t.On = !t.On }
func (t *Toggle) Focus() tea.Cmd     { t.focused = true; return nil }
func (t *Toggle) Blur()              { t.focused = false }
func (t *Toggle) SetSize(int, int)   {}
func (t *Toggle) FieldHints() []Hint { return []Hint{KeyHint(spaceKey, "toggle")} }

// ChoiceOption is one of a Choice's options: Value is what's chosen,
// Label what's shown.
type ChoiceOption struct {
	Value, Label string
}

// Choice picks one of its options: "Label: ‹ option ›". ←→ (or h/l) and
// space move through them, as does a click.
type Choice struct {
	Label    string
	Options  []ChoiceOption
	Selected int
	Styles   FieldStyles

	focused bool
}

// NewChoice is a Choice labeled label with the option valued value
// selected, else the first.
func NewChoice(label, value string, options ...ChoiceOption) *Choice {
	c := &Choice{Label: label, Options: options, Styles: defaultFieldStyles()}
	for i, o := range options {
		if o.Value == value {
			c.Selected = i
		}
	}
	return c
}

func (c *Choice) View() string {
	option := ""
	if c.Selected < len(c.Options) {
		option = c.Options[c.Selected].Label
	}
	if c.focused {
		option = "‹ " + option + " ›"
	}
	return c.Styles.style(c.focused).Render(c.Label + ": " + option)
}

func (c *Choice) Value() string {
	if c.Selected < len(c.Options) {
		return c.Options[c.Selected].Value
	}
	return ""
}

func (c *Choice) move(dir int) {
	if n := len(c.Options); n > 0 {
		c.Selected = (c.Selected + dir + n) % n
	}
}

func (c *Choice) UpdateField(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "left", "h":
			c.move(-1)
		case "right", "l", spaceKey.String():
			c.move(1)
		}
	}
	return nil
}

func (c *Choice) Click()             { c.move(1) }
func (c *Choice) Focus() tea.Cmd     { c.focused = true; return nil }
func (c *Choice) Blur()              { c.focused = false }
func (c *Choice) SetSize(int, int)   {}
func (c *Choice) FieldHints() []Hint { return []Hint{KeyHint(rightKey, "change")} }
