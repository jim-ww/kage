package notifyd

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
)

// iconPNG is the tray icon: generated once at init instead of shipped as a
// binary asset, so notifyd doesn't need an //go:embed file just for a
// plain filled square. Deliberately simple — a dark square with a lighter
// inset, legible at the ~16-22px a system tray actually renders it at.
var iconPNG []byte

func init() {
	const size = 32
	img := image.NewRGBA(image.Rect(0, 0, size, size))

	bg := color.RGBA{0x1a, 0x1a, 0x2e, 0xff}
	fg := color.RGBA{0xe9, 0x4f, 0x37, 0xff}

	for y := range size {
		for x := range size {
			img.Set(x, y, bg)
		}
	}
	const inset = 8
	for y := inset; y < size-inset; y++ {
		for x := inset; x < size-inset; x++ {
			img.Set(x, y, fg)
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic("notifyd: encoding tray icon: " + err.Error())
	}
	iconPNG = buf.Bytes()
}
