package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// newLargeMessageBenchModel is a chat whose last message is a huge paste.
func newLargeMessageBenchModel(size int) Model {
	m := newMouseSweepBenchModel(30, 150)
	msgs := m.accounts[0].Messages[0]
	line := "lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor\n"
	msgs[len(msgs)-1].Content = strings.Repeat(line, size/len(line))
	m.refreshViewport()
	return m
}

// BenchmarkRenderLargeMessage is the full re-render of a chat holding one
// huge (collapsed) message.
func BenchmarkRenderLargeMessage(b *testing.B) {
	m := newLargeMessageBenchModel(500_000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		clear(m.rowCache().rows)
		_, _ = m.renderMessagesWithOffsets()
	}
}

// BenchmarkRenderSmallMessage is BenchmarkRenderLargeMessage's baseline.
func BenchmarkRenderSmallMessage(b *testing.B) {
	m := newLargeMessageBenchModel(200)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		clear(m.rowCache().rows)
		_, _ = m.renderMessagesWithOffsets()
	}
}

// TestCollapsedPrefixKeepsVisibleRows checks wrapping the prefix gives the
// same rows shown collapsed (all but the last) as wrapping the whole body,
// and still more than fit when the body has more.
func TestCollapsedPrefixKeepsVisibleRows(t *testing.T) {
	const rows, width = 4, 10
	for _, body := range []string{
		"short",
		strings.Repeat("word ", 200),
		strings.Repeat("line\n", 50),
		"a\nb\n" + strings.Repeat("x", 500) + "\nc",
	} {
		full := strings.Split(ansi.Wrap(body, width, " "), "\n")
		got := strings.Split(ansi.Wrap(collapsedPrefix(body, rows, width), width, " "), "\n")
		n := min(rows-1, len(full))
		if len(got) < n || strings.Join(got[:n], "\n") != strings.Join(full[:n], "\n") {
			t.Errorf("body %.20q: prefix rows %q, want %q", body, got, full[:n])
		}
		if (len(full) >= rows) != (len(got) >= rows) {
			t.Errorf("body %.20q: %d rows wrapped from prefix, %d from body", body, len(got), len(full))
		}
	}
}
