package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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
	if m.contextMenu != nil {
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

	// Without such an item, n still closes.
	ran = ""
	m.openContextMenu("Plain", []contextMenuItem{{label: "A", run: func(*Model) tea.Cmd { ran = "A"; return nil }}})
	next, _ = m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = next.(Model)
	if ran != "" || m.contextMenu != nil {
		t.Fatalf("n in a menu without an n item: ran=%q open=%v", ran, m.contextMenu != nil)
	}
}

func TestContextMenuRowShowsShortcut(t *testing.T) {
	text := contextMenuRowText(contextMenuItem{label: "Mute", key: 'm'}, 20)
	if !strings.HasPrefix(text, "Mute") || !strings.HasSuffix(text, "m") || len(text) != 18 {
		t.Fatalf("row text %q: want label, padding, key, 18 wide", text)
	}
	if got := contextMenuRowText(contextMenuItem{label: "Copy"}, 20); got != "Copy" {
		t.Fatalf("row without a shortcut = %q, want the bare label", got)
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
	m.contextMenu.refresh = func(*Model) (string, []contextMenuItem) { return "y", items() }
	m.contextMenu.cursor = 2

	n = 2
	m.refreshContextMenu()
	if m.contextMenu.title != "y" || len(m.contextMenu.items) != 2 || m.contextMenu.cursor != 1 {
		t.Fatalf("after shrinking: title=%q items=%d cursor=%d", m.contextMenu.title, len(m.contextMenu.items), m.contextMenu.cursor)
	}

	n = 0
	m.refreshContextMenu()
	if m.contextMenu != nil {
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
		return m.renderContextMenuPopup()
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
