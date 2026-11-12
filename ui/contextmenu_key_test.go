package ui

import (
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// shortcutMenu opens a menu whose items record which one ran into *ran.
func shortcutMenu(m *Model, ran *string) {
	item := func(label string, key rune) contextMenuItem {
		return contextMenuItem{label: label, key: key, run: func(*Model) tea.Cmd { *ran = label; return nil }}
	}
	m.openContextMenu("Test", []contextMenuItem{item("First", 0), item("Mute", 'm'), item("Reject", 'n')})
}

func TestContextMenuShortcutRunsItem(t *testing.T) {
	m := newTestModel(nil)
	var ran string
	shortcutMenu(&m, &ran)

	next, _ := m.Update(tea.KeyPressMsg{Code: 'm', Text: "m"})
	m = next.(Model)
	if ran != "Mute" {
		t.Fatalf("m ran %q, want Mute", ran)
	}
	if menuOf(m) != nil {
		t.Fatal("menu still open after running an item by its shortcut")
	}
}

// TestContextMenuShortcutBeatsConfirmNo checks an item bound to a ConfirmNo
// letter runs instead of the menu just closing.
func TestContextMenuShortcutBeatsConfirmNo(t *testing.T) {
	m := newTestModel(nil)
	var ran string
	shortcutMenu(&m, &ran)

	next, _ := m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = next.(Model)
	if ran != "Reject" {
		t.Fatalf("n ran %q, want Reject", ran)
	}

	// Without such an item, n is just a key the menu has no use for.
	ran = ""
	m.openContextMenu("Plain", []contextMenuItem{{label: "A", run: func(*Model) tea.Cmd { ran = "A"; return nil }}})
	next, _ = m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = next.(Model)
	if ran != "" || menuOf(m) == nil {
		t.Fatalf("n in a menu without an n item: ran=%q open=%v, want nothing run and the menu still open", ran, menuOf(m) != nil)
	}
}

func TestContextMenuRowShowsShortcut(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.termHeight = 80, 24
	m.updateSizes()
	m.openContextMenu("x", []contextMenuItem{{label: "Mute", key: 'm'}, {label: "Copy"}})
	view := ansi.Strip(m.modals.View())
	if !regexp.MustCompile(`Mute +m`).MatchString(view) {
		t.Fatalf("Mute's row doesn't end in its key:\n%s", view)
	}
	if regexp.MustCompile(`Copy +[a-z] `).MatchString(view) {
		t.Fatalf("Copy has no key but shows one:\n%s", view)
	}
}

func TestRefreshContextMenu(t *testing.T) {
	m := newTestModel(nil)
	n := 3
	items := func() []contextMenuItem {
		out := make([]contextMenuItem, n)
		for i := range out {
			out[i] = contextMenuItem{label: string(rune('a' + i)), run: func(*Model) tea.Cmd { return nil }}
		}
		return out
	}
	m.openContextMenu("x", items())
	menuOf(m).refresh = func(*Model) (string, []contextMenuItem) { return "y", items() }
	menuOf(m).SetCursor(2)

	n = 2
	m.refreshContextMenu()
	if menuTitle(m) != "y" || len(menuOf(m).items) != 2 || menuOf(m).Cursor() != 1 {
		t.Fatalf("after shrinking: title=%q items=%d cursor=%d", menuTitle(m), len(menuOf(m).items), menuOf(m).Cursor())
	}

	n = 0
	m.refreshContextMenu()
	if menuOf(m) != nil {
		t.Fatal("menu with nothing left in it stayed open")
	}
}

// TestContextMenuRowsNeverWrap checks a label longer than the popup can
// fit is cut to one line rather than wrapped onto a second - and that the
// longest label that does fit isn't cut at all.
func TestContextMenuRowsNeverWrap(t *testing.T) {
	m := newTestModel(nil)
	open := func(label string) string {
		m.openContextMenu("x", []contextMenuItem{
			{label: label, run: func(*Model) tea.Cmd { return nil }},
			{label: "Short", key: 's', run: func(*Model) tea.Cmd { return nil }},
		})
		return m.modals.View()
	}
	short := open("Copy")
	long := open("start " + strings.Repeat("x", 300) + " end")
	if lipgloss.Height(long) != lipgloss.Height(short) || strings.Contains(long, " end") {
		t.Fatalf("long label wrapped:\n%s", long)
	}
	fits := "Default ALSA Output (currently PipeWire)"
	if popup := open(fits); !strings.Contains(popup, fits) || lipgloss.Height(popup) != lipgloss.Height(short) {
		t.Fatalf("label that fits got cut or wrapped:\n%s", popup)
	}
}
