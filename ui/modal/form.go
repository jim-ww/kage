package modal

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/lrstanley/bubblezone/v2"
)

// Form is text fields under a note: tab (or ↓) and shift+tab (or ↑) move
// between them, a click focuses one, and enter acts with every field's
// value ([]string, hidden ones included) - Act, not Submit, since what it
// starts usually takes a while: the embedder shows it with SetBusy, then
// closes the modal or shows SetError once the answer is in. While busy, the
// form takes no input and esc doesn't close it (see Busy).
type Form struct {
	Note   string
	Fields []*Input
	// SubmitLabel names what enter does in the hint row ("ok" if empty).
	SubmitLabel string
	Styles      FormStyles

	hidden []bool
	focus  int
	err    string
	status string
	width  int
	zone   *zone.Manager
	id     string
}

// FormStyles style a Form's note and its error and status lines.
type FormStyles struct {
	Note   lipgloss.Style
	Error  lipgloss.Style
	Status lipgloss.Style
}

// NewForm is a form of fields under note.
func NewForm(note string, fields ...*Input) *Form {
	return &Form{
		Note:   note,
		Fields: fields,
		Styles: FormStyles{
			Note:   lipgloss.NewStyle().Faint(true),
			Error:  lipgloss.NewStyle().Bold(true),
			Status: lipgloss.NewStyle().Italic(true),
		},
		hidden: make([]bool, len(fields)),
	}
}

// Values is every field's value, in order, hidden ones included.
func (f *Form) Values() []string {
	out := make([]string, len(f.Fields))
	for i, in := range f.Fields {
		out[i] = in.Model.Value()
	}
	return out
}

// Focused is the index of the field with focus.
func (f *Form) Focused() int { return f.focus }

// Err is the error shown under the fields, if any.
func (f *Form) Err() string { return f.err }

// SetError shows err under the fields ("" clears it).
func (f *Form) SetError(err string) { f.err = err }

// SetBusy shows status under the fields and takes no input until it's
// cleared with "".
func (f *Form) SetBusy(status string) {
	f.status = status
	if status != "" {
		f.err = ""
	}
}

// Busy reports whether the form is waiting on what it started: the stack
// doesn't close it then.
func (f *Form) Busy() bool { return f.status != "" }

// SetHidden hides or shows field i, moving focus off it if it's hidden.
func (f *Form) SetHidden(i int, hidden bool) tea.Cmd {
	f.hidden[i] = hidden
	if hidden && f.focus == i {
		return f.move(1)
	}
	return nil
}

// move focuses the next (dir 1) or previous (dir -1) shown field.
func (f *Form) move(dir int) tea.Cmd {
	for n, i := 0, f.focus; n < len(f.Fields); n++ {
		i = (i + dir + len(f.Fields)) % len(f.Fields)
		if !f.hidden[i] {
			return f.focusOn(i)
		}
	}
	return nil
}

func (f *Form) focusOn(i int) tea.Cmd {
	f.Fields[f.focus].Blur()
	f.focus = i
	return f.Fields[i].Focus()
}

func (f *Form) fieldID(i int) string { return f.id + "-field-" + strconv.Itoa(i) }

func (f *Form) Update(msg tea.Msg) (tea.Cmd, Outcome) {
	if f.Busy() {
		return nil, Continue
	}
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "tab", "down":
			return f.move(1), Continue
		case "shift+tab", "up":
			return f.move(-1), Continue
		case "enter":
			return nil, Act(f.Values())
		}
	case tea.MouseClickMsg:
		if f.zone != nil {
			for i := range f.Fields {
				if !f.hidden[i] && f.zone.Get(f.fieldID(i)).InBounds(msg) {
					return f.focusOn(i), Continue
				}
			}
		}
		return nil, Continue
	case tea.MouseMsg:
		return nil, Continue
	}
	var cmd tea.Cmd
	f.Fields[f.focus].Model, cmd = f.Fields[f.focus].Model.Update(msg)
	return cmd, Continue
}

func (f *Form) View() string {
	var lines []string
	if f.Note != "" {
		note := f.Note
		if f.width > 0 {
			note = lipgloss.NewStyle().Width(f.width).Render(note)
		}
		lines = append(lines, f.Styles.Note.Render(note), "")
	}
	for i, in := range f.Fields {
		if f.hidden[i] {
			continue
		}
		field := in.View()
		if f.zone != nil {
			field = f.zone.Mark(f.fieldID(i), field)
		}
		lines = append(lines, field)
	}
	switch {
	case f.status != "":
		lines = append(lines, "", f.Styles.Status.Render(f.status))
	case f.err != "":
		lines = append(lines, "", f.Styles.Error.Render(f.err))
	}
	return strings.Join(lines, "\n")
}

func (f *Form) SetSize(width, height int) {
	f.width = width
	for _, in := range f.Fields {
		in.SetSize(width, 1)
	}
}

func (f *Form) PreferredWidth() int { return 60 }

func (f *Form) Bind(z *zone.Manager, id string) { f.zone, f.id = z, id }

func (f *Form) Focus() tea.Cmd {
	if f.hidden[f.focus] {
		return f.move(1)
	}
	return f.Fields[f.focus].Focus()
}

func (f *Form) Blur() { f.Fields[f.focus].Blur() }

func (f *Form) Hints() []Hint {
	if f.Busy() {
		return nil
	}
	label := f.SubmitLabel
	if label == "" {
		label = "ok"
	}
	return []Hint{KeyHint(enterKey, label), KeyHint(tabKey, "next field")}
}

// NewPassword is an Input that doesn't show what's typed.
func NewPassword(placeholder string) *Input {
	in := NewInput("", placeholder)
	in.Model.EchoMode = textinput.EchoPassword
	return in
}
