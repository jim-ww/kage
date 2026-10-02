package daemon

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
)

// iconPNG is the tray icon: generated once at init instead of shipped as a
// binary asset, so daemon doesn't need an //go:embed file just for a
// plain filled square. Deliberately simple — a dark square with a lighter
// inset, legible at the ~16-22px a system tray actually renders it at.
//
// iconUnreadPNG is the same icon with a bright dot overlaid in the
// bottom-right corner, swapped in by SetUnread while any account has unread
// messages. Baked as a second full icon rather than composited at swap time
// because systray only takes whole icons anyway, and this keeps the swap a
// pointer assignment.
var (
	iconPNG       []byte
	iconUnreadPNG []byte
)

const iconSize = 32

// Geometry of the unread dot, in icon pixels: a filled circle of dotRadius
// ringed out to ringRadius in near-black, so the dot stays visible against
// both the icon's own colors and whatever the tray panel is behind it.
const (
	dotCenter  = 22.5
	dotRadius  = 6.5
	ringRadius = 8.5
)

func init() {
	iconPNG = encodeIcon(baseIcon())

	unread := baseIcon()
	drawUnreadDot(unread)
	iconUnreadPNG = encodeIcon(unread)
}

func baseIcon() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, iconSize, iconSize))

	bg := color.RGBA{0x1a, 0x1a, 0x2e, 0xff}
	fg := color.RGBA{0xe9, 0x4f, 0x37, 0xff}

	for y := range iconSize {
		for x := range iconSize {
			img.Set(x, y, bg)
		}
	}
	const inset = 8
	for y := inset; y < iconSize-inset; y++ {
		for x := inset; x < iconSize-inset; x++ {
			img.Set(x, y, fg)
		}
	}
	return img
}

// drawUnreadDot paints the dot (and its ring) onto img in place. Coverage is
// computed by 3x3 supersampling per pixel: a hard distance test leaves
// visibly jagged edges on a circle this small once the tray scales 32px down
// to 16.
func drawUnreadDot(img *image.RGBA) {
	dot := color.RGBA{0x4a, 0xe0, 0x6a, 0xff}
	ring := color.RGBA{0x0d, 0x0d, 0x17, 0xff}

	const samples = 3
	for y := range iconSize {
		for x := range iconSize {
			var dotCov, ringCov float64
			for sy := range samples {
				for sx := range samples {
					px := float64(x) + (float64(sx)+0.5)/samples
					py := float64(y) + (float64(sy)+0.5)/samples
					switch d := math.Hypot(px-dotCenter, py-dotCenter); {
					case d <= dotRadius:
						dotCov++
					case d <= ringRadius:
						ringCov++
					}
				}
			}
			if dotCov == 0 && ringCov == 0 {
				continue
			}
			const total = samples * samples
			c := blend(img.RGBAAt(x, y), ring, ringCov/total)
			c = blend(c, dot, dotCov/total)
			img.SetRGBA(x, y, c)
		}
	}
}

// blend composites src over dst at alpha a. The base icon is fully opaque,
// so the result is too.
func blend(dst, src color.RGBA, a float64) color.RGBA {
	switch {
	case a <= 0:
		return dst
	case a >= 1:
		return src
	}
	mix := func(d, s uint8) uint8 {
		return uint8(float64(d)*(1-a) + float64(s)*a + 0.5)
	}
	return color.RGBA{mix(dst.R, src.R), mix(dst.G, src.G), mix(dst.B, src.B), 0xff}
}

func encodeIcon(img image.Image) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic("daemon: encoding tray icon: " + err.Error())
	}
	return buf.Bytes()
}
