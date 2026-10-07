package ui

import (
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
)

// TestAccountsSnapshotInstallsLateArrivingAccounts covers the startup path
// after first paint stopped waiting on the daemon: the program now starts
// with no accounts at all, so everything ui.New used to do with them has to
// happen again when the snapshot lands.
func TestAccountsSnapshotInstallsLateArrivingAccounts(t *testing.T) {
	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	m.width, m.termHeight = 100, 40
	m.updateSizes()
	if len(m.accounts) != 0 {
		t.Fatalf("model started with %d accounts, want 0", len(m.accounts))
	}

	older := Chat{Name: "old", Address: "old@x", LastActivity: time.Now().Add(-time.Hour)}
	newer := Chat{Name: "new", Address: "new@x", LastActivity: time.Now()}
	next := updated(t, m, AccountsSnapshotMsg{
		StartAccount: 1,
		Accounts: []Account{
			{Name: "first@x", Chats: []list.Item{}},
			{Name: "second@x", Chats: []list.Item{older, newer}},
		},
	})

	if len(next.accounts) != 2 {
		t.Fatalf("installed %d accounts, want 2", len(next.accounts))
	}
	if next.currentAccount != 1 {
		t.Errorf("currentAccount = %d, want the configured start account 1", next.currentAccount)
	}
	// Activity-sorted, like ui.New does - the daemon snapshot arrives in
	// roster order, which is not what the list should show.
	if got := next.accounts[1].Chats[0].(Chat).Address; got != "new@x" {
		t.Errorf("first chat = %q, want the most recently active", got)
	}
	// And the visible list must actually be showing them, not left empty.
	if got := len(next.chats.Items()); got != 2 {
		t.Errorf("chat list has %d items, want 2", got)
	}
}

// A hidden chat must not appear just because it arrived in the snapshot.
func TestAccountsSnapshotStripsHiddenChats(t *testing.T) {
	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	m.width, m.termHeight = 100, 40
	m.updateSizes()

	next := updated(t, m, AccountsSnapshotMsg{
		Accounts: []Account{{
			Name: "me@x",
			Chats: []list.Item{
				Chat{Name: "visible", Address: "visible@x"},
				Chat{Name: "hidden", Address: "hidden@x", Hidden: true},
			},
		}},
	})

	if next.chatIndexByAddress(0, "hidden@x") >= 0 {
		t.Error("a chat flagged Hidden in the snapshot was left in the list")
	}
	if next.chatIndexByAddress(0, "visible@x") < 0 {
		t.Error("the visible chat is missing")
	}
	if !next.isChatHidden(0, "hidden@x") {
		t.Error("the hidden chat was not stashed, so it can never be unhidden")
	}
}

// An out-of-range start account (config naming an account that no longer
// exists) must clamp rather than panic on first render.
func TestAccountsSnapshotClampsStartAccount(t *testing.T) {
	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	m.width, m.termHeight = 100, 40
	m.updateSizes()

	next := updated(t, m, AccountsSnapshotMsg{
		StartAccount: 7,
		Accounts:     []Account{{Name: "only@x", Chats: []list.Item{}}},
	})
	if next.currentAccount != 0 {
		t.Errorf("currentAccount = %d, want it clamped to 0", next.currentAccount)
	}
	_ = next.View()
}

// An empty snapshot (every account removed) must not leave stale state or
// panic when rendered.
func TestAccountsSnapshotEmptyIsSafe(t *testing.T) {
	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	m.width, m.termHeight = 100, 40
	m.updateSizes()

	next := updated(t, m, AccountsSnapshotMsg{})
	if len(next.accounts) != 0 {
		t.Errorf("accounts = %d, want 0", len(next.accounts))
	}
	_ = next.View()
}
