package ui

import (
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func avatarPreviewModel(t *testing.T, pub *fakeAvatarPublisher) Model {
	t.Helper()
	m := avatarMenuModel(t, pub)
	m.width, m.height, m.termHeight = 120, 40, 40
	m.updateSizes()
	return m
}

// writeTestPNG writes a solid-color square named face.png and returns its
// path. Flat color, so even a huge one stays a small file — which is the
// case the size checks have to survive.
func writeTestPNG(t *testing.T, edge int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "face.png")
	writeSquarePNG(t, path, edge)
	return path
}

func writeSquarePNG(t *testing.T, path string, edge int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, edge, edge))
	for y := 0; y < edge; y++ {
		for x := 0; x < edge; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 40, B: 90, A: 255})
		}
	}
	writePNGFile(t, path, img)
}

// writeNoisyPNG writes a square of incompressible noise — the way to get a
// file genuinely over the publishable size without writing tens of
// megapixels.
func writeNoisyPNG(t *testing.T, edge int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, edge, edge))
	rng := rand.New(rand.NewSource(1))
	for y := 0; y < edge; y++ {
		for x := 0; x < edge; x++ {
			img.Set(x, y, color.RGBA{uint8(rng.Intn(256)), uint8(rng.Intn(256)), uint8(rng.Intn(256)), 255})
		}
	}
	path := filepath.Join(t.TempDir(), "noisy.png")
	writePNGFile(t, path, img)
	return path
}

// previewOf is the open avatar preview, or nil.
func previewOf(m Model) *avatarPreviewBody {
	if md := m.modals.Find(avatarPreviewModalID); md != nil {
		return md.Body.(*avatarPreviewBody)
	}
	return nil
}

// pickerOpen reports whether the file picker is open.
func pickerOpen(m Model) bool { return m.modals.Find(filePickerModalID) != nil }

// pickerRowZone is the zone of the open file picker's row i.
func pickerRowZone(m Model, i int) string {
	return m.modals.Find(filePickerModalID).Body.(*filePickerBody).rowID(i)
}

// loadedFrom runs startAvatarPreview's command and returns its decode.
func loadedFrom(t *testing.T, cmd tea.Cmd) avatarPreviewLoadedMsg {
	t.Helper()
	loaded, ok := findMsg[avatarPreviewLoadedMsg](cmd)
	if !ok {
		t.Fatal("startAvatarPreview produced no load")
	}
	return loaded
}

func writePNGFile(t *testing.T, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// Staging a candidate must not publish anything: that's the whole point of
// the preview.
func TestAvatarPreviewStagesWithoutPublishing(t *testing.T) {
	pub := &fakeAvatarPublisher{}
	m := avatarPreviewModel(t, pub)
	m.openFilePicker(true)

	cmd := m.startAvatarPreview(0, writeTestPNG(t, 32))
	if cmd == nil {
		t.Fatal("startAvatarPreview produced no load command")
	}
	if previewOf(m) == nil || !previewOf(m).loading {
		t.Fatalf("preview = %+v, want a loading candidate", previewOf(m))
	}
	if pub.setCalls != 0 {
		t.Errorf("SetOwnAvatar called %d times before confirmation, want 0", pub.setCalls)
	}
	// The picker stays open behind the preview, so rejecting the candidate
	// lands back in the same directory.
	if !pickerOpen(m) {
		t.Error("the picker closed under the preview")
	}
}

func TestAvatarPreviewLoadDecodesTheFile(t *testing.T) {
	m := avatarPreviewModel(t, &fakeAvatarPublisher{})
	path := writeTestPNG(t, 40)
	cmd := m.startAvatarPreview(0, path)

	loaded := loadedFrom(t, cmd)
	if loaded.err != nil {
		t.Fatalf("decoding a PNG failed: %v", loaded.err)
	}
	next := updated(t, m, loaded)
	if previewOf(next) == nil || previewOf(next).img == nil {
		t.Fatalf("preview = %+v, want a decoded image", previewOf(next))
	}
	p := previewOf(next)
	if p.loading || p.err != "" {
		t.Errorf("preview = %+v, want it loaded and error-free", p)
	}
	if p.format != "png" || p.width != 40 || p.height != 40 {
		t.Errorf("preview = %s %dx%d, want png 40x40", p.format, p.width, p.height)
	}
	if p.size <= 0 {
		t.Errorf("preview size = %d, want the file's size", p.size)
	}
}

// Enter/y is the only thing that reaches the network, and it closes the
// picker with it — there's nothing to pick a second one of.
func TestAvatarPreviewConfirmPublishes(t *testing.T) {
	pub := &fakeAvatarPublisher{}
	m := avatarPreviewModel(t, pub)
	m.openFilePicker(true)
	path := writeTestPNG(t, 32)
	cmd := m.startAvatarPreview(0, path)
	m = updated(t, m, loadedFrom(t, cmd))

	next, pubCmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := next.(Model)
	if previewOf(got) != nil {
		t.Error("the preview stayed open after confirming")
	}
	if pickerOpen(got) {
		t.Error("the picker stayed open after publishing")
	}
	if pubCmd == nil {
		t.Fatal("confirming produced no publish command")
	}
	if !containsAvatarPublished(pubCmd) {
		t.Fatalf("confirm command returned %T, want AvatarPublishedMsg", pubCmd())
	}
	if pub.setCalls != 1 || pub.setPath != path {
		t.Errorf("SetOwnAvatar got (%q, %d calls), want (%q, 1)", pub.setPath, pub.setCalls, path)
	}
}

// Rejecting a candidate goes back to the picker, not out of it.
func TestAvatarPreviewRejectKeepsThePickerOpen(t *testing.T) {
	pub := &fakeAvatarPublisher{}
	m := avatarPreviewModel(t, pub)
	m.openFilePicker(true)
	cmd := m.startAvatarPreview(0, writeTestPNG(t, 32))
	m = updated(t, m, loadedFrom(t, cmd))

	next := updated(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if previewOf(next) != nil {
		t.Error("esc left the preview open")
	}
	if !pickerOpen(next) {
		t.Error("esc on the preview closed the picker too")
	}
	if pub.setCalls != 0 {
		t.Errorf("SetOwnAvatar called %d times on a rejected candidate, want 0", pub.setCalls)
	}
}

// A file the daemon would refuse is refused here instead, before anything
// is published.
func TestAvatarPreviewRefusesUnpublishableFormats(t *testing.T) {
	pub := &fakeAvatarPublisher{}
	m := avatarPreviewModel(t, pub)

	path := filepath.Join(t.TempDir(), "face.gif")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewPaletted(image.Rect(0, 0, 8, 8), color.Palette{color.Black, color.White})
	if err := gif.Encode(f, img, nil); err != nil {
		t.Fatal(err)
	}
	f.Close()

	cmd := m.startAvatarPreview(0, path)
	m = updated(t, m, loadedFrom(t, cmd))
	if previewOf(m) == nil || previewOf(m).err == "" {
		t.Fatalf("preview = %+v, want a refusal", previewOf(m))
	}
	if previewOf(m).img != nil {
		t.Error("a GIF was staged as publishable")
	}

	next := updated(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if pub.setCalls != 0 {
		t.Errorf("SetOwnAvatar called %d times for an unpublishable file, want 0", pub.setCalls)
	}
	if previewOf(next) == nil {
		t.Error("enter dismissed a preview it could not act on")
	}
	rendered := ansi.Strip(next.modals.View())
	if !strings.Contains(rendered, "use PNG or") || !strings.Contains(rendered, "JPEG") {
		t.Errorf("popup %q does not say why the file was refused", rendered)
	}
}

// Nothing decodes instantly; a confirmation that raced the decode must not
// publish an unverified file.
func TestAvatarPreviewConfirmWaitsForTheDecode(t *testing.T) {
	pub := &fakeAvatarPublisher{}
	m := avatarPreviewModel(t, pub)
	m.startAvatarPreview(0, writeTestPNG(t, 32))

	next := updated(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if pub.setCalls != 0 {
		t.Errorf("SetOwnAvatar called %d times while still loading, want 0", pub.setCalls)
	}
	if previewOf(next) == nil {
		t.Error("enter dropped the candidate that was still loading")
	}
}

// A decode that lands after its candidate is gone belongs to nobody.
func TestAvatarPreviewIgnoresStaleLoads(t *testing.T) {
	m := avatarPreviewModel(t, &fakeAvatarPublisher{})
	loaded := loadedFrom(t, m.startAvatarPreview(0, writeTestPNG(t, 32)))

	m.modals.Close()
	m.startAvatarPreview(0, "/other/face.png")
	next := updated(t, m, loaded)
	if previewOf(next).img != nil || !previewOf(next).loading {
		t.Errorf("preview = %+v, want the other candidate untouched", previewOf(next))
	}

	m.modals.Close()
	if next := updated(t, m, loaded); previewOf(next) != nil {
		t.Error("a canceled preview was resurrected by its own load")
	}
}

// The preview is the picture, not a filename: it has to actually render
// the image, alongside what would be published.
func TestAvatarPreviewRendersThePicture(t *testing.T) {
	m := avatarPreviewModel(t, &fakeAvatarPublisher{})
	cmd := m.startAvatarPreview(0, writeTestPNG(t, 64))
	m = updated(t, m, loadedFrom(t, cmd))

	raw := m.modals.View()
	if !strings.Contains(raw, upperHalfBlock) {
		t.Error("the popup rendered no picture cells")
	}
	rendered := ansi.Strip(raw)
	for _, want := range []string{"Publish as avatar?", "face.png", "PNG", "64×64", "publish"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("popup %q is missing %q", rendered, want)
		}
	}
}

// The popup is what's on screen while it's up, over the picker it was
// opened from.
func TestAvatarPreviewIsWhatTheChatAreaShows(t *testing.T) {
	m := avatarPreviewModel(t, &fakeAvatarPublisher{})
	m.openFilePicker(true)
	cmd := m.startAvatarPreview(0, writeTestPNG(t, 32))
	m = updated(t, m, loadedFrom(t, cmd))

	if !m.popupActive() {
		t.Error("popupActive() is false while the preview is up")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "Publish as avatar?") {
		t.Error("the view does not show the preview")
	}
}

// A terminal too small for a picture still shows the file's details rather
// than a smear of cells.
func TestAvatarPreviewSizeGivesUpWhenTiny(t *testing.T) {
	if cols, rows := avatarPreviewSize(64, 64, 8, 2); cols != 0 || rows != 0 {
		t.Errorf("avatarPreviewSize(64, 64) = (%d, %d) in a tiny terminal, want (0, 0)", cols, rows)
	}
	if cols, rows := avatarPreviewSize(0, 0, 100, 30); cols != 0 || rows != 0 {
		t.Errorf("avatarPreviewSize(0, 0) = (%d, %d), want (0, 0)", cols, rows)
	}
}

// The preview's job is to show what the file is, so it keeps the file's
// proportions instead of stretching everything into a square.
func TestAvatarPreviewSizeKeepsTheAspectRatio(t *testing.T) {
	const spaceCols, spaceRows = 100, 30
	maxCols, maxRows := avatarPreviewSize(64, 64, spaceCols, spaceRows)
	if maxCols == 0 {
		t.Fatal("no room for a picture in a 120x40 terminal")
	}
	if maxRows != maxCols/2 {
		t.Errorf("a square image got %dx%d cells, want rows to be half of cols", maxCols, maxRows)
	}

	// A banner is drawn as a banner: it may use width the square couldn't,
	// but it is nowhere near as tall.
	wideCols, wideRows := avatarPreviewSize(4000, 200, spaceCols, spaceRows)
	if wideRows >= maxRows {
		t.Errorf("a 20:1 image got %d rows, want fewer than a square image's %d", wideRows, maxRows)
	}
	if wideRows < 1 {
		t.Errorf("a 20:1 image got %d rows, want at least one", wideRows)
	}
	// A cell is two pixels tall, so the drawn aspect ratio is cols/(2*rows).
	if drawn := float64(wideCols) / float64(2*wideRows); drawn < 15 || drawn > 25 {
		t.Errorf("a 20:1 image drew at %.1f:1 (%dx%d cells), want roughly 20:1", drawn, wideCols, wideRows)
	}

	// A tall image gives up width rather than overflowing the popup.
	tallCols, tallRows := avatarPreviewSize(200, 4000, spaceCols, spaceRows)
	if tallCols >= maxCols {
		t.Errorf("a 1:20 image got %d cols, want fewer than a square image's %d", tallCols, maxCols)
	}
	if tallRows > maxRows {
		t.Errorf("a 1:20 image got %d rows, want at most the %d that fit", tallRows, maxRows)
	}
}

// Decoding costs in proportion to pixels, not to file size, and a
// pathological image compresses to nothing: an 8000x8000 PNG of flat color
// is well under the publishable size yet a quarter of a gigabyte decoded.
// It has to be refused from the header, before that allocation happens.
func TestAvatarPreviewRefusesHumongousImages(t *testing.T) {
	m := avatarPreviewModel(t, &fakeAvatarPublisher{})
	path := writeTestPNG(t, 4097) // 16.8 megapixels, just over the cap

	loaded := loadAvatarPreviewCmd(path)().(avatarPreviewLoadedMsg)
	if loaded.err == nil {
		t.Fatal("a 4097x4097 image was accepted for preview")
	}
	if loaded.img != nil {
		t.Error("the image was decoded despite being refused")
	}
	// The dimensions come from the header, so the popup can still say what
	// the file was.
	if loaded.width != 4097 || loaded.height != 4097 {
		t.Errorf("refusal reports %dx%d, want the header's 4097x4097", loaded.width, loaded.height)
	}

	m.startAvatarPreview(0, path)
	m = updated(t, m, loaded)
	if !strings.Contains(ansi.Strip(m.modals.View()), "too large") {
		t.Error("the popup does not say the image was too large")
	}
}

// The publishable-size limit is the daemon's, but learning it after
// confirming means the publish failed for a reason the preview already
// knew.
func TestAvatarPreviewRefusesOversizeFiles(t *testing.T) {
	m := avatarPreviewModel(t, &fakeAvatarPublisher{})
	path := writeNoisyPNG(t, 1400) // noise doesn't compress: well over 1 MiB

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= AvatarMaxBytes {
		t.Fatalf("test file is %d bytes, want one over the %d limit", info.Size(), AvatarMaxBytes)
	}

	m.startAvatarPreview(0, path)
	loaded := loadAvatarPreviewCmd(path)().(avatarPreviewLoadedMsg)
	if loaded.err == nil {
		t.Fatal("an oversize file was accepted for preview")
	}
	if loaded.img != nil {
		t.Error("an unpublishable file was decoded anyway")
	}
	m = updated(t, m, loaded)
	rendered := ansi.Strip(m.modals.View())
	if !strings.Contains(rendered, "limit") {
		t.Errorf("popup %q does not mention the size limit", rendered)
	}
	if strings.Contains(rendered, "publish") {
		t.Error("the popup offers to publish a file that cannot be published")
	}
}

// Every line but the picture is truncated: the picture is what sets the
// dialog's width, and a long name or a verbose decoder error must not push
// it past the terminal's edge.
func TestAvatarPreviewPopupStaysInsideTheChatArea(t *testing.T) {
	m := avatarPreviewModel(t, &fakeAvatarPublisher{})
	long := strings.Repeat("verylongname", 15) + ".png"

	widthOK := func(t *testing.T, m Model, state string) {
		t.Helper()
		raw := m.modals.View()
		if w := lipgloss.Width(raw); w > m.chatAreaWidth() {
			t.Errorf("%s: popup is %d wide, want at most the chat area's %d", state, w, m.chatAreaWidth())
		}
		if !strings.Contains(ansi.Strip(raw), "…") {
			t.Errorf("%s: the long filename was not truncated", state)
		}
	}

	notAnImage := filepath.Join(t.TempDir(), long)
	if err := os.WriteFile(notAnImage, []byte("not an image at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Measured while still loading, once the decode has failed, and with a
	// real picture — the name is too long in all three states.
	m.startAvatarPreview(0, notAnImage)
	widthOK(t, m, "loading")

	m = updated(t, m, loadAvatarPreviewCmd(notAnImage)())
	widthOK(t, m, "failed")

	good := filepath.Join(t.TempDir(), long)
	writeSquarePNG(t, good, 64)
	m.startAvatarPreview(0, good)
	m = updated(t, m, loadAvatarPreviewCmd(good)())
	if previewOf(m).img == nil {
		t.Fatalf("preview = %+v, want a decoded image", previewOf(m))
	}
	widthOK(t, m, "loaded")
}
