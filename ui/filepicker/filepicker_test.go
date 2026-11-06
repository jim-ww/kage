package filepicker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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
