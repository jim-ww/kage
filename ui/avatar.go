package ui

import (
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"

	"charm.land/lipgloss/v2"
)

// Avatar rendering, monogram tier: a single colored cell carrying the
// contact's initial, rendered inline in the chat-list row title (see
// Chat.Title) and the chat status bar (see renderChatStatusBar).
//
// Deliberately not an image — at the one-to-three cells a sidebar row can
// spare, a downscaled photo is unreadable mush, while a color+initial
// swatch does the one job avatars do in a list: let the eye find a row
// without reading it. The color comes from a real avatar image when one is
// known (dominantColor) and from the bare JID otherwise (hashColor), so
// both cases render identically and a contact's swatch doesn't jump around
// when their avatar arrives.
//
// avatarCellWidth is how many columns the monogram swatch occupies in a
// chat-list row. Three so it reads as a swatch with a letter in it rather
// than as one more colored dot beside the presence glyph; the rendering
// pads and centers the initial, so this is the only place to change it.
const avatarCellWidth = 3

// avatarColor is a swatch background. Kept as plain RGB rather than a
// color.Color so it can be hashed, compared, and averaged.
type avatarColor struct{ R, G, B uint8 }

// avatarColors/avatarPictures map bare JID to that contact's swatch color
// and to the avatar image itself. Package-level (like presenceGlyphs)
// because Chat.Title is called by bubbles' list delegate, which hands us no
// place to thread a store through. Only ever written before/between
// renders, via SetAvatarImage.
var (
	avatarMu       sync.RWMutex
	avatarImages   = map[string]avatarColor{}
	avatarPictures = map[string]image.Image{}
	// avatarFallback stands in for every contact with no avatar of their
	// own. Only ever set from a file literally named "default" in the
	// avatar directory — real avatar fetching will have no such thing, but
	// without it the local-file stand-in only ever matches the handful of
	// JIDs someone happened to create files for, which is useless for
	// looking at the rendering against a real account.
	avatarFallback image.Image
)

// avatarFallbackName is the base name (sans extension) that marks a file in
// the avatar directory as the stand-in for every contact without their own.
const avatarFallbackName = "default"

// avatarStoredSize is the edge length every avatar is downscaled to on the
// way into the store. Large enough that any cell size the header asks for
// still box-averages over several source pixels, small enough that holding
// one per contact costs nothing.
const avatarStoredSize = 64

// SetAvatarImage records the swatch color for one contact's avatar image.
// This is the seam real XEP-0084/XEP-0153 avatar fetching will feed once
// it exists; for now LoadAvatarDir fills it from local files.
func SetAvatarImage(jid string, img image.Image) {
	key := strings.ToLower(jid)
	scaled := downscale(img, avatarStoredSize)

	avatarMu.Lock()
	defer avatarMu.Unlock()
	avatarPictures[key] = scaled
	// A greyscale avatar yields no usable swatch color; the picture is
	// still kept, and the JID hash supplies the monogram's color.
	if c, ok := dominantColor(img); ok {
		avatarImages[key] = c
	}
}

// ClearAvatarImages drops every recorded avatar color.
func ClearAvatarImages() {
	avatarMu.Lock()
	defer avatarMu.Unlock()
	avatarImages = map[string]avatarColor{}
	avatarPictures = map[string]image.Image{}
	avatarFallback = nil
}

// SetFallbackAvatarImage records the image used for every contact with no
// avatar of its own. The swatch color is deliberately not taken from it:
// one shared picture must not collapse every chat-list monogram to the same
// color, which is the one thing the list swatch is for.
func SetFallbackAvatarImage(img image.Image) {
	scaled := downscale(img, avatarStoredSize)
	avatarMu.Lock()
	defer avatarMu.Unlock()
	avatarFallback = scaled
}

// avatarPicture returns the stored image for a contact, if any.
func avatarPicture(address string) (image.Image, bool) {
	avatarMu.RLock()
	defer avatarMu.RUnlock()
	if img, ok := avatarPictures[strings.ToLower(address)]; ok {
		return img, true
	}
	if avatarFallback != nil {
		return avatarFallback, true
	}
	return nil, false
}

// hasAvatarPicture reports whether a contact has a real avatar image, as
// opposed to only a hashed monogram color. Drives whether the chat header
// grows to picture height — see Model.chatStatusHeight.
func hasAvatarPicture(address string) bool {
	_, ok := avatarPicture(address)
	return ok
}

// downscale box-averages img down to fit an edge*edge square, preserving
// aspect ratio. Done once on the way into the store so every later render
// samples a small image; an image already within the bound is returned
// as-is.
func downscale(img image.Image, edge int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return img
	}
	if w <= edge && h <= edge {
		return img
	}
	dw, dh := edge, edge
	if w > h {
		dh = max(1, h*edge/w)
	} else {
		dw = max(1, w*edge/h)
	}
	out := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			out.Set(x, y, boxAverage(img, b, x, y, dw, dh))
		}
	}
	return out
}

// boxAverage is the mean color of the source region that maps to cell
// (cx, cy) of a cols*rows grid laid over bounds. Averaging rather than
// nearest-neighbour matters at avatar sizes: a 6x6 nearest-neighbour
// sampling of a face is six pixels of whatever happened to sit under the
// sample points, which is noise, while the average at least preserves the
// picture's large shapes.
func boxAverage(img image.Image, bounds image.Rectangle, cx, cy, cols, rows int) color.RGBA {
	x0 := bounds.Min.X + cx*bounds.Dx()/cols
	x1 := bounds.Min.X + (cx+1)*bounds.Dx()/cols
	y0 := bounds.Min.Y + cy*bounds.Dy()/rows
	y1 := bounds.Min.Y + (cy+1)*bounds.Dy()/rows
	if x1 <= x0 {
		x1 = x0 + 1
	}
	if y1 <= y0 {
		y1 = y0 + 1
	}
	var r, g, b, n uint64
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			pr, pg, pb, _ := img.At(x, y).RGBA()
			r, g, b, n = r+uint64(pr>>8), g+uint64(pg>>8), b+uint64(pb>>8), n+1
		}
	}
	if n == 0 {
		return color.RGBA{A: 255}
	}
	return color.RGBA{R: uint8(r / n), G: uint8(g / n), B: uint8(b / n), A: 255}
}

// renderAvatarPicture renders a contact's avatar as cols x rows character
// cells of half-blocks: one U+2580 per cell, foreground painting the upper
// pixel and background the lower, so a cell carries two pixels and a row of
// cells carries two pixel rows. Pure SGR text — every cell is exactly one
// column wide to lipgloss.Width, which is what lets the result be composed
// with ordinary styled strings (unlike sixel or the Kitty graphics
// protocol, whose payloads measure as zero columns).
//
// Returns false when the contact has no avatar image, leaving the caller to
// fall back to the monogram.
func renderAvatarPicture(address string, cols, rows int) (string, bool) {
	if cols <= 0 || rows <= 0 {
		return "", false
	}
	img, ok := avatarPicture(address)
	if !ok {
		return "", false
	}
	bounds := img.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		return "", false
	}

	pixelRows := rows * 2
	lines := make([]string, 0, rows)
	var line strings.Builder
	for row := 0; row < rows; row++ {
		line.Reset()
		for col := 0; col < cols; col++ {
			top := boxAverage(img, bounds, col, row*2, cols, pixelRows)
			bottom := boxAverage(img, bounds, col, row*2+1, cols, pixelRows)
			line.WriteString(lipgloss.NewStyle().
				Foreground(lipgloss.Color(rgbHex(top))).
				Background(lipgloss.Color(rgbHex(bottom))).
				Render(upperHalfBlock))
		}
		lines = append(lines, line.String())
	}
	return strings.Join(lines, "\n"), true
}

// upperHalfBlock is U+2580. Foreground colors its upper half, background
// the lower.
const upperHalfBlock = "\u2580"

func rgbHex(c color.RGBA) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// LoadAvatarDir reads every PNG/JPEG in dir as an avatar, keyed by the
// file's base name (so "alice@localhost.png" is alice@localhost's avatar),
// and returns how many named avatars it loaded. A file named "default" is
// not one of them: it becomes the fallback shown for every contact without
// an avatar of their own. A missing directory is not an error — there
// simply are no local avatars. Temporary: a stand-in for fetching avatars
// over XMPP, so the rendering can be judged before the wire work.
func LoadAvatarDir(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	loaded := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".png", ".jpg", ".jpeg":
		default:
			continue
		}
		jid := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		img, err := decodeImageFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return loaded, fmt.Errorf("reading avatar %s: %w", e.Name(), err)
		}
		if strings.EqualFold(jid, avatarFallbackName) {
			SetFallbackAvatarImage(img)
			continue
		}
		SetAvatarImage(jid, img)
		loaded++
	}
	return loaded, nil
}

func decodeImageFile(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}

// dominantColor quantizes an image into a 4x4x4 color cube and returns the
// most populous bucket's mean color. Near-greys are skipped so a portrait's
// background or clothing wins over skin tones, which otherwise dominate any
// photo of a person and make every contact's swatch the same beige. Reports
// false for an image with no saturated pixels at all (a greyscale avatar),
// leaving the JID hash to supply the color.
func dominantColor(img image.Image) (avatarColor, bool) {
	type bucket struct{ r, g, b, n uint64 }
	buckets := map[int]*bucket{}
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r32, g32, b32, a32 := img.At(x, y).RGBA()
			if a32 < 0x8000 { // mostly transparent: not part of the picture
				continue
			}
			r, g, b := uint64(r32>>8), uint64(g32>>8), uint64(b32>>8)
			hi, lo := max(r, max(g, b)), min(r, min(g, b))
			if hi-lo < 40 {
				continue
			}
			key := int(r>>6)<<4 | int(g>>6)<<2 | int(b>>6)
			bkt := buckets[key]
			if bkt == nil {
				bkt = &bucket{}
				buckets[key] = bkt
			}
			bkt.r, bkt.g, bkt.b, bkt.n = bkt.r+r, bkt.g+g, bkt.b+b, bkt.n+1
		}
	}
	if len(buckets) == 0 {
		return avatarColor{}, false
	}
	keys := make([]int, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	// Sorted by population, then by key, so an image with two equally
	// populous buckets always picks the same one instead of rendering a
	// different color per run (Go randomizes map iteration order).
	sort.Slice(keys, func(i, j int) bool {
		bi, bj := buckets[keys[i]], buckets[keys[j]]
		if bi.n != bj.n {
			return bi.n > bj.n
		}
		return keys[i] < keys[j]
	})
	b := buckets[keys[0]]
	return avatarColor{
		R: uint8(b.r / b.n),
		G: uint8(b.g / b.n),
		B: uint8(b.b / b.n),
	}, true
}

// hashColor derives a stable swatch color from a bare JID: the hash picks a
// hue, while saturation and lightness are fixed at values that stay legible
// under white text in both light and dark terminals. Hue is the only free
// dimension precisely so that no contact can hash to an unreadably pale or
// muddy swatch.
func hashColor(jid string) avatarColor {
	h := fnv.New32a()
	h.Write([]byte(strings.ToLower(jid)))
	hue := float64(h.Sum32() % 360)
	r, g, b := hslToRGB(hue, 0.55, 0.45)
	return avatarColor{R: r, G: g, B: b}
}

func hslToRGB(h, s, l float64) (uint8, uint8, uint8) {
	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := l - c/2
	var r, g, b float64
	switch {
	case h < 60:
		r, g, b = c, x, 0
	case h < 120:
		r, g, b = x, c, 0
	case h < 180:
		r, g, b = 0, c, x
	case h < 240:
		r, g, b = 0, x, c
	case h < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	return uint8(math.Round((r + m) * 255)), uint8(math.Round((g + m) * 255)), uint8(math.Round((b + m) * 255))
}

// readableOn picks black or white for text drawn on bg, by relative
// luminance — so an initial stays legible on a pale avatar as well as a
// dark one.
func readableOn(bg avatarColor) avatarColor {
	lum := 0.2126*float64(bg.R) + 0.7152*float64(bg.G) + 0.0722*float64(bg.B)
	if lum > 140 {
		return avatarColor{0, 0, 0}
	}
	return avatarColor{255, 255, 255}
}

func (c avatarColor) hex() string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// avatarColorFor returns the swatch color for a contact: the one derived
// from their real avatar if we have it, else the JID hash.
func avatarColorFor(address string) avatarColor {
	key := strings.ToLower(address)
	avatarMu.RLock()
	c, ok := avatarImages[key]
	avatarMu.RUnlock()
	if ok {
		return c
	}
	return hashColor(key)
}

// avatarInitial is the character drawn in the swatch: the first letter or
// digit of the display name, uppercased. Leading punctuation is skipped so
// a nickname like "…alice" still shows an A — except when the name is *all*
// punctuation (a MUC's "#kage"), where that leading character is the
// identifying one and gets used as-is. Falls back to the address, then to
// "?" for a contact with neither.
func avatarInitial(name, address string) string {
	for _, source := range []string{name, address} {
		if r, ok := firstAlnum(source); ok {
			return string(unicode.ToUpper(r))
		}
		for _, r := range source {
			if !unicode.IsSpace(r) {
				return string(r)
			}
		}
	}
	return "?"
}

func firstAlnum(s string) (rune, bool) {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r, true
		}
	}
	return 0, false
}

// renderAvatarCell renders a contact's monogram swatch: avatarCellWidth
// columns of solid background with the initial centered in it. Returns a
// pre-styled string ending in a reset, like presenceGlyph — callers must
// not wrap it in an outer Foreground, which the reset would cut off (see
// renderChatStatusBar).
func renderAvatarCell(name, address string) string {
	bg := avatarColorFor(address)
	fg := readableOn(bg)
	text := avatarInitial(name, address)
	if pad := avatarCellWidth - lipgloss.Width(text); pad > 0 {
		left := pad / 2
		text = strings.Repeat(" ", left) + text + strings.Repeat(" ", pad-left)
	}
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(fg.hex())).
		Background(lipgloss.Color(bg.hex())).
		Bold(true).
		Render(text)
}

// encodePNG is png.Encode, named here so the test helper that writes
// fixture avatars doesn't need its own import of image/png alongside this
// file's decoder registration.
var encodePNG = png.Encode
