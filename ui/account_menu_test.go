package ui

import (
	"fmt"
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
		if menuOf(next) == nil {
			t.Errorf("view %v: account menu did not open", view)
			continue
		}
		labels := menuLabels(menuOf(next))
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
	labels := menuLabels(menuOf(m))
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
	if strings.Contains(menuLabels(menuOf(m)), "Storage password") {
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
	if menuOf(m) != nil {
		t.Error("account menu opened with no account")
	}
}

// The menu used to be mouse-only, which is fine for actions that all have
// their own keybinding and useless for a menu that is itself the binding.
func TestContextMenuKeyboardNavigation(t *testing.T) {
	m := accountMenuModel(t)
	m.openAccountMenu()
	n := len(menuOf(m).items)
	if n < 3 {
		t.Fatalf("menu has %d items, too few to test navigation", n)
	}

	next := updated(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if menuOf(next).Cursor() != 1 {
		t.Errorf("cursor = %d after one down, want 1", menuOf(next).Cursor())
	}
	next = updated(t, next, tea.KeyPressMsg{Code: tea.KeyUp})
	if menuOf(next).Cursor() != 0 {
		t.Errorf("cursor = %d after down then up, want 0", menuOf(next).Cursor())
	}
	// Wraps rather than sticking at the ends.
	next = updated(t, next, tea.KeyPressMsg{Code: tea.KeyUp})
	if menuOf(next).Cursor() != n-1 {
		t.Errorf("cursor = %d after up from the top, want %d", menuOf(next).Cursor(), n-1)
	}
	next = updated(t, next, tea.KeyPressMsg{Code: tea.KeyDown})
	if menuOf(next).Cursor() != 0 {
		t.Errorf("cursor = %d after down from the bottom, want 0", menuOf(next).Cursor())
	}

	if closed := updated(t, next, tea.KeyPressMsg{Code: tea.KeyEscape}); menuOf(closed) != nil {
		t.Error("esc did not close the menu")
	}
}

// Enter on "Avatar" must land in the avatar menu — and the context menu
// must not close that popup back down on its way out.
func TestContextMenuEnterRunsTheItem(t *testing.T) {
	m := accountMenuModel(t)
	m.openAccountMenu()
	idx := -1
	for i, item := range menuOf(m).items {
		if item.label == "Avatar" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("no Avatar item")
	}
	menuOf(m).SetCursor(idx)

	next := updated(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if menuOf(next) == nil {
		t.Fatal("enter on Avatar closed the menus instead of opening the avatar one")
	}
	if menuTitle(next) != "Avatar" {
		t.Errorf("menu title = %q, want the avatar menu to have replaced the account one", menuTitle(next))
	}
}

// The keyboard cursor row renders highlighted, or the keybinding gives you
// a menu with no visible selection.
func TestContextMenuRendersCursor(t *testing.T) {
	m := accountMenuModel(t)
	m.width, m.height, m.termHeight = 120, 40, 40
	m.updateSizes()
	m.openAccountMenu()
	menuOf(m).SetCursor(1)

	first := m.modals.View()
	menuOf(m).SetCursor(2)
	second := m.modals.View()
	if ansi.Strip(first) != ansi.Strip(second) {
		t.Fatalf("stripped rendering changed with the cursor; highlight must be color only")
	}
	if first == second {
		t.Error("moving the cursor did not change the rendering at all")
	}
}

// menuOf is the open menu, or nil.
func menuOf(m Model) *contextMenuBody {
	if md := m.modals.Find(menuModalID); md != nil {
		return md.Body.(*contextMenuBody)
	}
	return nil
}

// menuTitle is the open menu's title, or "" if none is open.
func menuTitle(m Model) string {
	if md := m.modals.Find(menuModalID); md != nil {
		return md.Title
	}
	return ""
}

func menuLabels(menu *contextMenuBody) string {
	labels := make([]string, 0, len(menu.items))
	for _, item := range menu.items {
		labels = append(labels, item.label)
	}
	return strings.Join(labels, ", ")
}

// Hovering the account bar swaps in a wider name (it gains the
// panel-toggle arrow), which must not move the menu button beside it —
// a control that slides away as you reach for it is unusable. Measured
// through View, since the bug was in how the two were laid out against
// each other there.
func TestAccountBarHoverDoesNotMoveTheMenuButton(t *testing.T) {
	m := accountMenuModel(t)
	m.width, m.height, m.termHeight = 100, 24, 24
	m.updateSizes()

	buttonColumn := func(hoverZone string) int {
		t.Helper()
		m.hover.id = hoverZone
		first := strings.Split(ansi.Strip(fmt.Sprint(m.View())), "\n")[0]
		// The button is the only "..." on the account bar (icons are off
		// in a DisplayOptions{} model, so it renders as dots).
		at := strings.Index(first, "...")
		if at < 0 {
			t.Fatalf("no account-menu button on the account bar: %q", first)
		}
		// Columns, not bytes: the panel-toggle arrow this is guarding
		// against is a multi-byte, single-column rune, so a byte offset
		// would report a shift that isn't there.
		return ansi.StringWidth(first[:at])
	}

	plain := buttonColumn("")
	for _, zone := range []string{zoneAccountBarName, zoneAccountMenuButton} {
		if got := buttonColumn(zone); got != plain {
			t.Errorf("hovering %s puts the menu button at column %d, unhovered it is at %d", zone, got, plain)
		}
	}

	m.hover.id = ""
	m.selectedView = viewAccounts // the bar shows the open-panel arrow too
	if got := buttonColumn(""); got != plain {
		t.Errorf("with the accounts panel open the menu button is at column %d, want %d", got, plain)
	}
}

// A menu opened from a keybinding or a toolbar button — rather than by
// right-clicking the thing itself — has to say what it is about to act on.
func TestAccountMenuIsTitledWithTheAccount(t *testing.T) {
	m := accountMenuModel(t)
	m.accounts = []Account{{Name: "me@movim.eu"}}
	m.width, m.height, m.termHeight = 120, 40, 40
	m.updateSizes()
	m.openAccountMenu()

	want := m.accounts[0].DisplayName()
	if menuTitle(m) != want {
		t.Errorf("menu title = %q, want %q", menuTitle(m), want)
	}
	rendered := ansi.Strip(m.modals.View())
	if !strings.Contains(rendered, want) {
		t.Errorf("rendered menu does not show %q:\n%s", want, rendered)
	}
	if strings.Contains(rendered, contextMenuDefaultTitle) {
		t.Errorf("rendered menu still shows the generic title %q", contextMenuDefaultTitle)
	}
}

// A menu with nothing specific to name still gets a heading rather than a
// blank line where one used to be.
func TestContextMenuFallsBackToGenericTitle(t *testing.T) {
	m := accountMenuModel(t)
	m.width, m.height, m.termHeight = 120, 40, 40
	m.updateSizes()
	m.openContextMenu("", []contextMenuItem{{label: "Something"}})

	if !strings.Contains(ansi.Strip(m.modals.View()), contextMenuDefaultTitle) {
		t.Errorf("untitled menu lost its heading")
	}
}

// TestAliasedAccountShowsJID checks an alias doesn't hide which account it
// is everywhere: the account menu's title and the accounts list carry the
// JID too.
func TestAliasedAccountShowsJID(t *testing.T) {
	m := newTestModel(nil)
	m.accounts = []Account{{Name: "me@example.org", Alias: "Work"}}
	m.currentAccount = 0
	m.openAccountMenu()
	if menuOf(m) == nil || menuTitle(m) != "Work · me@example.org" {
		t.Fatalf("account menu title: %+v", menuOf(m))
	}
	if list := m.renderAccountsList(80); !strings.Contains(list, "Work · me@example.org") {
		t.Fatalf("accounts list doesn't show the JID: %q", list)
	}
	if got := (Account{Name: "me@example.org"}).NameWithJID(); got != "me@example.org" {
		t.Fatalf("unaliased: %q", got)
	}
}
