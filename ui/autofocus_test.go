package ui

import (
	"fmt"
	"testing"
	"time"
)

// autofocusModel is the open chat with 30 messages, scrolled to the top
// with the first one selected - well away from where a new one lands.
func autofocusModel(a Autofocus) Model {
	msgs := make([]Message, 30)
	for i := range msgs {
		msgs[i] = Message{ID: fmt.Sprintf("m%d", i), Author: "Bob", Content: "hi", SentAt: time.Now()}
	}
	m := newTestModelWithMessages(msgs, nil)
	m.autofocus = a
	m.selectedView = viewChat
	m.width, m.termHeight = 80, 20
	m.updateSizes()
	m.selectedMsg = 0
	m.refreshViewport()
	m.viewport.GotoTop()
	return m
}

func arrive(t *testing.T, m Model, isMe bool) Model {
	t.Helper()
	msg := incomingFrom("new", time.Now())
	msg.Message.IsMe = isMe
	next, _, handled := m.handleEventMsg(msg)
	if !handled {
		t.Fatal("IncomingMessageMsg not handled")
	}
	return next
}

func TestAutofocus(t *testing.T) {
	cases := []struct {
		autofocus Autofocus
		isMe      bool
		focused   bool
	}{
		{AutofocusOff, false, false},
		{AutofocusOff, true, false},
		{AutofocusOwn, true, true},
		{AutofocusOwn, false, false},
		{AutofocusOthers, false, true},
		{AutofocusOthers, true, false},
		{AutofocusAll, true, true},
		{AutofocusAll, false, true},
	}
	for _, c := range cases {
		m := arrive(t, autofocusModel(c.autofocus), c.isMe)
		newest := len(m.currentMessages()) - 1
		if got := m.selectedMsg == newest && m.viewport.AtBottom(); got != c.focused {
			t.Errorf("autofocus %q, isMe %v: focused = %v (selected %d of %d, at bottom %v), want %v",
				c.autofocus, c.isMe, got, m.selectedMsg, newest, m.viewport.AtBottom(), c.focused)
		}
		if !c.focused && m.selectedMsg != 0 {
			t.Errorf("autofocus %q, isMe %v: selection moved to %d", c.autofocus, c.isMe, m.selectedMsg)
		}
	}
}

func TestAutofocusOwnSelectsWhatISend(t *testing.T) {
	m := autofocusModel(AutofocusOwn)
	m.daemon = &fakeSuccessSender{}
	m.input.SetValue("hello")
	m.sendCurrentInput()
	if newest := len(m.currentMessages()) - 1; m.selectedMsg != newest {
		t.Fatalf("selected %d, want the message just sent (%d)", m.selectedMsg, newest)
	}
}
