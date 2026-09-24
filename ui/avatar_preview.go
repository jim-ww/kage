package ui

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Avatar preview: the confirmation step between picking a file and
// publishing it as this account's avatar (see avatar_menu.go).
//
// Publishing is network-visible and hard to take back — every contact
// fetches whatever lands on the PEP node — and a file picker shows only a
// name, so the file is confirmed as a picture rather than as a filename.
// The picker stays open behind the preview: rejecting one candidate lands
// back in the same directory to pick another, instead of reopening the
// menu from the account bar.
//
// Rendered with the same half-block cells as contact avatars
// (renderImageBlocks), so what the preview shows is what the chat list and
// the avatar panel will show.

// avatarPreviewMaxEdge is the edge length the picked image is downscaled
// to before rendering. Larger than avatarStoredSize because the preview is
// the biggest an avatar is ever drawn — it gets the whole chat area rather
// than a sidebar's width — and it's one decode per picked file, not
// something held per contact.
const avatarPreviewMaxEdge = 192

// avatarPreviewMinCols is the narrowest preview worth drawing; below it a
// picture stops resolving (same reasoning as avatarPanelMinCols) and the
// popup shows the file's details alone.
const avatarPreviewMinCols = 12

// avatarPreviewState is the picked-but-not-yet-published candidate. img is
// nil until the decode lands — or forever, if it failed; err says why.
type avatarPreviewState struct {
	accountIdx int
	path       string
	img        image.Image
	width      int // source pixel dimensions, before downscaling
	height     int
	size       int64 // file size, which is what the server's limit applies to
	format     string
	err        string
	loading    bool
}

// avatarPreviewLoadedMsg carries the decoded candidate back to the UI
// goroutine. Decoding runs as a command because a multi-megapixel JPEG
// takes long enough to drop frames if it ran inline in Update.
type avatarPreviewLoadedMsg struct {
	path   string
	img    image.Image
	width  int
	height int
	size   int64
	format string
	err    error
}

// startAvatarPreview stages a picked file for confirmation.
func (m *Model) startAvatarPreview(accountIdx int, path string) tea.Cmd {
	m.avatarPreview = &avatarPreviewState{accountIdx: accountIdx, path: path, loading: true}
	return loadAvatarPreviewCmd(path)
}

func loadAvatarPreviewCmd(path string) tea.Cmd {
	return func() tea.Msg {
		out := avatarPreviewLoadedMsg{path: path}
		info, err := os.Stat(path)
		if err != nil {
			out.err = err
			return out
		}
		out.size = info.Size()
		f, err := os.Open(path)
		if err != nil {
			out.err = err
			return out
		}
		defer f.Close()
		img, format, err := image.Decode(f)
		if err != nil {
			out.err = fmt.Errorf("not a usable image: %w", err)
			return out
		}
		// Only PNG and JPEG can be published (see the daemon's
		// SetOwnAvatar), so anything else is refused here rather than
		// after the user confirms. image.Decode is registered for both by
		// the imports avatar.go already carries.
		if format != "png" && format != "jpeg" {
			out.err = fmt.Errorf("%s images cannot be published; use PNG or JPEG", format)
			return out
		}
		b := img.Bounds()
		out.width, out.height = b.Dx(), b.Dy()
		out.format = format
		out.img = downscale(img, avatarPreviewMaxEdge)
		return out
	}
}

// applyAvatarPreviewLoaded folds a finished decode into the staged
// candidate, ignoring one for a file that is no longer the candidate (the
// preview was canceled, or another file was picked while it decoded).
func (m *Model) applyAvatarPreviewLoaded(msg avatarPreviewLoadedMsg) {
	if m.avatarPreview == nil || m.avatarPreview.path != msg.path {
		return
	}
	m.avatarPreview.loading = false
	m.avatarPreview.size = msg.size
	if msg.err != nil {
		m.avatarPreview.err = msg.err.Error()
		return
	}
	m.avatarPreview.img = msg.img
	m.avatarPreview.width, m.avatarPreview.height = msg.width, msg.height
	m.avatarPreview.format = msg.format
}

// updateAvatarPreviewKey handles input while the preview is up. Confirming
// publishes and closes the picker with it; rejecting drops the candidate
// and leaves the picker where it was.
func (m Model) updateAvatarPreviewKey(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	preview := m.avatarPreview
	switch {
	case matchesKey(msg, m.keys.ConfirmYes), matchesKey(msg, m.keys.SelectSend):
		if preview.img == nil {
			// Nothing to confirm: the file is still decoding, or it never
			// will decode. Publishing it would only fail in the daemon.
			return m, nil, true
		}
		m.avatarPreview = nil
		m.pickingFile = false
		m.pickingAvatar = false
		return m, m.setOwnAvatarCmd(preview.accountIdx, preview.path), true
	case matchesKey(msg, m.keys.ConfirmNo), matchesKey(msg, m.keys.Back):
		m.avatarPreview = nil
	}
	return m, nil, true
}

// avatarPreviewSize is the picture's size in cells: as large as the popup
// can be given the chat area, square (a cell carries two pixels
// vertically, so rows are half of cols). Zero when there's no room, in
// which case the popup shows the file's details without a picture.
func (m Model) avatarPreviewSize() (cols, rows int) {
	// The popup's own chrome: border and padding, the title, the two
	// detail lines, and the footer.
	const chromeRows = 10
	const chromeCols = 12
	maxRows := m.height - m.inputAreaHeight() - chromeRows
	cols = min(m.chatAreaWidth()-chromeCols, maxRows*2)
	cols -= cols % 2 // an odd width can't split into whole pixel rows
	if cols < avatarPreviewMinCols {
		return 0, 0
	}
	return cols, cols / 2
}

// renderAvatarPreviewPopup draws the candidate over the file picker.
func (m Model) renderAvatarPreviewPopup() string {
	cw := m.chatAreaWidth()
	vh := m.height - m.inputAreaHeight()
	preview := m.avatarPreview

	var rows []string
	switch {
	case preview.loading:
		rows = append(rows, "reading "+filepath.Base(preview.path)+"…")
	case preview.err != "":
		rows = append(rows, m.styles.popupDanger.Render(filepath.Base(preview.path)+": "+preview.err))
	default:
		if cols, pictureRows := m.avatarPreviewSize(); cols > 0 {
			rows = append(rows, renderImageBlocks(preview.img, cols, pictureRows), "")
		}
		rows = append(rows,
			filepath.Base(preview.path),
			m.styles.messageReply.Render(fmt.Sprintf("%s · %d×%d · %s",
				strings.ToUpper(preview.format), preview.width, preview.height, humanBytes(preview.size))),
		)
	}

	footer := "[esc] pick another"
	if preview.img != nil {
		footer = "[enter/y] publish · " + footer
	}
	body := m.styles.listPopup("Publish as avatar?", rows, footer)
	popup := m.zone.Mark(zoneAvatarPreviewPopup, m.styles.popupDialog(m.styles.colors.borderA, body))
	return lipgloss.Place(cw, vh, lipgloss.Center, lipgloss.Center, popup)
}
