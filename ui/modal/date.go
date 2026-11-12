package modal

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/lrstanley/bubblezone/v2"

	"github.com/jim-ww/kage/ui/datepicker"
)

// Date is a calendar: picking a day (enter, or a click) submits it, a
// time.Time at that day's midnight.
type Date struct {
	Picker datepicker.Model
	// SubmitLabel names what enter does in the hint row ("pick" if empty).
	SubmitLabel string

	width int
}

// NewDate is a calendar on picker's cursor day.
func NewDate(picker datepicker.Model) *Date { return &Date{Picker: picker} }

func (d *Date) Update(msg tea.Msg) (tea.Cmd, Outcome) {
	d.Picker, _ = d.Picker.Update(msg)
	if day, ok := d.Picker.Picked(); ok {
		return nil, Submit(day)
	}
	return nil, Continue
}

// View is the calendar, centered in the width it's given.
func (d *Date) View() string {
	return lipgloss.PlaceHorizontal(max(d.width, datepicker.Width), lipgloss.Center, d.Picker.View())
}

func (d *Date) SetSize(width, _ int) { d.width = width }

func (d *Date) PreferredWidth() int { return datepicker.Width }

func (d *Date) Bind(z *zone.Manager, id string) { d.Picker.Zone, d.Picker.ID = z, id }

func (d *Date) Hints() []Hint {
	label := d.SubmitLabel
	if label == "" {
		label = "pick"
	}
	km := d.Picker.KeyMap
	return []Hint{
		KeyHint(enterKey, label),
		bindingHint(km.Today),
		KeyHint(tea.KeyPressMsg{Code: tea.KeyPgUp}, "prev month"),
		KeyHint(tea.KeyPressMsg{Code: tea.KeyPgDown}, "next month"),
	}
}
