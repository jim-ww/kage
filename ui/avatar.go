package ui

import (
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

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
// avatarColor is a configured contact tint. Kept as plain RGB rather than
// a color.Color so it can be compared.
type avatarColor struct{ R, G, B uint8 }

// avatarsOn gates avatar rendering: the sidebar panel goes away when it's
// off and its rows go back to the chat list. Package-level (like
// AttachmentsDir) because the avatar store it guards is itself
// package-level, reached from rendering paths the Model isn't threaded
// through. Defaults on, so a Model built without DisplayOptions — every
// test — behaves like the shipped default. Contact name tints are a
// separate, opt-in setting and are not gated by this.
var avatarsOn atomic.Bool

func init() { avatarsOn.Store(true) }

// setAvatarsEnabled applies the config's avatar setting; see
// DisplayOptions.AvatarsDisabled.
func setAvatarsEnabled(on bool) { avatarsOn.Store(on) }

// avatarsEnabled reports whether avatars are shown at all.
func avatarsEnabled() bool { return avatarsOn.Load() }

// avatarPictures maps bare JID to that contact's avatar image.
// Package-level (like presenceGlyphs) because the rendering paths that
// reach it aren't threaded through the Model. Only ever written
// before/between renders, via SetAvatarImage.
var (
	avatarMu       sync.RWMutex
	avatarPictures = map[string]image.Image{}
	// avatarGen is bumped whenever any of the above changes, so the render
	// cache below can be invalidated without comparing images.
	avatarGen uint64
	// avatarBlockCache memoizes the last rendered picture. One entry is
	// enough: exactly one panel is drawn per frame, so this turns every
	// frame that didn't change the avatar, its size, or the selected
	// contact — a keystroke, a mouse motion, an incoming message — into a
	// map-free string reuse, while a resize drag still pays one render per
	// distinct size.
	avatarBlockCache avatarBlockKey
)

type avatarBlockKey struct {
	address    string
	cols, rows int
	gen        uint64
	rendered   string
}

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
	avatarGen++
}

// ClearAvatarImages drops every recorded avatar color.
func ClearAvatarImages() {
	avatarMu.Lock()
	defer avatarMu.Unlock()
	avatarPictures = map[string]image.Image{}
	avatarGen++
	avatarBlockCache = avatarBlockKey{}
}

// avatarPicture returns the stored image for a contact, if any.
func avatarPicture(address string) (image.Image, bool) {
	avatarMu.RLock()
	defer avatarMu.RUnlock()
	img, ok := avatarPictures[strings.ToLower(address)]
	return img, ok
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
	// Fast path for *image.RGBA — what downscale produces, so every stored
	// avatar takes it. img.At boxes a color.Color into an interface for
	// every pixel read, which at a few thousand pixels per rendered frame
	// is most of the work.
	if rgba, ok := img.(*image.RGBA); ok {
		for y := y0; y < y1; y++ {
			row := rgba.Pix[(y-rgba.Rect.Min.Y)*rgba.Stride:]
			for x := x0; x < x1; x++ {
				i := (x - rgba.Rect.Min.X) * 4
				r, g, b, n = r+uint64(row[i]), g+uint64(row[i+1]), b+uint64(row[i+2]), n+1
			}
		}
	} else {
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				pr, pg, pb, _ := img.At(x, y).RGBA()
				r, g, b, n = r+uint64(pr>>8), g+uint64(pg>>8), b+uint64(pb>>8), n+1
			}
		}
	}
	if n == 0 {
		return color.RGBA{A: 255}
	}
	return color.RGBA{R: uint8(r / n), G: uint8(g / n), B: uint8(b / n), A: 255}
}

// renderImageBlocks renders img as cols x rows half-block cells, shared by
// the contact-avatar path below and the avatar preview (see
// avatar_preview.go). Returns "" for a degenerate size or an empty image.
func renderImageBlocks(img image.Image, cols, rows int) string {
	if img == nil || cols <= 0 || rows <= 0 {
		return ""
	}
	bounds := img.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		return ""
	}
	pixelRows := rows * 2
	// Written as SGR directly rather than one lipgloss.Style.Render per
	// cell. A picture this size is hundreds of cells, and a Render each —
	// with its own allocation and grapheme-width scan — costs milliseconds
	// per frame, which a window-resize drag pays on every size message.
	// The output is byte-identical to what lipgloss would emit for a
	// truecolor foreground+background pair.
	var sb strings.Builder
	sb.Grow(rows * (cols*32 + 4))
	for row := 0; row < rows; row++ {
		if row > 0 {
			sb.WriteByte('\n')
		}
		// Each line starts after a reset, so no color carries into it.
		var lastTop, lastBottom color.RGBA
		var have bool
		for col := 0; col < cols; col++ {
			top := boxAverage(img, bounds, col, row*2, cols, pixelRows)
			bottom := boxAverage(img, bounds, col, row*2+1, cols, pixelRows)
			// Flat regions — backgrounds, hair, clothing — are most of a
			// typical avatar, and repeating an identical SGR pair for each
			// of their cells triples the string every later layout pass has
			// to scan for grapheme widths.
			switch {
			case !have || (top != lastTop && bottom != lastBottom):
				writeHalfBlockCell(&sb, top, bottom)
			case top != lastTop:
				writeSGRColor(&sb, 38, top)
				sb.WriteString(upperHalfBlock)
			case bottom != lastBottom:
				writeSGRColor(&sb, 48, bottom)
				sb.WriteString(upperHalfBlock)
			default:
				sb.WriteString(upperHalfBlock)
			}
			lastTop, lastBottom, have = top, bottom, true
		}
		sb.WriteString(ansiReset)
	}
	return sb.String()
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
	key := strings.ToLower(address)
	avatarMu.RLock()
	img, ok := avatarPictures[key]
	c := avatarBlockCache
	gen := avatarGen
	avatarMu.RUnlock()
	if !ok || img == nil {
		return "", false
	}
	if c.rendered != "" && c.address == key && c.cols == cols && c.rows == rows && c.gen == gen {
		return c.rendered, true
	}

	out := renderImageBlocks(img, cols, rows)
	if out == "" {
		return "", false
	}

	avatarMu.Lock()
	if avatarGen == gen {
		avatarBlockCache = avatarBlockKey{address: key, cols: cols, rows: rows, gen: gen, rendered: out}
	}
	avatarMu.Unlock()
	return out, true
}

const ansiReset = "\x1b[m"

// writeHalfBlockCell emits one half-block cell: a truecolor foreground and
// background SGR pair followed by the block itself.
func writeHalfBlockCell(sb *strings.Builder, top, bottom color.RGBA) {
	sb.WriteString("\x1b[38;2;")
	writeRGB(sb, top)
	sb.WriteString(";48;2;")
	writeRGB(sb, bottom)
	sb.WriteByte('m')
	sb.WriteString(upperHalfBlock)
}

// writeSGRColor emits just one of the pair — 38 for foreground, 48 for
// background — when the other is unchanged from the previous cell.
func writeSGRColor(sb *strings.Builder, param int, c color.RGBA) {
	if param == 38 {
		sb.WriteString("\x1b[38;2;")
	} else {
		sb.WriteString("\x1b[48;2;")
	}
	writeRGB(sb, c)
	sb.WriteByte('m')
}

func writeRGB(sb *strings.Builder, c color.RGBA) {
	writeUint8(sb, c.R)
	sb.WriteByte(';')
	writeUint8(sb, c.G)
	sb.WriteByte(';')
	writeUint8(sb, c.B)
}

func writeUint8(sb *strings.Builder, v uint8) {
	if v >= 100 {
		sb.WriteByte('0' + v/100)
	}
	if v >= 10 {
		sb.WriteByte('0' + v/10%10)
	}
	sb.WriteByte('0' + v%10)
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

// contactColors holds the per-contact name tints from config, keyed by
// lowercased bare JID. Opt-in: a contact with no entry renders their name
// plain. Hashing a color for everybody was tried and reverted — it turns
// the list into a fruit salad and so stops any one row standing out, which
// was the entire point.
var (
	contactColorMu sync.RWMutex
	contactColors  = map[string]avatarColor{}
)

// SetContactColors replaces the configured per-contact tints. Values are
// "#rgb" or "#rrggbb"; anything else is skipped and named in the returned
// error, so a typo in config.toml is reported rather than silently
// dropping that contact's color.
func SetContactColors(colors map[string]string) error {
	parsed := make(map[string]avatarColor, len(colors))
	var bad []string
	for jid, hex := range colors {
		c, ok := parseHexColor(hex)
		if !ok {
			bad = append(bad, fmt.Sprintf("%s=%q", jid, hex))
			continue
		}
		parsed[strings.ToLower(jid)] = c
	}
	contactColorMu.Lock()
	contactColors = parsed
	contactColorMu.Unlock()

	avatarMu.Lock()
	avatarGen++ // any cached render may have used an old tint
	avatarMu.Unlock()

	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("invalid contact colors: %s", strings.Join(bad, ", "))
	}
	return nil
}

// contactColor is the tint configured for a contact, if any.
func contactColor(address string) (avatarColor, bool) {
	contactColorMu.RLock()
	defer contactColorMu.RUnlock()
	c, ok := contactColors[strings.ToLower(address)]
	return c, ok
}

// parseHexColor accepts "#rgb" and "#rrggbb", with or without the leading
// "#" and in either case.
func parseHexColor(s string) (avatarColor, bool) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "#"))
	switch len(s) {
	case 3:
		// "#abc" is "#aabbcc" — each digit doubled, not zero-padded.
		var expanded strings.Builder
		for _, r := range s {
			expanded.WriteRune(r)
			expanded.WriteRune(r)
		}
		s = expanded.String()
	case 6:
	default:
		return avatarColor{}, false
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return avatarColor{}, false
	}
	return avatarColor{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v)}, true
}

// renderTintedName colors a contact's name with the tint configured for
// them, and leaves it alone otherwise. Like presenceGlyph, a tinted result
// ends in its own reset — callers must not wrap it in an outer Foreground.
func renderTintedName(name, address string) string {
	if name == "" {
		return name
	}
	tint, ok := contactColor(address)
	if !ok {
		return name
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(tint.hex())).Render(name)
}

func (c avatarColor) hex() string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// encodePNG is png.Encode, named here so the test helper that writes
// fixture avatars doesn't need its own import of image/png alongside this
// file's decoder registration.
var encodePNG = png.Encode

// anyAvatarKnown reports whether any avatar image at all has been loaded.
// The sidebar's avatar panel reserves its rows only when this is true, so
// an install with no avatars anywhere never pays for an empty picture slot
// — and, unlike asking whether one particular contact has a picture, the
// answer doesn't change as chats are switched, which would resize the chat
// list underneath the selection on every move.
func anyAvatarKnown() bool {
	if !avatarsEnabled() {
		return false
	}
	avatarMu.RLock()
	defer avatarMu.RUnlock()
	return len(avatarPictures) > 0
}

// mustRenderAvatarPicture is renderAvatarPicture for callers that have
// already checked the contact has one (hasAvatarPicture). An empty result
// would be a caller bug, not a contact without an avatar.
func mustRenderAvatarPicture(address string, cols, rows int) string {
	block, _ := renderAvatarPicture(address, cols, rows)
	return block
}

// avatarGeneration is bumped every time any stored avatar changes; render
// caches key on it so they never have to compare images.
func avatarGeneration() uint64 {
	avatarMu.RLock()
	defer avatarMu.RUnlock()
	return avatarGen
}

// LoadAvatarFile records one contact's avatar from an image file on disk.
// The daemon fetches avatars over XMPP and caches them where this process
// can read them, so what crosses the socket is a path, not image bytes —
// see the daemon's avatars.go.
func LoadAvatarFile(jid, path string) error {
	img, err := decodeImageFile(path)
	if err != nil {
		return err
	}
	SetAvatarImage(jid, img)
	return nil
}

// RemoveAvatarImage forgets a contact's avatar — they stopped publishing
// one, so they go back to their monogram rather than keeping a picture
// they've taken down.
func RemoveAvatarImage(jid string) {
	key := strings.ToLower(jid)
	avatarMu.Lock()
	defer avatarMu.Unlock()
	delete(avatarPictures, key)
	avatarGen++
}
