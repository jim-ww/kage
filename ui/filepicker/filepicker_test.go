package filepicker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestOpenFollowsSymlinkToDir(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "inner.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}

	m := New()
	m.CurrentDirectory = dir
	m.DirsFirst = false
	m.SetHeight(10)
	m, _ = m.Update(m.Init()())
	for m.HighlightedPath() != filepath.Join(dir, "link") {
		before := m.HighlightedPath()
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		if m.HighlightedPath() == before {
			t.Fatal("symlink not listed")
		}
	}
	if !strings.Contains(m.View(), "link → "+target) {
		t.Errorf("view doesn't show symlink target:\n%s", m.View())
	}

	m, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on symlink to dir didn't descend")
	}
	m, _ = m.Update(cmd())
	if got, want := m.HighlightedPath(), filepath.Join(dir, "link", "inner.txt"); got != want {
		t.Fatalf("highlighted %q, want %q", got, want)
	}
}

func TestHiddenEntriesFiltered(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{".hidden", "shown"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := New()
	m.CurrentDirectory = dir
	m.SetHeight(10)
	m, _ = m.Update(m.Init()())
	if len(m.files) != 1 || m.files[0].name != "shown" {
		t.Fatalf("files = %+v, want only shown", m.files)
	}
	m, cmd := m.ToggleHidden()
	m, _ = m.Update(cmd())
	if len(m.files) != 2 {
		t.Fatalf("with hidden shown got %d files, want 2", len(m.files))
	}
}

func TestPainterMatchesRender(t *testing.T) {
	d := DefaultStyles()
	styles := map[string]lipgloss.Style{
		"cursor":   d.Cursor,
		"selected": d.Selected,
		"file":     d.File,
		"empty":    d.EmptyDirectory,
		"width":    lipgloss.NewStyle().Width(30).Foreground(lipgloss.Color("1")),
		"border":   lipgloss.NewStyle().Border(lipgloss.NormalBorder()),
		"under":    lipgloss.NewStyle().Underline(true),
	}
	texts := []string{"", " ", ">", " -rw-r--r-- report.pdf      12kB   3 minutes ago", "日本語 → /tmp/x"}
	for name, style := range styles {
		p := newPainter(style)
		for _, text := range texts {
			if got, want := p.render(text), style.Render(text); got != want {
				t.Errorf("%s: render(%q) = %q, want %q", name, text, got, want)
			}
		}
	}
	if !newPainter(d.Selected).wraps {
		t.Error("color/bold style fell back to Render")
	}
}
