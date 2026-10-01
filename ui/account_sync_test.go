package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// An account another client added has to land in every client's list at the
// daemon's index, since indices are what every later RPC addresses.
func TestAccountAppearedAppends(t *testing.T) {
	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	m.accounts = []Account{{Name: "alice@example.test"}}

	next, _ := m.Update(AccountAppearedMsg{Index: 1, Account: Account{Name: "bob@example.test"}})
	m = next.(Model)
	if len(m.accounts) != 2 || m.accounts[1].Name != "bob@example.test" {
		t.Fatalf("accounts = %v, want alice then bob", accountNames(m))
	}
	if m.noticeText == "" {
		t.Error("no notification shown for an account added elsewhere")
	}
}

// Our own add arrives twice - as the RPC result and as the broadcast every
// client gets - in either order. Neither ordering may duplicate the row.
func TestAccountAddedIsIdempotent(t *testing.T) {
	for _, order := range []string{"result first", "broadcast first"} {
		t.Run(order, func(t *testing.T) {
			m := newTestModelWithSender(&fakeSuccessSender{}, nil)
			m.accounts = []Account{{Name: "alice@example.test"}}
			added := AccountAddedMsg{Account: Account{Name: "bob@example.test"}}
			appeared := AccountAppearedMsg{Index: 1, Account: Account{Name: "bob@example.test"}}

			var msgs []tea.Msg
			if order == "result first" {
				msgs = []tea.Msg{added, appeared}
			} else {
				msgs = []tea.Msg{appeared, added}
			}
			for _, msg := range msgs {
				next, _ := m.Update(msg)
				m = next.(Model)
			}

			if len(m.accounts) != 2 {
				t.Fatalf("accounts = %v, want exactly alice and bob", accountNames(m))
			}
			if m.addingAccount || m.addAccountBusy {
				t.Error("add-account form left open after the add completed")
			}
		})
	}
}

// A client whose list has drifted from the daemon's can't place the new
// account at an index that stays true, so it drops it rather than giving every
// later RPC a wrong index.
func TestAccountAppearedIndexMismatchDropped(t *testing.T) {
	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	m.accounts = []Account{{Name: "alice@example.test"}}

	next, _ := m.Update(AccountAppearedMsg{Index: 4, Account: Account{Name: "bob@example.test"}})
	m = next.(Model)
	if len(m.accounts) != 1 {
		t.Fatalf("accounts = %v, want just alice", accountNames(m))
	}
}

// The removing client sees both the RPC result and the broadcast; applying the
// second must not re-announce a removal it already handled.
func TestAccountRemovedIsIdempotent(t *testing.T) {
	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	m.accounts = []Account{{Name: "alice@example.test"}, {Name: "bob@example.test"}}

	next, _ := m.Update(AccountRemovedMsg{Index: 1})
	m = next.(Model)
	if !m.accounts[1].Removed {
		t.Fatal("account not marked removed")
	}
	firstNotice := m.noticeID

	next, _ = m.Update(AccountRemovedMsg{Index: 1})
	m = next.(Model)
	if !m.accounts[1].Removed {
		t.Error("account no longer marked removed after a repeat")
	}
	if m.noticeID != firstNotice {
		t.Error("repeat removal announced the removal again")
	}
	// Indices never shift - the row stays, flagged.
	if len(m.accounts) != 2 {
		t.Errorf("accounts = %v, want both rows kept", accountNames(m))
	}
}

func accountNames(m Model) []string {
	names := make([]string, len(m.accounts))
	for i, acct := range m.accounts {
		names[i] = acct.Name
	}
	return names
}
