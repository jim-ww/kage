package ui

import (
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

// BenchmarkMouseSweepFullPipeline drives the full Update+View pipeline (not
// just handleMouseMotion in isolation) with a realistic sidebar (many
// chats) and an open chat with many messages, simulating a mouse sweeping
// up and down across message rows — the scenario reported as showing a
// visible lag trail behind the actual cursor position.
func BenchmarkMouseSweepFullPipeline(b *testing.B) {
	nChats := 30
	nMsgs := 150
	items := make([]list.Item, nChats)
	for i := range items {
		items[i] = Chat{Name: "chat", Address: "c" + string(rune('a'+i)) + "@example.com", LastMessage: "hey there, how's it going"}
	}
	msgs := make([]Message, nMsgs)
	base := time.Now()
	for i := range msgs {
		msgs[i] = Message{ID: string(rune(i)), Author: "bob", Content: "hello world this is a message with some more text to wrap around a bit, maybe a link too https://example.com/path", SentAt: base.Add(time.Duration(i) * time.Second)}
	}

	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	m.mouseEnabled = true
	m.accounts = []Account{{Chats: items, Messages: map[int][]Message{0: msgs}}}
	m.currentAccount = 0
	m.chats.SetItems(items)
	m.chats.Select(0)
	m.selectedView = viewChat
	m.selectedMsg = nMsgs - 1
	m.width, m.termHeight = 120, 40
	m.updateSizes()
	m.refreshViewport()
	m.viewport.GotoBottom()

	// Render once so the zone manager has real bounds to hit-test against.
	_ = m.View()

	// Collect real on-screen coordinates for a handful of message rows
	// (whatever's actually visible at the bottom of the loaded chat).
	var points []tea.Mouse
	for i := nMsgs - 20; i < nMsgs; i++ {
		z := m.zone.Get(zoneMessage(i))
		if z == nil {
			continue
		}
		points = append(points, tea.Mouse{X: z.StartX, Y: z.StartY})
	}
	if len(points) < 2 {
		b.Fatal("not enough visible message zones to sweep over — check viewport height/window setup")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// up
		for j := len(points) - 1; j >= 0; j-- {
			next, _ := m.Update(tea.MouseMotionMsg(points[j]))
			m = next.(Model)
			_ = m.View()
		}
		// down
		for j := 0; j < len(points); j++ {
			next, _ := m.Update(tea.MouseMotionMsg(points[j]))
			m = next.(Model)
			_ = m.View()
		}
	}
}
