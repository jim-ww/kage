package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func accountMenuModel(t *testing.T) Model {
	t.Helper()
	m := newTestModelWithSender(&fakeAvatarPublisher{}, nil)
	m.accounts = []Account{{Name: "me"}}
	m.currentAccount = 0
	return m
}

// The menu exists so the account's actions don't require finding the
// accounts panel first — so it has to open from anywhere.
func TestAccountMenuOpensFromAnyView(t *testing.T) {
	for _, view := range []selectedView{viewChat, viewChats, viewAccounts} {
		m := accountMenuModel(t)
		m.selectedView = view

		next := updated(t, m, tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl})
		if next.contextMenu == nil {
			t.Errorf("view %v: account menu did not open", view)
			continue
		}
		labels := menuLabels(next.contextMenu)
		for _, want := range []string{"Avatar", "Contacts", "OMEMO devices"} {
			if !strings.Contains(labels, want) {
				t.Errorf("view %v: menu %q is missing %q", view, labels, want)
			}
		}
	}
}

// Every account-scoped action belongs in this one menu — that's the whole
// reason it exists.
func TestAccountMenuListsEveryAccountAction(t *testing.T) {
	m := accountMenuModel(t)
	if cmd := m.openAccountMenu(); cmd != nil {
		t.Fatalf("openAccountMenu returned a notification command: %v", cmd())
	}
	labels := menuLabels(m.contextMenu)
	for _, want := range []string{"Status", "Avatar", "Contacts", "OMEMO devices", "Make default", "Remove account"} {
		if !strings.Contains(labels, want) {
			t.Errorf("menu %q is missing %q", labels, want)
		}
	}
}

// Storage-password rotation is only offered when something can perform it.
func TestAccountMenuHidesStoragePasswordWithoutAChanger(t *testing.T) {
	m := accountMenuModel(t)
	m.openAccountMenu()
	if strings.Contains(menuLabels(m.contextMenu), "Storage password") {
		t.Error("menu offers storage-password rotation with nothing wired up to do it")
	}
}

func TestAccountMenuWithNoAccount(t *testing.T) {
	m := newTestModelWithSender(&fakeAvatarPublisher{}, nil)
	m.accounts = nil
	m.currentAccount = -1
	if cmd := m.openAccountMenu(); cmd == nil {
		t.Error("openAccountMenu with no account gave no feedback")
	}
	if m.contextMenu != nil {
		t.Error("account menu opened with no account")
	}
}

// The menu used to be mouse-only, which is fine for actions that all have
// their own keybinding and useless for a menu that is itself the binding.
func TestContextMenuKeyboardNavigation(t *testing.T) {
	m := accountMenuModel(t)
	m.openAccountMenu()
	n := len(m.contextMenu.items)
	if n < 3 {
		t.Fatalf("menu has %d items, too few to test navigation", n)
	}

	next := updated(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if next.contextMenu.cursor != 1 {
		t.Errorf("cursor = %d after one down, want 1", next.contextMenu.cursor)
	}
	next = updated(t, next, tea.KeyPressMsg{Code: tea.KeyUp})
	if next.contextMenu.cursor != 0 {
		t.Errorf("cursor = %d after down then up, want 0", next.contextMenu.cursor)
	}
	// Wraps rather than sticking at the ends.
	next = updated(t, next, tea.KeyPressMsg{Code: tea.KeyUp})
	if next.contextMenu.cursor != n-1 {
		t.Errorf("cursor = %d after up from the top, want %d", next.contextMenu.cursor, n-1)
	}
	next = updated(t, next, tea.KeyPressMsg{Code: tea.KeyDown})
	if next.contextMenu.cursor != 0 {
		t.Errorf("cursor = %d after down from the bottom, want 0", next.contextMenu.cursor)
	}

	if closed := updated(t, next, tea.KeyPressMsg{Code: tea.KeyEscape}); closed.contextMenu != nil {
		t.Error("esc did not close the menu")
	}
}

// Enter on "Avatar" must land in the avatar menu — and the context menu
// must not close that popup back down on its way out.
func TestContextMenuEnterRunsTheItem(t *testing.T) {
	m := accountMenuModel(t)
	m.openAccountMenu()
	idx := -1
	for i, item := range m.contextMenu.items {
		if item.label == "Avatar" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("no Avatar item")
	}
	m.contextMenu.cursor = idx

	next := updated(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if next.avatarMenu == nil {
		t.Fatal("enter on Avatar did not open the avatar menu")
	}
	if next.contextMenu != nil {
		t.Error("the account menu stayed open behind the avatar menu")
	}
}

// The keyboard cursor row renders highlighted, or the keybinding gives you
// a menu with no visible selection.
func TestContextMenuRendersCursor(t *testing.T) {
	m := accountMenuModel(t)
	m.width, m.height, m.termHeight = 120, 40, 40
	m.updateSizes()
	m.openAccountMenu()
	m.contextMenu.cursor = 1

	first := ansi.Strip(m.renderContextMenuPopup())
	m.contextMenu.cursor = 2
	second := ansi.Strip(m.renderContextMenuPopup())
	if first != second {
		t.Fatalf("stripped rendering changed with the cursor; highlight must be color only")
	}
	m.contextMenu.cursor = 1
	if m.renderContextMenuPopup() == func() string { m2 := m; m2.contextMenu.cursor = 2; return m2.renderContextMenuPopup() }() {
		t.Error("moving the cursor did not change the rendering at all")
	}
}

func menuLabels(menu *contextMenu) string {
	labels := make([]string, 0, len(menu.items))
	for _, item := range menu.items {
		labels = append(labels, item.label)
	}
	return strings.Join(labels, ", ")
}
