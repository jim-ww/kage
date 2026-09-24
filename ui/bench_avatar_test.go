package ui

import (
	"image"
	"image/color"
	"testing"

	"charm.land/bubbles/v2/list"
)

// gradientImage is a stand-in for a real photograph: no two adjacent
// pixels alike, so the renderer's run-length SGR elision gets no help. A
// flat fixture would report times a real avatar never sees.
func gradientImage(size int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8(x * 255 / size),
				G: uint8(y * 255 / size),
				B: uint8((x*x + y*y) % 256),
				A: 255,
			})
		}
	}
	return img
}

// BenchmarkAvatarPictureCold is the cost a window-resize drag pays: every
// size message renders the picture at a size never seen before, so the
// cache can't help. Keep an eye on this one — it's multiplied by however
// many size messages a drag produces.
func BenchmarkAvatarPictureCold(b *testing.B) {
	ClearAvatarImages()
	b.Cleanup(ClearAvatarImages)
	SetAvatarImage("alice@localhost", gradientImage(256))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Alternating sizes so every iteration misses the one-entry cache.
		cols := 38 - 2*(i%2)
		renderAvatarPicture("alice@localhost", cols, cols/2)
	}
}

// BenchmarkAvatarPictureWarm is what every other frame pays — a keystroke,
// a mouse motion, an incoming message — with nothing about the avatar
// changed.
func BenchmarkAvatarPictureWarm(b *testing.B) {
	ClearAvatarImages()
	b.Cleanup(ClearAvatarImages)
	SetAvatarImage("alice@localhost", gradientImage(256))
	renderAvatarPicture("alice@localhost", 38, 19)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		renderAvatarPicture("alice@localhost", 38, 19)
	}
}

func benchModel(b *testing.B, withAvatars bool) Model {
	ClearAvatarImages()
	if withAvatars {
		SetAvatarImage("c@localhost", gradientImage(256))
	}
	m := newTestModel(nil)
	items := make([]list.Item, 0, 30)
	for i := 0; i < 30; i++ {
		items = append(items, Chat{Name: "contact", Address: "c@localhost", Presence: PresenceOnline, LastMessage: "hello there"})
	}
	m.accounts = []Account{{Name: "me", Chats: items}}
	m.chats.SetItems(items)
	m.width, m.height, m.termHeight = 160, 40, 40
	m.updateSizes()
	return m
}

// BenchmarkAvatarPanel is the panel's own cost per frame once its cache is
// warm — most frames.
func BenchmarkAvatarPanel(b *testing.B) {
	m := benchModel(b, true)
	w := m.sidebarContentWidth()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.renderAvatarPanel(w)
	}
}

// BenchmarkViewWithAvatarPanel and BenchmarkViewWithoutAvatarPanel bracket
// what the panel costs a whole frame. The gap between them is what a window
// resize pays per size message, since a resize invalidates every render
// cache in the sidebar. Benchmarked against a gradient avatar, the worst
// case for the renderer's run-length SGR elision.
func BenchmarkViewWithAvatarPanel(b *testing.B) {
	m := benchModel(b, true)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.View()
	}
}

func BenchmarkViewWithoutAvatarPanel(b *testing.B) {
	m := benchModel(b, false)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.View()
	}
}
