package ui

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestAvatarInitial(t *testing.T) {
	tests := []struct {
		name    string
		address string
		want    string
	}{
		{"alice", "alice@localhost", "A"},
		{"", "bob@localhost", "B"},
		{"#kage", "kage@conference.localhost", "K"},
		{"#", "", "#"},
		{"…alice", "", "A"},
		{"7of9", "", "7"},
		{"", "", "?"},
		{"  ", "", "?"},
		{"Ünal", "", "Ü"},
	}
	for _, tt := range tests {
		if got := avatarInitial(tt.name, tt.address); got != tt.want {
			t.Errorf("avatarInitial(%q, %q) = %q, want %q", tt.name, tt.address, got, tt.want)
		}
	}
}

func TestHashColorStableAndDistinct(t *testing.T) {
	a := hashColor("alice@localhost")
	if again := hashColor("alice@localhost"); a != again {
		t.Fatalf("hashColor not stable: %v vs %v", a, again)
	}
	if upper := hashColor("Alice@LocalHost"); upper != a {
		t.Errorf("hashColor is case-sensitive: %v vs %v", upper, a)
	}
	if b := hashColor("bob@localhost"); b == a {
		t.Errorf("distinct JIDs hashed to the same color %v", a)
	}
}

// Fixed saturation/lightness is the whole reason hue is the only hashed
// dimension — no JID may land on a swatch too pale or too dark to read.
func TestHashColorAlwaysReadable(t *testing.T) {
	for _, jid := range []string{"a@x", "b@x", "c@x", "zzz@example.com", "42@y", "ünal@x"} {
		c := hashColor(jid)
		lum := 0.2126*float64(c.R) + 0.7152*float64(c.G) + 0.0722*float64(c.B)
		if lum < 40 || lum > 200 {
			t.Errorf("hashColor(%q) = %v has luminance %.0f, outside the readable band", jid, c, lum)
		}
	}
}

func TestReadableOn(t *testing.T) {
	if got := readableOn(avatarColor{20, 20, 20}); got != (avatarColor{255, 255, 255}) {
		t.Errorf("dark background got %v, want white text", got)
	}
	if got := readableOn(avatarColor{240, 240, 240}); got != (avatarColor{0, 0, 0}) {
		t.Errorf("light background got %v, want black text", got)
	}
}

// solidImage paints a w*h image in bg, then overwrites the left `greyCols`
// columns with a neutral grey.
func solidImage(w, h int, bg color.RGBA, greyCols int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := bg
			if x < greyCols {
				c = color.RGBA{128, 128, 128, 255}
			}
			img.Set(x, y, c)
		}
	}
	return img
}

func TestDominantColorSkipsGreys(t *testing.T) {
	// Grey covers three quarters of the image; the saturated quarter must
	// still win, or every photo of a person renders the same beige.
	img := solidImage(40, 10, color.RGBA{200, 40, 40, 255}, 30)
	got, ok := dominantColor(img)
	if !ok {
		t.Fatal("dominantColor reported no color for an image with a saturated region")
	}
	if got.R < 150 || got.G > 90 || got.B > 90 {
		t.Errorf("dominantColor = %v, want the red region", got)
	}
}

func TestDominantColorGreyscaleImage(t *testing.T) {
	img := solidImage(10, 10, color.RGBA{128, 128, 128, 255}, 10)
	if c, ok := dominantColor(img); ok {
		t.Errorf("dominantColor = %v, true for an all-grey image; want false so the JID hash supplies the color", c)
	}
}

func TestDominantColorIgnoresTransparent(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			if x < 8 {
				img.Set(x, y, color.RGBA{0, 0, 0, 0}) // transparent padding
				continue
			}
			img.Set(x, y, color.RGBA{40, 180, 60, 255})
		}
	}
	got, ok := dominantColor(img)
	if !ok {
		t.Fatal("dominantColor reported no color")
	}
	if got.G < 120 || got.R > 90 {
		t.Errorf("dominantColor = %v, want the opaque green region", got)
	}
}

func TestSetAvatarImageOverridesHash(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)

	const jid = "alice@localhost"
	SetAvatarImage(jid, solidImage(10, 10, color.RGBA{200, 40, 40, 255}, 0))
	got := avatarColorFor(jid)
	if got == hashColor(jid) {
		t.Fatal("avatarColorFor still returned the hash color after an image was set")
	}
	if got.R < 150 || got.G > 90 {
		t.Errorf("avatarColorFor = %v, want the image's red", got)
	}
	// Lookup is case-insensitive in both directions.
	if upper := avatarColorFor("Alice@LocalHost"); upper != got {
		t.Errorf("avatarColorFor is case-sensitive: %v vs %v", upper, got)
	}
}

// A greyscale avatar must leave the hash color in place rather than
// registering an unreadable grey swatch.
func TestSetAvatarImageKeepsHashForGreyscale(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)

	const jid = "bob@localhost"
	SetAvatarImage(jid, solidImage(10, 10, color.RGBA{128, 128, 128, 255}, 10))
	if got := avatarColorFor(jid); got != hashColor(jid) {
		t.Errorf("avatarColorFor = %v, want the hash color %v", got, hashColor(jid))
	}
}

func TestChatTitleTintsOnlyConfiguredContacts(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(func() {
		ClearAvatarImages()
		SetContactColors(nil)
	})
	if err := SetContactColors(map[string]string{"alice@localhost": "#7f9cf5"}); err != nil {
		t.Fatalf("SetContactColors: %v", err)
	}

	tinted := Chat{Name: "alice", Address: "alice@localhost", Presence: PresenceOnline}.Title()
	plainContact := Chat{Name: "alice", Address: "alice@example.com", Presence: PresenceOnline}.Title()

	if plain := ansi.Strip(tinted); plain != "● alice" {
		t.Errorf("Title() = %q, want %q — tinting must not add columns", plain, "● alice")
	}
	if !strings.Contains(tinted, "127;156;245") {
		t.Errorf("Title() = %q, missing the configured tint", tinted)
	}
	// A contact with no configured color is left alone: opt-in is the whole
	// point, since tinting everybody stops any one row standing out.
	if strings.Contains(plainContact, "\x1b[38;2;") {
		t.Errorf("Title() = %q for an unconfigured contact, want the name untinted", plainContact)
	}
	if lipgloss.Width(tinted) != lipgloss.Width(ansi.Strip(tinted)) {
		t.Errorf("Title() measures %d columns but prints %d", lipgloss.Width(tinted), lipgloss.Width(ansi.Strip(tinted)))
	}
}

func TestParseHexColor(t *testing.T) {
	tests := []struct {
		in     string
		want   avatarColor
		wantOK bool
	}{
		{"#7f9cf5", avatarColor{0x7f, 0x9c, 0xf5}, true},
		{"7f9cf5", avatarColor{0x7f, 0x9c, 0xf5}, true},
		{"#7F9CF5", avatarColor{0x7f, 0x9c, 0xf5}, true},
		{"  #7f9cf5  ", avatarColor{0x7f, 0x9c, 0xf5}, true},
		{"#abc", avatarColor{0xaa, 0xbb, 0xcc}, true}, // doubled, not zero-padded
		{"#000", avatarColor{0, 0, 0}, true},
		{"#fff", avatarColor{255, 255, 255}, true},
		{"", avatarColor{}, false},
		{"#", avatarColor{}, false},
		{"#12345", avatarColor{}, false},
		{"#1234567", avatarColor{}, false},
		{"#gggggg", avatarColor{}, false},
		{"rebeccapurple", avatarColor{}, false},
	}
	for _, tt := range tests {
		got, ok := parseHexColor(tt.in)
		if ok != tt.wantOK || got != tt.want {
			t.Errorf("parseHexColor(%q) = (%v, %v), want (%v, %v)", tt.in, got, ok, tt.want, tt.wantOK)
		}
	}
}

// A typo in one entry must be reported, not silently drop that contact's
// color, and must not take the valid entries down with it.
func TestSetContactColorsReportsBadValues(t *testing.T) {
	t.Cleanup(func() { SetContactColors(nil) })

	err := SetContactColors(map[string]string{
		"alice@localhost": "#7f9cf5",
		"bob@localhost":   "not-a-color",
		"carol@localhost": "#12345",
	})
	if err == nil {
		t.Fatal("SetContactColors accepted invalid colors")
	}
	for _, want := range []string{"bob@localhost", "carol@localhost"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
	if strings.Contains(err.Error(), "alice@localhost") {
		t.Errorf("error %q names the valid entry", err)
	}
	if _, ok := contactColor("alice@localhost"); !ok {
		t.Error("a valid entry was dropped along with the invalid ones")
	}
	if _, ok := contactColor("bob@localhost"); ok {
		t.Error("an invalid entry was kept")
	}
}

// A configured color also drives the panel's monogram, and outranks the
// color derived from the contact's own avatar image — the user said what
// they wanted.
func TestContactColorOutranksImageColor(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(func() {
		ClearAvatarImages()
		SetContactColors(nil)
	})

	SetAvatarImage("alice@localhost", solidImage(32, 32, color.RGBA{220, 30, 30, 255}, 0))
	if got := avatarColorFor("alice@localhost"); got.R < 150 {
		t.Fatalf("avatarColorFor = %v before any configured color, want the image red", got)
	}
	if err := SetContactColors(map[string]string{"ALICE@localhost": "#7f9cf5"}); err != nil {
		t.Fatal(err)
	}
	if got, want := avatarColorFor("alice@localhost"), (avatarColor{0x7f, 0x9c, 0xf5}); got != want {
		t.Errorf("avatarColorFor = %v, want the configured %v (lookup is case-insensitive)", got, want)
	}
}

func TestLoadAvatarDir(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)

	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "alice@localhost.png"), color.RGBA{200, 40, 40, 255})
	writePNG(t, filepath.Join(dir, "notes.txt"), color.RGBA{})
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me"), 0o644); err != nil {
		t.Fatal(err)
	}

	n, err := LoadAvatarDir(dir)
	if err != nil {
		t.Fatalf("LoadAvatarDir: %v", err)
	}
	if n != 1 {
		t.Errorf("loaded %d avatars, want 1 (non-image files skipped)", n)
	}
	if got := avatarColorFor("alice@localhost"); got.R < 150 {
		t.Errorf("avatarColorFor = %v, want the loaded image's red", got)
	}
}

func TestLoadAvatarDirMissingIsNotAnError(t *testing.T) {
	n, err := LoadAvatarDir(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Errorf("LoadAvatarDir on a missing directory: %v", err)
	}
	if n != 0 {
		t.Errorf("loaded %d avatars from a missing directory", n)
	}
}

func writePNG(t *testing.T, path string, c color.RGBA) {
	t.Helper()
	if filepath.Ext(path) != ".png" {
		return
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := encodePNG(f, solidImage(8, 8, c, 0)); err != nil {
		t.Fatal(err)
	}
}

// Callers lay the block out against the size they asked for, so the
// rendered result has to be exactly that many rows of exactly that many
// columns.
func TestRenderAvatarPictureDimensions(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)

	const jid = "alice@localhost"
	SetAvatarImage(jid, solidImage(64, 64, color.RGBA{200, 40, 40, 255}, 32))

	block, ok := renderAvatarPicture(jid, 8, 4)
	if !ok {
		t.Fatal("renderAvatarPicture reported no picture for a contact with one")
	}
	lines := strings.Split(block, "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d rows, want 4", len(lines))
	}
	for i, line := range lines {
		if got := lipgloss.Width(line); got != 8 {
			t.Errorf("row %d width = %d, want 8", i, got)
		}
	}
}

// The left half of the fixture is grey and the right half red; the rendered
// block must preserve that split rather than smearing one color across.
func TestRenderAvatarPictureCarriesTheImage(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)

	const jid = "alice@localhost"
	SetAvatarImage(jid, solidImage(64, 64, color.RGBA{220, 30, 30, 255}, 32))

	block, ok := renderAvatarPicture(jid, 4, 1)
	if !ok {
		t.Fatal("renderAvatarPicture reported no picture")
	}
	if !strings.Contains(block, "128;128;128") {
		t.Errorf("block %q lost the grey half of the image", block)
	}
	if !strings.Contains(block, "220;30;30") {
		t.Errorf("block %q lost the red half of the image", block)
	}
}

func TestRenderAvatarPictureWithoutImage(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)

	if _, ok := renderAvatarPicture("nobody@localhost", 8, 4); ok {
		t.Error("renderAvatarPicture reported a picture for a contact with none")
	}
	if hasAvatarPicture("nobody@localhost") {
		t.Error("hasAvatarPicture = true for a contact with no image")
	}
}

func TestRenderAvatarPictureZeroSize(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)

	SetAvatarImage("alice@localhost", solidImage(8, 8, color.RGBA{200, 40, 40, 255}, 0))
	if _, ok := renderAvatarPicture("alice@localhost", 0, 3); ok {
		t.Error("renderAvatarPicture accepted zero columns")
	}
	if _, ok := renderAvatarPicture("alice@localhost", 6, 0); ok {
		t.Error("renderAvatarPicture accepted zero rows")
	}
}

// A greyscale avatar yields no swatch color, but the picture itself must
// still be kept — the header draws the image, not the swatch.
func TestGreyscaleAvatarStillStoresPicture(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)

	const jid = "bob@localhost"
	SetAvatarImage(jid, solidImage(16, 16, color.RGBA{128, 128, 128, 255}, 16))
	if !hasAvatarPicture(jid) {
		t.Error("greyscale avatar was not stored as a picture")
	}
	if got := avatarColorFor(jid); got != hashColor(jid) {
		t.Errorf("avatarColorFor = %v, want the hash color %v", got, hashColor(jid))
	}
}

func TestDownscaleBounds(t *testing.T) {
	small := solidImage(8, 8, color.RGBA{200, 40, 40, 255}, 0)
	if got := downscale(small, avatarStoredSize); got != small {
		t.Error("downscale copied an image already within the bound")
	}
	wide := downscale(solidImage(400, 200, color.RGBA{200, 40, 40, 255}, 0), 64)
	if b := wide.Bounds(); b.Dx() != 64 || b.Dy() != 32 {
		t.Errorf("downscale(400x200) = %dx%d, want 64x32 (aspect preserved)", b.Dx(), b.Dy())
	}
	tall := downscale(solidImage(200, 400, color.RGBA{200, 40, 40, 255}, 0), 64)
	if b := tall.Bounds(); b.Dx() != 32 || b.Dy() != 64 {
		t.Errorf("downscale(200x400) = %dx%d, want 32x64 (aspect preserved)", b.Dx(), b.Dy())
	}
}

// A contact who publishes no avatar gets the monogram, not somebody
// else's picture — see LoadAvatarDir on why there is no shared fallback.
func TestContactWithoutAvatarHasNoPicture(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)

	SetAvatarImage("alice@localhost", solidImage(32, 32, color.RGBA{220, 30, 30, 255}, 0))
	if hasAvatarPicture("stranger@example.com") {
		t.Error("a contact with no avatar of their own was given a picture")
	}
	if _, ok := renderAvatarPicture("stranger@example.com", 6, 3); ok {
		t.Error("renderAvatarPicture served a picture to a contact without one")
	}
	if got := avatarColorFor("stranger@example.com"); got != hashColor("stranger@example.com") {
		t.Errorf("avatarColorFor = %v, want the JID hash %v", got, hashColor("stranger@example.com"))
	}
}

// "default.png" used to be a magic name for a shared fallback; it is now
// just a contact like any other, so nothing inherits it.
func TestLoadAvatarDirHasNoMagicNames(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)

	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "default.png"), color.RGBA{40, 120, 200, 255})
	writePNG(t, filepath.Join(dir, "alice@localhost.png"), color.RGBA{220, 30, 30, 255})

	n, err := LoadAvatarDir(dir)
	if err != nil {
		t.Fatalf("LoadAvatarDir: %v", err)
	}
	if n != 2 {
		t.Errorf("loaded %d avatars, want 2", n)
	}
	if hasAvatarPicture("stranger@example.com") {
		t.Error("a contact inherited default.png")
	}
	if !hasAvatarPicture("default") {
		t.Error("default.png was not loaded as its own contact")
	}
}

// The render cache is keyed by size as well as contact, or a resize would
// keep handing back the previous size's block.
func TestAvatarPictureCacheKeyedBySize(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)
	SetAvatarImage("alice@localhost", solidImage(64, 64, color.RGBA{200, 40, 40, 255}, 0))

	small, ok := renderAvatarPicture("alice@localhost", 4, 2)
	if !ok {
		t.Fatal("no picture at 4x2")
	}
	large, ok := renderAvatarPicture("alice@localhost", 10, 5)
	if !ok {
		t.Fatal("no picture at 10x5")
	}
	if got := len(strings.Split(small, "\n")); got != 2 {
		t.Errorf("4x2 render has %d rows, want 2", got)
	}
	if got := len(strings.Split(large, "\n")); got != 5 {
		t.Errorf("10x5 render has %d rows, want 5", got)
	}
	if again, _ := renderAvatarPicture("alice@localhost", 4, 2); again != small {
		t.Error("re-rendering the original size did not reproduce it")
	}
}

// Nor may it survive the avatar itself changing.
func TestAvatarPictureCacheInvalidatedOnNewImage(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)

	const jid = "alice@localhost"
	SetAvatarImage(jid, solidImage(64, 64, color.RGBA{220, 30, 30, 255}, 0))
	before, _ := renderAvatarPicture(jid, 6, 3)

	SetAvatarImage(jid, solidImage(64, 64, color.RGBA{30, 220, 60, 255}, 0))
	after, _ := renderAvatarPicture(jid, 6, 3)

	if before == after {
		t.Error("render cache returned the old picture after the avatar changed")
	}
	if !strings.Contains(after, "30;220;60") {
		t.Errorf("block %q is not the new avatar", after)
	}
}

// One contact's cached block must not be served to another.
func TestAvatarPictureCacheKeyedByContact(t *testing.T) {
	ClearAvatarImages()
	t.Cleanup(ClearAvatarImages)
	SetAvatarImage("alice@localhost", solidImage(64, 64, color.RGBA{220, 30, 30, 255}, 0))
	SetAvatarImage("bob@localhost", solidImage(64, 64, color.RGBA{40, 120, 200, 255}, 0))

	alice, _ := renderAvatarPicture("alice@localhost", 6, 3)
	bob, _ := renderAvatarPicture("bob@localhost", 6, 3)
	if alice == bob {
		t.Error("two contacts with different avatars rendered identically")
	}
	if !strings.Contains(bob, "40;120;200") {
		t.Errorf("block %q is not bob's avatar", bob)
	}
}
