package modal

import (
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// TestToggleAndChoiceBothWays checks a form's toggle and choice change by
// keyboard and by click, and that enter acts with their values.
func TestToggleAndChoiceBothWays(t *testing.T) {
	s, zm := newStack(80, 20)
	var acted [][]string
	toggle := NewToggle("Members only", false)
	choice := NewChoice("Addresses", "moderators", ChoiceOption{"moderators", "moderators"}, ChoiceOption{"anyone", "everyone"})
	form := NewForm("", NewInput("Room", ""), toggle, choice)
	s.Push(&Modal[*host]{Title: "Settings", Body: form, OnAct: func(_ *host, v any) tea.Cmd {
		acted = append(acted, v.([]string))
		return nil
	}})
	h := &host{}

	s.Update(h, tabKey)
	s.Update(h, spaceKey)
	if !toggle.On {
		t.Fatal("space didn't turn the toggle on")
	}
	if hints := form.Hints(); len(hints) != 3 || hints[1].Label != "toggle" {
		t.Errorf("hints on the toggle = %+v", hints)
	}
	s.Update(h, tabKey)
	s.Update(h, rightKey)
	if choice.Value() != "anyone" {
		t.Fatalf("→ chose %q", choice.Value())
	}
	if view := ansi.Strip(choice.View()); view != "Addresses: ‹ everyone ›" {
		t.Errorf("focused choice = %q", view)
	}
	s.Update(h, tea.KeyPressMsg{Code: tea.KeyLeft})
	s.Update(h, enterKey)
	if len(acted) != 1 || strings.Join(acted[0], ",") != "Room,true,moderators" {
		t.Fatalf("acted %v", acted)
	}

	// Clicks flip and cycle.
	s.Update(h, click(draw(t, s, zm, form.fieldID(1))))
	s.Update(h, click(draw(t, s, zm, form.fieldID(2))))
	if toggle.On || choice.Value() != "anyone" || form.Focused() != 2 {
		t.Errorf("after clicks: toggle %v, choice %q, focus %d", toggle.On, choice.Value(), form.Focused())
	}
}

// TestFormScrollsToTheFocus checks a form taller than the space keeps its
// focused field in view.
func TestFormScrollsToTheFocus(t *testing.T) {
	s, _ := newStack(60, 8) // a 6-row body
	var fields []Field
	for i := range 20 {
		fields = append(fields, NewToggle("option "+strconv.Itoa(i), false))
	}
	open(s, "Settings", NewForm("", fields...))
	for range 12 {
		s.Update(&host{}, tabKey)
	}
	if view := ansi.Strip(s.View()); !strings.Contains(view, "option 12") || strings.Contains(view, "option 0 ") {
		t.Fatalf("focused option 12 isn't in view:\n%s", view)
	}
}
