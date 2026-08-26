package main

import (
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/ui"
)

// recordingModel is a minimal tea.Model that reports every Msg it receives
// on a channel, so the test can observe what actually reached the program.
type recordingModel struct {
	received chan tea.Msg
}

func (m recordingModel) Init() tea.Cmd { return nil }
func (m recordingModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(tea.QuitMsg); ok {
		return m, tea.Quit
	}
	select {
	case m.received <- msg:
	default:
	}
	return m, nil
}
func (m recordingModel) View() tea.View { return tea.NewView("") }

// TestIPCClientDoesNotDropEventsBeforeProgramReady reproduces the bug where a
// contact's presence (or any other daemon event) that arrives while this
// process is still doing its synchronous startup (listAccounts, ui.New) -
// before tea.NewProgram/setProgram ever runs - used to be silently and
// permanently dropped by dispatch()'s `if c.program == nil { return }`
// guard. In practice this showed up as one account's contact staying stuck
// at PresenceOffline in another account's chat list even though it was
// actually online, because the presence push raced the UI process's own
// startup and got thrown away with nothing to ever resync it.
func TestIPCClientDoesNotDropEventsBeforeProgramReady(t *testing.T) {
	c := newIPCClient()

	// Simulate the daemon broadcasting a presence event before this
	// process has gotten around to constructing tea.Program - exactly the
	// window between newIPCClient()/ipc.Dial and client.setProgram(p) in
	// main().
	data, err := json.Marshal(ui.PresenceMsg{AccountIdx: 1, From: "alice@localhost", Presence: ui.PresenceOnline})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	c.handleEvent(ipc.Event{Kind: evPresence, Data: data})

	// Give dispatchLoop a moment to have pulled it off c.events and (before
	// the fix) dropped it.
	time.Sleep(50 * time.Millisecond)

	// Roomy enough that bubbletea's own startup traffic can't fill the buffer
	// and get our PresenceMsg dropped by the non-blocking send in Update.
	model := recordingModel{received: make(chan tea.Msg, 64)}
	p := tea.NewProgram(model, tea.WithoutRenderer(), tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutSignalHandler())
	go p.Run()
	defer p.Quit()

	c.setProgram(p)

	// bubbletea delivers its own startup messages (color profile, window
	// size, ...) to the model too, and which of those land - and in what
	// order relative to ours - is an implementation detail that changes
	// between versions. What this test is about is only whether the event
	// buffered before setProgram arrives at all, so scan past anything else
	// rather than asserting on whatever happens to be first.
	deadline := time.After(3 * time.Second)
	var seen []string
	for {
		select {
		case msg := <-model.received:
			pm, ok := msg.(ui.PresenceMsg)
			if !ok {
				seen = append(seen, fmt.Sprintf("%T", msg))
				continue
			}
			if pm.AccountIdx != 1 || pm.From != "alice@localhost" || pm.Presence != ui.PresenceOnline {
				t.Fatalf("got %+v, want {AccountIdx:1 From:alice@localhost Presence:PresenceOnline}", pm)
			}
			return
		case <-deadline:
			t.Fatalf("PresenceMsg buffered before setProgram was never delivered to the program (saw %v)", seen)
		}
	}
}
