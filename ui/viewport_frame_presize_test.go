package ui

import "testing"

// TestViewportFramePresizedMatchesSizedRender pins the precondition
// renderViewportFrame's fast path relies on: when the viewport's own
// width/height already match the frame being rendered, the content it
// returns is already exactly that size, so lipgloss's wrap+align passes
// (viewportContent's, and viewportFrame's own Width/Height) are
// byte-for-byte identity transforms. If a future lipgloss/viewport change
// makes either pass actually alter the content, this fails instead of the
// chat pane silently losing its padding.
func TestViewportFramePresizedMatchesSizedRender(t *testing.T) {
	m := newMouseSweepBenchModel(30, 150)
	_ = m.View() // lay out the viewport so its width/height are real

	width := m.chatAreaWidth()
	height := m.height - m.inputAreaHeight() - chatStatusHeight
	if m.viewport.Width() != width || m.viewport.Height() != height {
		t.Fatalf("viewport %dx%d does not match frame %dx%d — the fast path's precondition no longer holds",
			m.viewport.Width(), m.viewport.Height(), width, height)
	}

	for _, tc := range []struct {
		name   string
		offset int
	}{
		{"top", 0},
		{"middle", len(m.viewportLines) / 2},
		{"bottom", max(0, len(m.viewportLines)-height)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m.viewport.SetYOffset(tc.offset)
			content := m.viewport.View()

			sized := m.styles.viewportFrame(width, height, m.styles.viewportContent(width, height, content))
			presized := m.styles.viewportFramePresized(content)
			if sized != presized {
				t.Errorf("presized render differs from sized render\n sized len=%d\npresized len=%d", len(sized), len(presized))
			}
		})
	}
}

// TestViewportFrameFallsBackWhenSizeMismatch guards the other half of the
// fast path: a frame whose size doesn't match the viewport's must still go
// through the sizing styles, since there the wrap/pad is doing real work.
func TestViewportFrameFallsBackWhenSizeMismatch(t *testing.T) {
	m := newMouseSweepBenchModel(30, 150)
	_ = m.View()

	content := m.viewport.View()
	narrower := m.chatAreaWidth() - 5
	got := m.renderViewportFrame(viewportFrameCacheEntry{
		width:   narrower,
		height:  m.viewport.Height(),
		content: content,
	})
	want := m.styles.viewportFrame(narrower, m.viewport.Height(), m.styles.viewportContent(narrower, m.viewport.Height(), content))
	if got != want {
		t.Error("mismatched frame size did not fall back to the sizing styles")
	}
}
