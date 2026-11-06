package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// newFilePickerBenchModel opens the attach-file picker over a directory of
// nFiles files, inside the same realistic sidebar/chat model the mouse-sweep
// benchmarks use.
func newFilePickerBenchModel(b testing.TB, nFiles int) Model {
	b.Helper()
	dir := b.TempDir()
	for i := range nFiles {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file-%05d-some-longer-name.pdf", i)), []byte("x"), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	m := newMouseSweepBenchModel(30, 150)
	m.filePicker.CurrentDirectory = dir
	m.pickingFile = true
	m.filePicker.SetHeight(max(1, m.height-m.inputAreaHeight()-6))
	next, _ := m.Update(m.filePicker.Init()())
	return next.(Model)
}

// filePickerSweepPoints returns one pointer position per visible picker row.
func filePickerSweepPoints(b testing.TB, m *Model) []tea.Mouse {
	b.Helper()
	for attempt := range 100 {
		_ = m.View()
		var points []tea.Mouse
		for i := range m.filePicker.Height() {
			z := m.zone.Get(zoneFilePickerRow(i))
			if z == nil || z.IsZero() {
				continue
			}
			points = append(points, tea.Mouse{X: z.StartX + 5, Y: z.StartY})
		}
		if len(points) >= 10 {
			return points
		}
		if attempt > 0 {
			time.Sleep(time.Millisecond)
		}
	}
	b.Fatal("not enough visible file picker rows")
	return nil
}

// drain runs cmd and feeds every message it yields back through Update+View,
// the way the tea.Program would (tea.Sequence included).
func drain(b *testing.B, m Model, cmd tea.Cmd) Model {
	if cmd == nil {
		return m
	}
	// Timers (the idle tick every Update schedules) would block for
	// seconds; a cmd that doesn't return promptly isn't part of the
	// cursor-move work, and waiting it out isn't counted.
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	b.StopTimer()
	select {
	case msg = <-done:
	case <-time.After(5 * time.Millisecond):
		b.StartTimer()
		return m
	}
	b.StartTimer()
	switch msg := msg.(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range msg {
			m = drain(b, m, c)
		}
	default:
		if seq, ok := sequenceCmds(msg); ok {
			for _, c := range seq {
				m = drain(b, m, c)
			}
			return m
		}
		next, c := m.Update(msg)
		m = next.(Model)
		_ = m.View()
		m = drain(b, m, c)
	}
	return m
}

// BenchmarkFilePickerKeyDown is one j/k press in the open picker through the
// whole app's Update+View.
func BenchmarkFilePickerKeyDown(b *testing.B) {
	m := newFilePickerBenchModel(b, 2000)
	down := tea.KeyPressMsg{Code: tea.KeyDown}
	up := tea.KeyPressMsg{Code: tea.KeyUp}
	_ = m.View()
	b.ResetTimer()
	for i := range b.N {
		k := down
		if (i/100)%2 == 1 {
			k = up
		}
		next, cmd := m.Update(k)
		m = next.(Model)
		_ = m.View()
		m = drain(b, m, cmd)
	}
}

// BenchmarkFilePickerHoverSweep sweeps the pointer down and up across every
// visible picker row, one motion event per row, each followed by everything
// it schedules (the synthetic cursor moves) and a render per message.
func BenchmarkFilePickerHoverSweep(b *testing.B) {
	m := newFilePickerBenchModel(b, 2000)
	points := filePickerSweepPoints(b, &m)
	b.ResetTimer()
	for range b.N {
		for _, p := range points {
			next, cmd := m.Update(tea.MouseMotionMsg(p))
			m = drain(b, next.(Model), cmd)
			_ = m.View()
		}
		for j := len(points) - 1; j >= 0; j-- {
			next, cmd := m.Update(tea.MouseMotionMsg(points[j]))
			m = drain(b, next.(Model), cmd)
			_ = m.View()
		}
	}
}

// sequenceCmds unwraps tea.Sequence's unexported message type.
func sequenceCmds(msg tea.Msg) ([]tea.Cmd, bool) {
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Type().Elem() != reflect.TypeOf(tea.Cmd(nil)) {
		return nil, false
	}
	cmds := make([]tea.Cmd, v.Len())
	for i := range cmds {
		cmds[i] = v.Index(i).Interface().(tea.Cmd)
	}
	return cmds, true
}

// BenchmarkFilePickerHoverMotionOnly is BenchmarkFilePickerHoverSweep
// without rendering: what each motion event costs Update, which is what
// decides whether the cursor keeps up with the pointer, since a terminal
// emits motion events far faster than frames.
func BenchmarkFilePickerHoverMotionOnly(b *testing.B) {
	m := newFilePickerBenchModel(b, 2000)
	points := filePickerSweepPoints(b, &m)
	b.ResetTimer()
	for range b.N {
		for _, p := range points {
			next, _ := m.Update(tea.MouseMotionMsg(p))
			m = next.(Model)
		}
		for j := len(points) - 1; j >= 0; j-- {
			next, _ := m.Update(tea.MouseMotionMsg(points[j]))
			m = next.(Model)
		}
	}
}
