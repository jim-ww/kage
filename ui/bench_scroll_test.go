package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// BenchmarkMouseWheelScroll drives the same Update path a real mouse wheel
// tick takes (handleMouseWheel -> viewport.Update, msgIndexAtOffset,
// refreshViewportSelection), scrolling up then down across a long history —
// the reported "scrolling up/down feels slow" scenario.
func BenchmarkMouseWheelScroll(b *testing.B) {
	m := newMouseSweepBenchModel(30, 150)
	_ = m.View()

	z := m.zone.Get(zoneMessage(149))
	for i := 148; z == nil && i >= 0; i-- {
		z = m.zone.Get(zoneMessage(i))
	}
	if z == nil {
		b.Fatal("no message zone visible — check viewport height/window setup")
	}
	wheelUp := tea.MouseWheelMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseWheelUp}
	wheelDown := tea.MouseWheelMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseWheelDown}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < 40; j++ {
			next, _ := m.Update(wheelUp)
			m = next.(Model)
		}
		for j := 0; j < 40; j++ {
			next, _ := m.Update(wheelDown)
			m = next.(Model)
		}
	}
}

// BenchmarkRenderMessagesWithOffsets isolates the full-chat re-render that
// refreshViewport falls back to whenever refreshViewportSelection can't
// splice in place (width change, first render, etc).
func BenchmarkRenderMessagesWithOffsets(b *testing.B) {
	m := newMouseSweepBenchModel(30, 150)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = m.renderMessagesWithOffsets()
	}
}

// BenchmarkRefreshViewportSelection isolates the per-tick cost handleMouseWheel
// actually pays on most scroll ticks: re-rendering just the two rows whose
// selection state changed and splicing them into the cached content.
func BenchmarkRefreshViewportSelection(b *testing.B) {
	m := newMouseSweepBenchModel(30, 150)
	m.refreshViewport()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		old := m.selectedMsg
		m.selectedMsg = 149 - (i % 150)
		m.refreshViewportSelection(old, m.selectedMsg)
	}
}

// BenchmarkRenderMessageSingle isolates one row's render cost — the unit
// renderMessagesWithOffsets/refreshViewportSelection call once per message.
func BenchmarkRenderMessageSingle(b *testing.B) {
	m := newMouseSweepBenchModel(30, 150)
	msgs := m.currentMessages()
	nameWidth := maxSenderNameWidth(msgs)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.renderMessage(msgs[75], 75, 100, msgs, nameWidth)
	}
}
