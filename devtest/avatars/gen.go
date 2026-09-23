//go:build ignore

// Generates the placeholder avatar PNGs in this directory.
//
//	go run gen.go
//
// Temporary: stands in for avatars fetched over XMPP (XEP-0084/XEP-0153)
// so the monogram rendering can be judged before the wire work exists.
// Each file is named for the bare JID it belongs to — see ui.LoadAvatarDir.
package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

const size = 256

type subject struct {
	jid   string
	bg    [2]color.RGBA // diagonal gradient endpoints
	shirt color.RGBA
	hair  color.RGBA
}

func main() {
	subjects := []subject{
		{
			jid:   "alice@localhost",
			bg:    [2]color.RGBA{{30, 140, 150, 255}, {50, 80, 220, 255}},
			shirt: color.RGBA{215, 90, 70, 255},
			hair:  color.RGBA{60, 40, 35, 255},
		},
		{
			jid:   "bob@localhost",
			bg:    [2]color.RGBA{{200, 140, 40, 255}, {150, 60, 30, 255}},
			shirt: color.RGBA{60, 90, 160, 255},
			hair:  color.RGBA{190, 160, 90, 255},
		},
		{
			jid:   "carol@localhost",
			bg:    [2]color.RGBA{{120, 60, 160, 255}, {40, 30, 90, 255}},
			shirt: color.RGBA{80, 170, 120, 255},
			hair:  color.RGBA{30, 25, 25, 255},
		},
		{
			// "default" is the stand-in shown for any contact without an
			// avatar of their own — see ui.LoadAvatarDir. High contrast on
			// purpose, so it stays legible at the header's 8x8 pixels.
			jid:   "default",
			bg:    [2]color.RGBA{{235, 235, 230, 255}, {180, 185, 195, 255}},
			shirt: color.RGBA{40, 50, 70, 255},
			hair:  color.RGBA{25, 25, 30, 255},
		},
	}
	for _, s := range subjects {
		if err := write(s); err != nil {
			panic(err)
		}
	}
}

func write(s subject) error {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	cx, cy := float64(size)/2, float64(size)/2

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			fx, fy := float64(x), float64(y)
			t := (fx + fy) / (2 * size)
			c := color.RGBA{
				R: lerp(s.bg[0].R, s.bg[1].R, t),
				G: lerp(s.bg[0].G, s.bg[1].G, t),
				B: lerp(s.bg[0].B, s.bg[1].B, t),
				A: 255,
			}

			head := math.Hypot(fx-cx, fy-cy+10)
			if head < 86 {
				c = color.RGBA{242, 200, 165, 255}
			}
			if head < 92 && fy < cy-18 {
				c = s.hair
			}
			for _, ex := range []float64{cx - 30, cx + 30} {
				if math.Hypot(fx-ex, fy-(cy-5)) < 9 {
					c = color.RGBA{35, 30, 30, 255}
				}
			}
			if mouth := math.Hypot(fx-cx, fy-(cy+5)); mouth > 42 && mouth < 50 && fy > cy+25 {
				c = color.RGBA{170, 70, 70, 255}
			}
			if math.Hypot(fx-cx, fy-(cy+180)) < 120 && fy > cy+70 {
				c = s.shirt
			}
			img.Set(x, y, c)
		}
	}

	f, err := os.Create(s.jid + ".png")
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func lerp(a, b uint8, t float64) uint8 {
	return uint8(math.Round(float64(a) + (float64(b)-float64(a))*t))
}
