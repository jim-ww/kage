package ui

import (
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

// recordingFocusReporter captures every focus state the UI pushes to the
// daemon, so a test can assert on what the daemon would actually be told
// rather than on Model internals.
type recordingFocusReporter struct {
	calls []focusReport
}

type focusReport struct {
	accountJID  string
	chatAddress string
	focused     bool
}

func (r *recordingFocusReporter) SetFocusState(accountJID, chatAddress string, focused bool) error {
	r.calls = append(r.calls, focusReport{accountJID, chatAddress, focused})
	return nil
}

// newFocusTestModel builds a Model with one chat open and a recording focus
// reporter wired in.
func newFocusTestModel(t *testing.T) (Model, *recordingFocusReporter) {
	t.Helper()
	reporter := &recordingFocusReporter{}
	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	chat := Chat{Name: "bob", Address: "bob@example.com"}
	m.accounts = []Account{{
		Name:     "me@example.com",
		Chats:    []list.Item{chat},
		Messages: map[int][]Message{},
	}}
	m.currentAccount = 0
	m.chats.SetItems([]list.Item{chat})
	m.chats.Select(0)
	m.selectedView = viewChat
	m.focusReporter = reporter
	m.focused = true
	return m, reporter
}

// drainCmds runs cmd and every sub-command of a tea.BatchMsg it produces,
// which is what the real Program loop does and what makes the deferred
// SetFocusState closure actually fire - Update only returns commands, it
// never runs them. The idle timer is skipped: invoking it would block on
// notifyIdleTimeout's real 10-minute tick (see isIdleTimerCmd).
func drainCmds(cmd tea.Cmd) {
	if cmd == nil || isIdleTimerCmd(cmd) {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			drainCmds(c)
		}
	}
}

// TestGoingIdleIsReported is the regression for "non-focused state of app,
// with open chat doesnt send notifications".
//
// The state pushed to the daemon is focused && !idle, and the "old" value it
// was compared against used to be read *after* Update had already mutated
// m.idle - so an idle transition compared equal to itself and was never
// reported. That killed the idle fallback entirely, and the fallback is the
// only thing covering terminals that never send focus events (plain tmux
// among them): the daemon kept believing the open chat was on screen and
// suppressed every notification for it.
func TestGoingIdleIsReported(t *testing.T) {
	m, reporter := newFocusTestModel(t)

	// Activity first, so the model is in the focused+active state the real
	// one sits in and the idle timer is armed with a known generation.
	next, cmd := m.Update(keyText("x"))
	m = next.(Model)
	drainCmds(cmd)
	reporter.calls = nil

	// The timer firing with a matching generation is what "the user walked
	// away" looks like.
	next, cmd = m.Update(idleMsg{gen: m.idleGen})
	m = next.(Model)
	if !m.idle {
		t.Fatal("idleMsg with a matching generation did not mark the model idle")
	}
	drainCmds(cmd)

	if len(reporter.calls) == 0 {
		t.Fatal("going idle was never reported to the daemon; notifications for the open chat stay suppressed forever")
	}
	last := reporter.calls[len(reporter.calls)-1]
	if last.focused {
		t.Errorf("reported focused=%v, want false once idle", last.focused)
	}
	if last.chatAddress != "bob@example.com" {
		t.Errorf("reported chat %q, want the open chat", last.chatAddress)
	}
}

// Coming back from idle must be reported too, or notifications stay
// un-suppressed for a chat the user is now actually reading.
func TestReturningFromIdleIsReported(t *testing.T) {
	m, reporter := newFocusTestModel(t)

	next, cmd := m.Update(idleMsg{gen: m.idleGen})
	m = next.(Model)
	drainCmds(cmd)
	if !m.idle {
		t.Fatal("model did not go idle")
	}
	reporter.calls = nil

	next, cmd = m.Update(keyText("y"))
	m = next.(Model)
	if m.idle {
		t.Fatal("activity did not clear idle")
	}
	drainCmds(cmd)

	if len(reporter.calls) == 0 {
		t.Fatal("returning from idle was never reported to the daemon")
	}
	last := reporter.calls[len(reporter.calls)-1]
	if !last.focused {
		t.Errorf("reported focused=%v, want true once active again", last.focused)
	}
}

// A stale idle timer (activity has since rearmed it) must change nothing and
// report nothing.
func TestStaleIdleTimerReportsNothing(t *testing.T) {
	m, reporter := newFocusTestModel(t)
	reporter.calls = nil

	next, cmd := m.Update(idleMsg{gen: m.idleGen - 1})
	m = next.(Model)
	if m.idle {
		t.Error("a stale idle timer marked the model idle")
	}
	drainCmds(cmd)
	if len(reporter.calls) != 0 {
		t.Errorf("stale idle timer produced %d reports, want none", len(reporter.calls))
	}
}

// Terminal blur is the primary signal and must still be reported on its own.
func TestBlurIsReported(t *testing.T) {
	m, reporter := newFocusTestModel(t)
	reporter.calls = nil

	next, cmd := m.Update(tea.BlurMsg{})
	m = next.(Model)
	if m.focused {
		t.Fatal("BlurMsg did not clear focused")
	}
	drainCmds(cmd)

	if len(reporter.calls) == 0 {
		t.Fatal("blur was never reported to the daemon")
	}
	if last := reporter.calls[len(reporter.calls)-1]; last.focused {
		t.Errorf("reported focused=%v after blur, want false", last.focused)
	}
}
