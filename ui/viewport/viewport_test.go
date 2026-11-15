package viewport

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func newSized(w, h int, lines ...string) Model {
	m := New()
	m.SetWidth(w)
	m.SetHeight(h)
	m.SetContentLines(lines)
	return m
}

// TestViewMatchesLipglossSizing checks View pads to the same block a sized
// lipgloss render of the visible lines gives.
func TestViewMatchesLipglossSizing(t *testing.T) {
	styled := lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Render("red")
	m := newSized(8, 4, "a", styled+" 日本", "", "abcdefgh", "hidden")
	want := lipgloss.NewStyle().Width(8).Height(4).Render(strings.Join([]string{"a", styled + " 日本", "", "abcdefgh"}, "\n"))
	if got := m.View(); got != want {
		t.Fatalf("View =\n%q\nwant\n%q", got, want)
	}
}

func TestViewPadsShortContentAndCutsWideLines(t *testing.T) {
	m := newSized(4, 3, "abcdefg")
	if got, want := m.View(), "abcd\n    \n    "; got != want {
		t.Fatalf("View = %q, want %q", got, want)
	}
	if z := newSized(0, 3, "a"); z.View() != "" {
		t.Fatal("zero-width View isn't empty")
	}
}

func TestScrollingClampsToContent(t *testing.T) {
	m := newSized(5, 3, "0", "1", "2", "3", "4", "5", "6")
	if !m.AtTop() || m.AtBottom() {
		t.Fatal("a fresh viewport isn't at the top")
	}
	m.GotoBottom()
	if m.YOffset() != 4 || !m.AtBottom() {
		t.Fatalf("GotoBottom: YOffset = %d, want 4", m.YOffset())
	}
	m.SetYOffset(100)
	if m.YOffset() != 4 {
		t.Fatalf("SetYOffset past the end: YOffset = %d, want 4", m.YOffset())
	}
	m.SetYOffset(-3)
	if m.YOffset() != 0 {
		t.Fatalf("SetYOffset before the start: YOffset = %d, want 0", m.YOffset())
	}

	m.SetYOffset(4)
	m.SetContentLines([]string{"0", "1", "2", "3"})
	if m.YOffset() != 1 {
		t.Fatalf("shrunk content: YOffset = %d, want clamped to 1", m.YOffset())
	}
}

func TestUpdatePagesAndWheels(t *testing.T) {
	m := newSized(5, 3, strings.Split(strings.Repeat("x\n", 19)+"x", "\n")...)
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.YOffset() != 3 {
		t.Fatalf("pgdown: YOffset = %d, want 3", m.YOffset())
	}
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.YOffset() != 3+wheelDelta {
		t.Fatalf("wheel down: YOffset = %d, want %d", m.YOffset(), 3+wheelDelta)
	}
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.YOffset() != 0 {
		t.Fatalf("wheel up, pgup: YOffset = %d, want 0", m.YOffset())
	}
}

func TestSetContentEmptyIsNoLines(t *testing.T) {
	m := newSized(3, 2)
	m.SetContent("")
	if m.TotalLineCount() != 0 {
		t.Fatalf("TotalLineCount = %d, want 0", m.TotalLineCount())
	}
}

func BenchmarkView(b *testing.B) {
	lines := make([]string, 500)
	for i := range lines {
		lines[i] = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Render("hello world, a message line ") + "日本語"
	}
	m := newSized(120, 60, lines...)
	for b.Loop() {
		_ = m.View()
	}
}
