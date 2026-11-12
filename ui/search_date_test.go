package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jim-ww/kage/ui/datepicker"
)

// TestSearchDateTabJumpsToStartOfDay drives the search popup's date tab:
// tab switches to it, enter on a day asks for the history window from that
// day's midnight on, and the reply lands on the day's first message.
func TestSearchDateTabJumpsToStartOfDay(t *testing.T) {
	day := time.Date(2026, time.June, 1, 0, 0, 0, 0, time.Local)
	window := make([]Message, 40)
	for i := range window {
		// 20 messages on 31 May, then 20 on 1 June from 09:00.
		at := day.Add(-12*time.Hour + time.Duration(i)*time.Minute)
		if i >= 20 {
			at = day.Add(9*time.Hour + time.Duration(i)*time.Minute)
		}
		window[i] = Message{ID: fmt.Sprintf("m%d", i), StoreID: int64(i + 1), Content: fmt.Sprintf("msg %d", i), SentAt: at}
	}
	loader := &fakeHistoryLoader{fakeSuccessSender: &fakeSuccessSender{}, response: HistoryWindowMsg{Messages: window, HasOlder: true, HasNewer: true}}
	m := newTestModelWithMessages(window[39:], &fakeHistorySearcher{})
	m.historyLoader = loader
	m.width, m.termHeight = 100, 30
	m.updateSizes()

	m.actionSearchChat()
	m = run(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.searchTab != searchTabDate {
		t.Fatal("tab didn't switch to the date tab")
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Mo Tu We Th Fr Sa Su") {
		t.Fatal("the date tab shows no calendar")
	}

	m.searchDate.SetCursor(day.Add(15 * time.Hour))
	next, cmd := m.updateSearchChatForm(tea.KeyPressMsg{Code: tea.KeyEnter})
	model := next.(Model)
	if cmd == nil {
		t.Fatal("enter on a day picked nothing")
	}
	picked, ok := cmd().(datepicker.PickedMsg)
	if !ok {
		t.Fatalf("got %T, want datepicker.PickedMsg", cmd())
	}

	model, cmd, _ = model.handleEventMsg(picked)
	if model.searchingChat {
		t.Fatal("the popup stayed open after picking a day")
	}
	if loader.lastAnchor == nil || loader.lastAnchor.Delay != day.Unix() || loader.lastAnchor.StoreID != 0 {
		t.Fatalf("anchor = %+v, want the start of 1 June", loader.lastAnchor)
	}

	model, _, _ = model.handleEventMsg(cmd())
	if model.selectedMsg != 20 {
		t.Fatalf("selectedMsg = %d, want 20, the first message of 1 June", model.selectedMsg)
	}
	if model.viewport.YOffset() != model.msgOffsets[20] {
		t.Fatalf("viewport at line %d, want the day's first message (line %d) at the top", model.viewport.YOffset(), model.msgOffsets[20])
	}
	if model.pendingDateJump != nil {
		t.Fatal("the jump is still pending after its window arrived")
	}
}
