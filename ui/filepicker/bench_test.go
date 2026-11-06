package filepicker

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// benchDir fills a temp dir with n files (plus a few dirs and a symlink),
// the shape of a typical ~/Downloads.
func benchDir(tb testing.TB, n int) string {
	tb.Helper()
	dir := tb.TempDir()
	for i := range n {
		name := filepath.Join(dir, fmt.Sprintf("file-%05d-some-longer-name.pdf", i))
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			tb.Fatal(err)
		}
	}
	for i := range 10 {
		if err := os.Mkdir(filepath.Join(dir, fmt.Sprintf("dir-%d", i)), 0o700); err != nil {
			tb.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "dir-0"), filepath.Join(dir, "link")); err != nil {
		tb.Fatal(err)
	}
	return dir
}

func loadedPicker(tb testing.TB, dir string, height int) Model {
	tb.Helper()
	m := New()
	m.CurrentDirectory = dir
	m.SetHeight(height)
	m.Update(m.Init()())
	if len(m.files) == 0 {
		tb.Fatal("picker loaded no files")
	}
	return m
}

func BenchmarkReadDir(b *testing.B) {
	for _, n := range []int{100, 2000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			m := New()
			m.CurrentDirectory = benchDir(b, n)
			cmd := m.Init()
			b.ResetTimer()
			for range b.N {
				_ = cmd()
			}
		})
	}
}

func BenchmarkView(b *testing.B) {
	m := loadedPicker(b, benchDir(b, 2000), 35)
	m.SetWidth(100)
	b.ResetTimer()
	for range b.N {
		_ = m.View()
	}
}

// BenchmarkCursorDownView is one keypress worth of picker work: move the
// cursor, render.
func BenchmarkCursorDownView(b *testing.B) {
	m := loadedPicker(b, benchDir(b, 2000), 35)
	m.SetWidth(100)
	down := tea.KeyPressMsg{Code: tea.KeyDown}
	up := tea.KeyPressMsg{Code: tea.KeyUp}
	b.ResetTimer()
	for i := range b.N {
		k := down
		if (i/100)%2 == 1 {
			k = up
		}
		m.Update(k)
		_ = m.View()
	}
}
