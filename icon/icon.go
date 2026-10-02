// Package icon renders kage's icon: the tray icon in its four variants
// (color/mono x plain/unread) and the rounded "plate" app icon.
//
// It's pixel art rendered in code rather than a //go:embed'd PNG because the
// whole design *is* the 16x16 ASCII map below — keeping that as the source
// of truth means the mono and unread variants are a palette swap and a few
// cell edits instead of four binary blobs that have to be re-exported in
// lockstep. Rasterizing one tray PNG costs well under a millisecond, so the
// daemon just renders the variants it needs on first use.
//
// The same grid also renders to SVG and to the plate PNG; those are written
// to assets/icon/ by icon/gen (go generate ./icon), since README and
// desktop-entry packaging need files on disk, not bytes in a process.
package icon

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"
	"sync"
)

//go:generate go run ./gen ../assets/icon

// TraySize is the edge length of the rendered tray PNGs. The tray renders
// them at ~16-24px, but StatusNotifierItem hosts scale the single pixmap we
// hand them, so this is sized for a HiDPI panel rather than for the nominal
// size.
const TraySize = 128

// Grid cell kinds. Everything not listed is empty (fully transparent).
const (
	empty = iota
	shadow
	bubble
	dot
)

// bubbleMap is the icon: a speech bubble with a tail, drawn at 13x12 inside
// the 16x16 grid. '#' is bubble, '.' is a hole punched through it (the
// bubble's own "text" line and the rounded corners).
var bubbleMap = []string{
	".###########.",
	"#############",
	"##.##########",
	"###.#########",
	"####.########",
	"###.#########",
	"##.###....###",
	"#############",
	".###########.",
	".###.........",
	".##..........",
	".#...........",
}

type grid [16][16]int

// build lays out the grid: the drop shadow first (the same bubble offset
// down-right), then the bubble over it, then — for the unread variant — a
// notch bitten out of the bubble's top-right corner with the dot sitting in
// it, so the dot reads as a separate badge rather than part of the bubble.
func build(unread bool) grid {
	var g grid
	for y, row := range bubbleMap {
		for x, ch := range row {
			if ch == '#' && y+3 < 16 && x+2 < 16 {
				g[y+3][x+2] = shadow
			}
		}
	}
	for y, row := range bubbleMap {
		a, b := strings.Index(row, "#"), strings.LastIndex(row, "#")
		for x := a; x <= b; x++ {
			if row[x] == '#' {
				g[y+1][x] = bubble
			} else {
				g[y+1][x] = empty
			}
		}
	}
	if unread {
		for _, p := range [][2]int{{12, 0}, {12, 1}, {12, 2}, {12, 3}, {13, 3}, {14, 3}, {15, 3}} {
			g[p[1]][p[0]] = empty
		}
		for y := range 3 {
			for x := 13; x < 16; x++ {
				g[y][x] = dot
			}
		}
	}
	return g
}

type palette map[int]color.NRGBA

var (
	// colorPal is the normal icon. monoPal is for panels that theme their
	// own tray icons: white at three opacities, so a desktop that tints or
	// masks the pixmap gets a single-color silhouette to work with.
	colorPal = palette{
		shadow: {0x39, 0x4b, 0x70, 0xff},
		bubble: {0xc0, 0xca, 0xf5, 0xff},
		dot:    {0xf7, 0x76, 0x8e, 0xff},
	}
	monoPal = palette{
		shadow: {0xff, 0xff, 0xff, 0x59},
		bubble: {0xff, 0xff, 0xff, 0xff},
		dot:    {0xff, 0xff, 0xff, 0xff},
	}
	plateBg = color.NRGBA{0x1a, 0x1b, 0x26, 0xff}
)

func pal(mono bool) palette {
	if mono {
		return monoPal
	}
	return colorPal
}

// trayCache holds the encoded PNG per variant, indexed by variantIndex. The
// daemon only ever uses two of the four (one palette, both unread states),
// so they're rendered on first use rather than all four at init.
var (
	trayOnce  [4]sync.Once
	trayCache [4][]byte
)

func variantIndex(mono, unread bool) int {
	i := 0
	if mono {
		i |= 1
	}
	if unread {
		i |= 2
	}
	return i
}

// Tray returns the PNG bytes for one tray variant, suitable for handing
// straight to systray.SetIcon. The result is cached and must not be
// modified by the caller.
func Tray(mono, unread bool) []byte {
	i := variantIndex(mono, unread)
	trayOnce[i].Do(func() {
		trayCache[i] = encodePNG(TrayImage(mono, unread))
	})
	return trayCache[i]
}

// TrayImage renders one tray variant as an image: each grid cell becomes a
// TraySize/16 square of flat color, empty cells left transparent.
func TrayImage(mono, unread bool) *image.NRGBA {
	g, p := build(unread), pal(mono)
	img := image.NewNRGBA(image.Rect(0, 0, TraySize, TraySize))
	const s = TraySize / 16
	for y := range 16 {
		for x := range 16 {
			if g[y][x] == empty {
				continue
			}
			c := p[g[y][x]]
			for py := range s {
				for px := range s {
					img.SetNRGBA(x*s+px, y*s+py, c)
				}
			}
		}
	}
	return img
}

// SVG renders one icon variant as a 16x16 SVG — one path per color, each
// cell a 1x1 rect, crispEdges so it scales as pixel art instead of being
// smoothed. For README/docs use; the tray can't take SVG.
func SVG(mono, unread bool) string {
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" width="16" height="16" shape-rendering="crispEdges">` +
		cellsSVG(build(unread), pal(mono), 0, 0) + "</svg>\n"
}

// PlateSVG renders the app icon: the color grid inset by 4 cells on a
// rounded dark square, for anywhere the icon needs its own background
// (desktop entry, app stores, the README header).
func PlateSVG() string {
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="512" height="512">` +
		fmt.Sprintf(`<rect width="24" height="24" rx="5.5" fill="%s"/>`, hex(plateBg)) +
		`<g shape-rendering="crispEdges">` + cellsSVG(build(false), colorPal, 4, 4) + "</g></svg>\n"
}

// PlatePNG rasterizes the plate at cell pixels per grid cell (so 24*cell
// square). The rounded corners are 4x4 supersampled into the alpha channel;
// everything inside is flat, composited onto the opaque plate background.
func PlatePNG(cell int) *image.NRGBA {
	g := build(false)
	size := 24 * cell
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	r := 5.5 * float64(cell)
	for py := range size {
		for px := range size {
			cov := roundedCoverage(px, py, size, r)
			if cov == 0 {
				continue
			}
			c := plateBg
			gx, gy := px/cell-4, py/cell-4
			if gx >= 0 && gx < 16 && gy >= 0 && gy < 16 && g[gy][gx] != empty {
				c = over(plateBg, colorPal[g[gy][gx]])
			}
			c.A = uint8(math.Round(cov * 255))
			img.SetNRGBA(px, py, c)
		}
	}
	return img
}

func cellsSVG(g grid, p palette, ox, oy int) string {
	var sb strings.Builder
	for _, k := range []int{shadow, bubble, dot} {
		c := p[k]
		var d strings.Builder
		for y := range 16 {
			for x := range 16 {
				if g[y][x] == k {
					fmt.Fprintf(&d, "M%d %dh1v1h-1z", x+ox, y+oy)
				}
			}
		}
		if d.Len() == 0 {
			continue
		}
		op := ""
		if c.A != 0xff {
			op = fmt.Sprintf(` fill-opacity="%.2f"`, float64(c.A)/255)
		}
		fmt.Fprintf(&sb, `<path fill="%s"%s d="%s"/>`, hex(c), op, d.String())
	}
	return sb.String()
}

func hex(c color.NRGBA) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// over composites src onto an opaque dst color.
func over(dst, src color.NRGBA) color.NRGBA {
	a := float64(src.A) / 255
	mix := func(d, s uint8) uint8 { return uint8(math.Round(float64(s)*a + float64(d)*(1-a))) }
	return color.NRGBA{mix(dst.R, src.R), mix(dst.G, src.G), mix(dst.B, src.B), 0xff}
}

// roundedCoverage returns the fraction of pixel (px,py) that falls inside a
// rounded square of corner radius r, 4x4 supersampled.
func roundedCoverage(px, py, size int, r float64) float64 {
	const n = 4
	in := 0
	fs := float64(size)
	for j := range n {
		for i := range n {
			x := float64(px) + (float64(i)+0.5)/n
			y := float64(py) + (float64(j)+0.5)/n
			cx := math.Max(r, math.Min(fs-r, x))
			cy := math.Max(r, math.Min(fs-r, y))
			if (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r {
				in++
			}
		}
	}
	return float64(in) / (n * n)
}

// Assets returns every on-disk icon artifact keyed by its path under
// assets/icon/. icon/gen writes these out and the package's own test
// compares them against what's committed, so the two can't drift.
func Assets() map[string][]byte {
	a := map[string][]byte{
		"svg/icon-plate.svg":       []byte(PlateSVG()),
		"plate/icon-plate-480.png": encodePNG(PlatePNG(20)),
		"plate/icon-plate-240.png": encodePNG(PlatePNG(10)),
	}
	for _, v := range []struct {
		name         string
		mono, unread bool
	}{
		{"icon", false, false},
		{"icon-unread", false, true},
		{"icon-mono", true, false},
		{"icon-mono-unread", true, true},
	} {
		a["svg/"+v.name+".svg"] = []byte(SVG(v.mono, v.unread))
		a["tray/"+v.name+".png"] = Tray(v.mono, v.unread)
	}
	return a
}

func encodePNG(img image.Image) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic("icon: encoding PNG: " + err.Error())
	}
	return buf.Bytes()
}
