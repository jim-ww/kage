package ui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// The TUI is built with no accounts at all and runs that way until the
// daemon's snapshot lands - seconds, against a slow server (see runTUI).
// Every key the user can hit in that window reaches a model whose account
// and chat lists are empty, and several handlers index into them.
func TestNoAccountsSurvivesEveryKey(t *testing.T) {
	plain := []rune{tea.KeyEnter, tea.KeyEscape, tea.KeyTab, tea.KeyUp, tea.KeyDown,
		tea.KeyLeft, tea.KeyRight, tea.KeyPgUp, tea.KeyPgDown, tea.KeyHome, tea.KeyEnd,
		tea.KeySpace, tea.KeyBackspace, tea.KeyDelete}
	letters := []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ/?:0123456789")
	var msgs []tea.KeyMsg
	for _, c := range plain {
		msgs = append(msgs, tea.KeyPressMsg{Code: c})
		msgs = append(msgs, tea.KeyPressMsg{Code: c, Mod: tea.ModCtrl})
		msgs = append(msgs, tea.KeyPressMsg{Code: c, Mod: tea.ModShift})
		msgs = append(msgs, tea.KeyPressMsg{Code: c, Mod: tea.ModAlt})
	}
	for _, c := range letters {
		msgs = append(msgs, tea.KeyPressMsg{Code: c, BaseCode: c, Text: string(c)})
		msgs = append(msgs, tea.KeyPressMsg{Code: c, BaseCode: c, Mod: tea.ModCtrl})
		msgs = append(msgs, tea.KeyPressMsg{Code: c, BaseCode: c, Mod: tea.ModAlt})
	}
	for _, view := range []selectedView{viewAccounts, viewChats, viewChat} {
		for _, msg := range msgs {
			m := newTestModelWithSender(&fakeSuccessSender{}, nil)
			m.width, m.termHeight = 80, 24
			m.updateSizes()
			m.selectedView = view
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("view=%v msg=%#v panicked: %v", view, msg, r)
					}
				}()
				next, _ := m.Update(msg)
				_ = next.(Model).View()
			}()
		}
	}
}

// Same window, from the other side: daemon pushes do arrive before the
// snapshot (the daemon is already connected and its events are broadcast to
// every attached client), addressed by account index into lists this model
// doesn't have yet.
func TestNoAccountsSurvivesDaemonEvents(t *testing.T) {
	msgs := []tea.Msg{
		IncomingMessageMsg{AccountIdx: 0, From: "bob@x", Message: Message{ID: "m1", Content: "hi"}},
		ChatAddedMsg{AccountIdx: 0, Chat: Chat{Name: "bob", Address: "bob@x"}},
		ChatRemovedMsg{AccountIdx: 0, Address: "bob@x"},
		PresenceMsg{AccountIdx: 0, From: "bob@x", Presence: PresenceOnline},
		TypingMsg{AccountIdx: 0, From: "bob@x", Typing: true},
		MessageCorrectedMsg{AccountIdx: 0, From: "bob@x", ReplaceID: "m1", NewContent: "edited"},
		MessageRetractedMsg{AccountIdx: 0, From: "bob@x", RetractID: "m1"},
		MessageDeliveredMsg{AccountIdx: 0, From: "bob@x", MessageID: "m1"},
		MessageReactionsMsg{AccountIdx: 0, From: "bob@x", MessageID: "m1"},
		HistorySyncedMsg{AccountIdx: 0, From: "bob@x", Messages: []Message{{ID: "h1"}}},
		HistoryWindowMsg{AccountIdx: 0, From: "bob@x", Messages: []Message{{ID: "h1"}}},
		HistorySyncStartedMsg{AccountIdx: 0},
		HistorySyncFinishedMsg{AccountIdx: 0},
		AccountLiveMsg{Index: 0, NewChats: nil},
		AccountConnectErrorMsg{Index: 0, Err: errTestConnect},
		openPendingChatMsg{},
	}
	for _, msg := range msgs {
		m := newTestModelWithSender(&fakeSuccessSender{}, nil)
		m.width, m.termHeight = 80, 24
		m.updateSizes()
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("msg=%T panicked: %v", msg, r)
				}
			}()
			next, cmd := m.Update(msg)
			_ = next.(Model).View()
			drainCmds(cmd)
		}()
	}
}

var errTestConnect = errors.New("connect failed")

// A snapshot that never arrives must say so: with the model starting empty,
// a dropped snapshot otherwise looks exactly like a user with no configured
// accounts.
func TestFailedAccountsSnapshotIsReported(t *testing.T) {
	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	m.width, m.termHeight = 80, 24
	m.updateSizes()

	next, _ := m.Update(AccountsSnapshotMsg{Err: "daemon said no"})
	m = next.(Model)
	if !strings.Contains(m.noticeText, "daemon said no") {
		t.Fatalf("notice after a failed snapshot = %q, want it to name the error", m.noticeText)
	}
}
