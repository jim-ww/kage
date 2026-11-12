package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// TestPopupsFitChatArea renders every text-heavy popup at a range of
// terminal widths and asserts none is wider than the chat area it's placed
// in. lipgloss.Place neither wraps nor clips an oversized child, so a popup
// whose content is wider than that box simply spills past the terminal's
// right edge — which is what the change-password dialog (a 78-column
// warning plus the popup's own 10 columns of border and padding) used to do.
func TestPopupsFitChatArea(t *testing.T) {
	popups := map[string]func(m *Model) string{
		"change password": func(m *Model) string {
			m.openChangePasswordPopup()
			formOf(*m, storagePasswordModalID).SetError("passwords don't match")
			return m.modals.View()
		},
		"add account": func(m *Model) string {
			m.openAddAccount()
			return m.modals.View()
		},
		"rename chat": func(m *Model) string {
			m.actionRenameChat()
			return m.modals.View()
		},
		"search chat": func(m *Model) string {
			m.actionSearchChat()
			return m.modals.View()
		},
		"confirm": func(m *Model) string {
			m.actionHideChat()
			return m.modals.View()
		},
	}

	for _, width := range []int{40, 60, 80, 100} {
		for name, render := range popups {
			chat := Chat{Name: "Bob", Address: "bob@example.test"}
			m := newTestModel(&fakeAccountAdder{})
			m.accounts = []Account{{Name: "me", Chats: []list.Item{chat}}}
			m.currentAccount = 0
			if cmd := m.chats.SetItems([]list.Item{chat}); cmd != nil {
				_ = cmd()
			}
			m.selectedView = viewChat
			m.width, m.height = width, 24
			m.updateSizes()

			for i, line := range strings.Split(render(&m), "\n") {
				if got := ansi.StringWidth(line); got > m.chatAreaWidth() {
					t.Errorf("%s popup at width %d: line %d is %d columns wide, chat area is %d",
						name, width, i, got, m.chatAreaWidth())
					break
				}
			}
		}
	}
}

// TestPopupFrameFitsTerminal guards popups' Place height: the popup area
// sits between the chat status line and the compose box, and padding a
// popup to more than that pushes the whole frame past the terminal's
// height.
func TestPopupFrameFitsTerminal(t *testing.T) {
	m := newMouseSweepBenchModel(30, 150)
	m.openContextMenu("Menu", []contextMenuItem{{label: "one"}, {label: "two"}})
	for _, size := range [][2]int{{120, 40}, {60, 16}} {
		next, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = next.(Model)
		if lines := strings.Count(m.View().Content, "\n") + 1; lines != size[1] {
			t.Errorf("%dx%d: frame is %d lines, want %d", size[0], size[1], lines, size[1])
		}
	}
}
