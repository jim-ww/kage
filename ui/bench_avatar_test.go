package ui

import (
	"image/color"
	"testing"
)

// BenchmarkAvatarPictureCold is the cost a window-resize drag pays: every
// size message renders the picture at a size never seen before, so the
// cache can't help. Keep an eye on this one — it's multiplied by however
// many size messages a drag produces.
func BenchmarkAvatarPictureCold(b *testing.B) {
	ClearAvatarImages()
	b.Cleanup(ClearAvatarImages)
	SetAvatarImage("alice@localhost", solidImage(256, 256, color.RGBA{200, 40, 40, 255}, 128))

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
	SetAvatarImage("alice@localhost", solidImage(256, 256, color.RGBA{200, 40, 40, 255}, 128))
	renderAvatarPicture("alice@localhost", 38, 19)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		renderAvatarPicture("alice@localhost", 38, 19)
	}
}
