package emojipicker

import "testing"

// BenchmarkHitTestIDs is the per-motion-event work an embedding app's hover
// handler does: every visible cell's zone ID.
func BenchmarkHitTestIDs(b *testing.B) {
	m := New(nil)
	m.ID = "emoji-picker"
	m.Resize(8, 6, 60)
	b.ResetTimer()
	for range b.N {
		for _, i := range m.VisibleCells() {
			_ = m.CellZoneID(i)
		}
	}
}

func BenchmarkView(b *testing.B) {
	m := New(nil)
	m.ID = "emoji-picker"
	m.Resize(8, 6, 60)
	b.ResetTimer()
	for range b.N {
		_ = m.View()
	}
}
