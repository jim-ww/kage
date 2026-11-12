package modal

import (
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

var enterKey = tea.KeyPressMsg{Code: tea.KeyEnter}

// Input is a one-line text field: enter submits its text (a string).
// Model is the underlying textinput, for the embedder to configure (prompt,
// placeholder, key map, styles).
type Input struct {
	Model textinput.Model
	// SubmitLabel names what enter does in the hint row ("ok" if empty).
	SubmitLabel string
}

// NewInput is an Input holding value, with placeholder shown while empty.
func NewInput(value, placeholder string) *Input {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.SetValue(value)
	ti.CursorEnd()
	return &Input{Model: ti}
}

func (in *Input) Update(msg tea.Msg) (tea.Cmd, Outcome) {
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == enterKey.String() {
		return nil, Submit(in.Model.Value())
	}
	var cmd tea.Cmd
	in.Model, cmd = in.Model.Update(msg)
	return cmd, Continue
}

func (in *Input) View() string { return in.Model.View() }

func (in *Input) SetSize(width, _ int) {
	in.Model.SetWidth(max(1, width-len([]rune(in.Model.Prompt))-1))
}

// PreferredWidth leaves room to type: an input sized to its (empty) View
// would be a few columns wide.
func (in *Input) PreferredWidth() int { return 48 }

func (in *Input) Focus() tea.Cmd { return in.Model.Focus() }

func (in *Input) Blur() { in.Model.Blur() }

func (in *Input) Hints() []Hint {
	label := in.SubmitLabel
	if label == "" {
		label = "ok"
	}
	return []Hint{KeyHint(enterKey, label)}
}
