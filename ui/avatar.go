package ui

import (
	"fmt"
	"hash/fnv"
	"image"
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
// avatarCellWidth is how many columns the swatch occupies. One keeps the
// per-row cost to a single column on top of the presence dot that's
// already there; the rendering handles wider values if that turns out to
// read better.
const avatarCellWidth = 1

// avatarColor is a swatch background. Kept as plain RGB rather than a
// color.Color so it can be hashed, compared, and averaged.
type avatarColor struct{ R, G, B uint8 }

// avatarColors maps bare JID to the color derived from that contact's real
// avatar image. Package-level (like presenceGlyphs) because Chat.Title is
// called by bubbles' list delegate, which hands us no place to thread a
// store through. Only ever written before/between renders, via
// SetAvatarImage.
var (
	avatarMu     sync.RWMutex
	avatarImages = map[string]avatarColor{}
)

// SetAvatarImage records the swatch color for one contact's avatar image.
// This is the seam real XEP-0084/XEP-0153 avatar fetching will feed once
// it exists; for now LoadAvatarDir fills it from local files.
func SetAvatarImage(jid string, img image.Image) {
	c, ok := dominantColor(img)
	if !ok {
		return
	}
	avatarMu.Lock()
	defer avatarMu.Unlock()
	avatarImages[strings.ToLower(jid)] = c
}

// ClearAvatarImages drops every recorded avatar color.
func ClearAvatarImages() {
	avatarMu.Lock()
	defer avatarMu.Unlock()
	avatarImages = map[string]avatarColor{}
}

// LoadAvatarDir reads every PNG/JPEG in dir as an avatar, keyed by the
// file's base name (so "alice@localhost.png" is alice@localhost's avatar),
// and returns how many it loaded. A missing directory is not an error —
// there simply are no local avatars. Temporary: a stand-in for fetching
// avatars over XMPP, so the rendering can be judged before the wire work.
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
