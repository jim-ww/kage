package ui

import "testing"

// TestMessageRowCacheMatchesFreshRender walks a row through the visual
// states a mouse sweep actually produces and asserts the cached rendering
// is byte-identical to rendering it from scratch. A key that failed to
// capture one of those states would show up here as a row that kept its
// previous state's styling.
func TestMessageRowCacheMatchesFreshRender(t *testing.T) {
	m := newMouseSweepBenchModel(30, 150)
	_ = m.View()
	msgs := m.currentMessages()
	cw := m.chatAreaWidth()
	nameWidth := maxSenderNameWidth(msgs)
	idx := 140

	states := []struct {
		name  string
		apply func(m *Model)
	}{
		{"plain", func(m *Model) { m.selectedMsg = -1; m.hover.id = "" }},
		{"selected", func(m *Model) { m.selectedMsg = idx; m.hover.id = "" }},
		{"hovered", func(m *Model) { m.selectedMsg = -1; m.hover.id = zoneMessage(idx) }},
		{"selected+hovered", func(m *Model) { m.selectedMsg = idx; m.hover.id = zoneMessage(idx) }},
		{"flashed", func(m *Model) { m.selectedMsg = -1; m.hover.id = ""; m.flashMsgIdx = idx }},
		{"plain again", func(m *Model) { m.selectedMsg = -1; m.hover.id = ""; m.flashMsgIdx = -1 }},
	}

	for _, st := range states {
		t.Run(st.name, func(t *testing.T) {
			st.apply(&m)

			// Uncached: same inputs, but with the cache detached so it has
			// to render from scratch.
			bare := m
			bare.msgRowCache = nil
			want := bare.renderMessageRow(msgs, idx, cw, nameWidth)

			// First call populates, second must hit — both must match.
			if got := m.renderMessageRow(msgs, idx, cw, nameWidth); got != want {
				t.Errorf("first (populating) render differs from uncached render")
			}
			if got := m.renderMessageRow(msgs, idx, cw, nameWidth); got != want {
				t.Errorf("cached render differs from uncached render")
			}
		})
	}
}

// TestMessageRowCacheDroppedOnContentChange pins the invariant the cache
// hangs off: refreshViewport is where content changes land, so a row whose
// message text changed must not come back from the cache afterwards.
func TestMessageRowCacheDroppedOnContentChange(t *testing.T) {
	m := newMouseSweepBenchModel(30, 150)
	_ = m.View()
	idx := 140

	before := m.renderMessageRow(m.currentMessages(), idx, m.chatAreaWidth(), maxSenderNameWidth(m.currentMessages()))

	msgs := m.accounts[0].Messages[0]
	msgs[idx].Content = "completely different text that must show up"
	m.accounts[0].Messages[0] = msgs
	m.refreshViewport()

	after := m.renderMessageRow(m.currentMessages(), idx, m.chatAreaWidth(), maxSenderNameWidth(m.currentMessages()))
	if before == after {
		t.Error("row came back unchanged after its message content changed — cache outlived refreshViewport")
	}
}

// TestMessageRowCacheDroppedOnWidthChange guards the other invalidation
// axis: every row's wrapping depends on the pane width, so a resize must
// not serve rows wrapped for the old one.
func TestMessageRowCacheDroppedOnWidthChange(t *testing.T) {
	m := newMouseSweepBenchModel(30, 150)
	_ = m.View()
	idx := 140
	msgs := m.currentMessages()
	nameWidth := maxSenderNameWidth(msgs)

	wide := m.renderMessageRow(msgs, idx, m.chatAreaWidth(), nameWidth)
	narrow := m.renderMessageRow(msgs, idx, m.chatAreaWidth()-20, nameWidth)
	if wide == narrow {
		t.Error("row rendered at a narrower width came back identical — cache ignored the width change")
	}
}
