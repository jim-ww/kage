package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// benchKeyNav moves the message selection up then down steps times each,
// rendering a frame after every key the way the program does.
func benchKeyNav(b *testing.B, nMsgs, steps int, view bool) {
	benchKeyNavSize(b, nMsgs, steps, view, 120, 40)
}

func benchKeyNavSize(b *testing.B, nMsgs, steps int, view bool, w, h int) {
	m := newMouseSweepBenchModel(30, nMsgs)
	m.width, m.termHeight = w, h
	m.updateSizes()
	m.refreshViewport()
	m.viewport.GotoBottom()
	_ = m.View()
	up := tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl}
	down := tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl}
	b.ResetTimer()
	for b.Loop() {
		for _, k := range []tea.KeyPressMsg{up, down} {
			for range steps {
				next, _ := m.Update(k)
				m = next.(Model)
				if view {
					_ = m.View()
				}
			}
		}
	}
}

// BenchmarkKeyNavFullPipeline is a held ctrl+k/ctrl+j through a long chat,
// Update and View per key.
func BenchmarkKeyNavFullPipeline(b *testing.B) { benchKeyNav(b, 200, 40, true) }

// BenchmarkKeyNavUpdateOnly is BenchmarkKeyNavFullPipeline without View.
func BenchmarkKeyNavUpdateOnly(b *testing.B) { benchKeyNav(b, 200, 40, false) }

// BenchmarkKeyNavFullPipelineLarge is BenchmarkKeyNavFullPipeline on a
// large terminal.
func BenchmarkKeyNavFullPipelineLarge(b *testing.B) { benchKeyNavSize(b, 200, 40, true, 300, 80) }
