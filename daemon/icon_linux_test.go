package daemon

import (
	"bytes"
	"image/png"
	"testing"
)

// The unread icon must be the plain icon plus a dot in one corner: a tray
// swapping between the two should look like a badge appearing, not like a
// different application.
func TestUnreadIconIsBaseIconPlusCornerDot(t *testing.T) {
	plain, err := png.Decode(bytes.NewReader(iconPNG))
	if err != nil {
		t.Fatalf("decoding plain icon: %v", err)
	}
	unread, err := png.Decode(bytes.NewReader(iconUnreadPNG))
	if err != nil {
		t.Fatalf("decoding unread icon: %v", err)
	}
	if plain.Bounds() != unread.Bounds() {
		t.Fatalf("bounds differ: plain %v, unread %v", plain.Bounds(), unread.Bounds())
	}

	for y := range iconSize {
		for x := range iconSize {
			// Everything outside the dot's bounding box must be untouched.
			inDot := float64(x) >= dotCenter-ringRadius && float64(y) >= dotCenter-ringRadius
			same := plain.At(x, y) == unread.At(x, y)
			if !inDot && !same {
				t.Fatalf("pixel (%d,%d) outside the dot changed", x, y)
			}
		}
	}

	// dotCenter falls on a pixel boundary; step back half a pixel to land on
	// the pixel just inside it.
	const c = int(dotCenter - 0.5)
	if plain.At(c, c) == unread.At(c, c) {
		t.Fatal("dot center is unchanged - no dot was drawn")
	}
}
