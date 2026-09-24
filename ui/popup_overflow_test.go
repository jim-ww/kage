package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
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
			m.changePasswordState = m.newChangePasswordForm()
			m.changePasswordState.err = "passwords don't match"
			return m.renderChangePasswordPopup()
		},
		"add account": func(m *Model) string {
			m.addingAccount = true
			m.addAccountInputs = m.newAddAccountForm()
			return m.renderAddAccountPopup()
		},
		"rename chat": func(m *Model) string {
			m.actionRenameChat()
			return m.renderRenameChatPopup()
		},
		"search chat": func(m *Model) string {
			m.actionSearchChat()
			return m.renderSearchChatPopup()
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
