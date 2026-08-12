package ui

import (
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

// newMouseSweepBenchModel builds a Model with a realistic sidebar (many
// chats) and an open chat with many messages, shared by the mouse-sweep
// benchmarks below.
func newMouseSweepBenchModel(nChats, nMsgs int) Model {
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
	return m
}

// BenchmarkMouseSweepFullPipeline drives the full Update+View pipeline (not
// just handleMouseMotion in isolation), simulating a mouse sweeping up and
// down across message rows — the scenario reported as showing a visible
// lag trail behind the actual cursor position. This is the sidebar
// render cache's sweet spot: hovering messages never touches sidebar
// state, so it should hit on every frame.
func BenchmarkMouseSweepFullPipeline(b *testing.B) {
	m := newMouseSweepBenchModel(30, 150)
	_ = m.View() // populate zone bounds to hit-test against

	var points []tea.Mouse
	for i := 149 - 20; i < 150; i++ {
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
		for j := len(points) - 1; j >= 0; j-- {
			next, _ := m.Update(tea.MouseMotionMsg(points[j]))
			m = next.(Model)
			_ = m.View()
		}
		for j := 0; j < len(points); j++ {
			next, _ := m.Update(tea.MouseMotionMsg(points[j]))
			m = next.(Model)
			_ = m.View()
		}
	}
}

// BenchmarkMouseSweepSidebarFullPipeline mirrors
// BenchmarkMouseSweepFullPipeline but sweeps over chat-list rows instead of
// messages — the viewport frame cache's sweet spot: hovering the chat list
// never touches the open chat's message content, so renderViewportFrame
// should hit on every frame while renderSidebar (whose input includes the
// chat list's own hover-dependent styling) misses on every one.
func BenchmarkMouseSweepSidebarFullPipeline(b *testing.B) {
	m := newMouseSweepBenchModel(30, 150)
	m.selectedView = viewChats
	_ = m.View()

	var points []tea.Mouse
	for i := 0; i < 30; i++ {
		z := m.zone.Get(zoneChatItem(i))
		if z == nil {
			continue
		}
		points = append(points, tea.Mouse{X: z.StartX, Y: z.StartY})
	}
	if len(points) < 2 {
		b.Fatal("not enough visible chat-item zones to sweep over")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := len(points) - 1; j >= 0; j-- {
			next, _ := m.Update(tea.MouseMotionMsg(points[j]))
			m = next.(Model)
			_ = m.View()
		}
		for j := 0; j < len(points); j++ {
			next, _ := m.Update(tea.MouseMotionMsg(points[j]))
			m = next.(Model)
			_ = m.View()
		}
	}
}
