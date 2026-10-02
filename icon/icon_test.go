package icon

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// The committed artifacts under assets/icon/ are what README and packaging
// point at, but the daemon renders its tray icon from this package instead —
// so nothing would notice if the two diverged. This is that notice: run
// `go generate ./icon` after changing the grid or a palette.
func TestAssetsMatchGenerated(t *testing.T) {
	for name, want := range Assets() {
		path := filepath.Join("..", "assets", "icon", name)
		got, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: %v (run: go generate ./icon)", name, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is stale (run: go generate ./icon)", name)
		}
	}
}

// The unread variant must look like the plain icon with a badge added, not
// like a different application: same size, and every pixel outside the
// badge's corner identical.
func TestUnreadIsPlainIconPlusCornerBadge(t *testing.T) {
	for _, mono := range []bool{false, true} {
		plain, unread := TrayImage(mono, false), TrayImage(mono, true)
		if plain.Bounds() != unread.Bounds() {
			t.Fatalf("mono=%v: bounds differ: %v vs %v", mono, plain.Bounds(), unread.Bounds())
		}

		// The badge occupies the top-right 3x3 grid cells, plus the notch
		// bitten out of the bubble around it - the whole top-right quadrant
		// of the grid, generously.
		const cell = TraySize / 16
		changedInBadge := false
		for y := range TraySize {
			for x := range TraySize {
				inBadge := x >= 12*cell && y < 4*cell
				same := plain.At(x, y) == unread.At(x, y)
				switch {
				case !inBadge && !same:
					t.Fatalf("mono=%v: pixel (%d,%d) outside the badge corner changed", mono, x, y)
				case inBadge && !same:
					changedInBadge = true
				}
			}
		}
		if !changedInBadge {
			t.Errorf("mono=%v: no badge was drawn", mono)
		}
	}
}

// Tray hands its result straight to systray.SetIcon, which decodes it with
// image/png and nothing else - so every variant has to be decodable PNG at
// the declared size.
func TestTrayVariantsAreDecodablePNG(t *testing.T) {
	for _, mono := range []bool{false, true} {
		for _, unread := range []bool{false, true} {
			img, err := png.Decode(bytes.NewReader(Tray(mono, unread)))
			if err != nil {
				t.Fatalf("mono=%v unread=%v: %v", mono, unread, err)
			}
			if b := img.Bounds(); b.Dx() != TraySize || b.Dy() != TraySize {
				t.Errorf("mono=%v unread=%v: size %v, want %dx%d", mono, unread, b.Size(), TraySize, TraySize)
			}
		}
	}
}

// The mono variant exists for panels that tint or mask the pixmap, which
// only works if the icon carries no color of its own.
func TestMonoVariantIsGreyscale(t *testing.T) {
	img := TrayImage(true, true)
	for y := range TraySize {
		for x := range TraySize {
			c := img.NRGBAAt(x, y)
			if c.A == 0 {
				continue
			}
			if c.R != c.G || c.G != c.B {
				t.Fatalf("pixel (%d,%d) is colored: %v", x, y, c)
			}
		}
	}
}
