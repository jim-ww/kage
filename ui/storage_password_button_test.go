package ui

import (
	"strings"
	"testing"
)

// TestStoragePasswordButtonOnlyShowsInAccountsView checks the button (and
// its "[k]" text-mode label) renders only while the accounts panel is open,
// not in the chats/chat views.
func TestStoragePasswordButtonOnlyShowsInAccountsView(t *testing.T) {
	m := newTestModel(&fakeAccountAdder{})
	m.accounts = []Account{{Name: "me"}}

	m.selectedView = viewChats
	if strings.Contains(m.View().Content, "[k]") {
		t.Fatal("storage password button should not render outside the accounts view")
	}

	m.selectedView = viewAccounts
	if !strings.Contains(m.View().Content, "[k]") {
		t.Fatal("storage password button should render while the accounts view is open")
	}
}
