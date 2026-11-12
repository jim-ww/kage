package ui

import (
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jim-ww/kage/ui/modal"
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
// (renderImageBlocks), at the picked file's own aspect ratio — see
// avatarPreviewSize.

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

// avatarPreviewTextWidth caps the popup's text lines. The picture is what
// sets the dialog's width; a filename or a decoder error is truncated to
// this rather than allowed to stretch it.
const avatarPreviewTextWidth = 60

// avatarPreviewMaxPixels is the largest image the preview will decode.
// Decoding is what a pathological file costs — every pixel becomes four
// bytes of heap regardless of how well it compressed on disk — and a file
// small enough to publish can still be tens of megapixels. 16 megapixels
// is roughly 64MB decoded: far beyond any avatar, and still far short of
// hurting.
const avatarPreviewMaxPixels = 16 << 20

// AvatarMaxBytes is the largest avatar that can be published, mirroring
// xmpp.AvatarMaxBytes so the preview can refuse an oversized file before
// the user commits to it — ui doesn't import xmpp. The two are pinned
// together by TestAvatarMaxBytesMatchesTheWireLimit in package main, which
// imports both.
const AvatarMaxBytes = 1 << 20

// avatarPreviewModalID finds the open avatar preview, to show the picked
// file once it's read.
const avatarPreviewModalID = "avatar-preview"

// avatarPreviewBody is a picked avatar file, shown before it's published:
// the picture (when the space allows), name, format, size and dimensions,
// or why it can't be used. enter or y publishes it; esc goes back to the
// file picker, still where it was.
type avatarPreviewBody struct {
	accountIdx int
	path       string
	img        image.Image
	width      int // source pixel dimensions, before downscaling
	height     int
	size       int64 // file size, which is what the server's limit applies to
	format     string
	err        string
	loading    bool
	keys       *KeyMap
	styles     *uiStyles
	cols, rows int // the space it's shown in
}

func (b *avatarPreviewBody) Update(msg tea.Msg) (tea.Cmd, modal.Outcome) {
	if k, ok := msg.(tea.KeyPressMsg); ok && b.img != nil &&
		(matchesKey(k, b.keys.ConfirmYes) || matchesKey(k, b.keys.SelectSend)) {
		return nil, modal.Submit(b.path)
	}
	return nil, modal.Continue
}

func (b *avatarPreviewBody) View() string {
	textWidth := max(avatarPreviewMinCols, min(b.cols, avatarPreviewTextWidth))
	name := ansi.Truncate(filepath.Base(b.path), textWidth, "…")
	var rows []string
	switch {
	case b.loading:
		rows = append(rows, ansi.Truncate("reading "+name+"…", textWidth, "…"))
	case b.err != "":
		// Wrapped, not cut: why a file can't be used is the point here.
		rows = append(rows, b.styles.popupDanger.Width(textWidth).Render(name+": "+b.err))
	default:
		if cols, pictureRows := avatarPreviewSize(b.width, b.height, b.cols, b.rows-3); cols > 0 {
			rows = append(rows, renderImageBlocks(b.img, cols, pictureRows), "")
		}
		rows = append(rows,
			name,
			b.styles.messageReply.Render(ansi.Truncate(fmt.Sprintf("%s · %d×%d · %s",
				strings.ToUpper(b.format), b.width, b.height, humanBytes(b.size)), textWidth, "…")),
		)
	}
	return strings.Join(rows, "\n")
}

func (b *avatarPreviewBody) SetSize(width, height int) { b.cols, b.rows = width, height }

func (b *avatarPreviewBody) PreferredWidth() int { return avatarPreviewTextWidth }

func (b *avatarPreviewBody) Hints() []modal.Hint {
	if b.img == nil {
		return nil
	}
	return []modal.Hint{modal.KeyHint(tea.KeyPressMsg{Code: tea.KeyEnter}, "publish")}
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
	body := &avatarPreviewBody{accountIdx: accountIdx, path: path, loading: true, keys: m.keys, styles: m.styles}
	return tea.Batch(loadAvatarPreviewCmd(path), m.openModal(&modal.Modal[*Model]{
		ID:    avatarPreviewModalID,
		Title: "Publish as avatar?",
		Body:  body,
		OnSubmit: func(m *Model, _ any) tea.Cmd {
			m.modals.Remove(filePickerModalID)
			return m.setOwnAvatarCmd(body.accountIdx, body.path)
		},
	}))
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
		// The header is read first, and every refusal that can be decided
		// from it is decided here: decoding is what costs, and it costs in
		// proportion to the pixels rather than to the file — an 8000x8000
		// PNG of flat color is well under the publishable size yet holds a
		// quarter of a gigabyte once decoded.
		cfg, format, err := image.DecodeConfig(f)
		if err != nil {
			out.err = fmt.Errorf("not a usable image: %w", err)
			return out
		}
		out.width, out.height = cfg.Width, cfg.Height
		out.format = format
		// Only PNG and JPEG can be published (see the daemon's
		// SetOwnAvatar), so anything else is refused here rather than
		// after the user confirms. Both are registered for the decoder by
		// the imports avatar.go already carries.
		if format != "png" && format != "jpeg" {
			out.err = fmt.Errorf("%s images cannot be published; use PNG or JPEG", format)
			return out
		}
		if out.size > AvatarMaxBytes {
			out.err = fmt.Errorf("%s, over the %s limit", humanBytes(out.size), humanBytes(AvatarMaxBytes))
			return out
		}
		if px := int64(cfg.Width) * int64(cfg.Height); px > avatarPreviewMaxPixels {
			out.err = fmt.Errorf("%d×%d is too large to preview", cfg.Width, cfg.Height)
			return out
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			out.err = err
			return out
		}
		img, _, err := image.Decode(f)
		if err != nil {
			out.err = fmt.Errorf("not a usable image: %w", err)
			return out
		}
		out.img = downscale(img, avatarPreviewMaxEdge)
		return out
	}
}

// applyAvatarPreviewLoaded folds a finished decode into the staged
// candidate, ignoring one for a file that is no longer the candidate (the
// preview was canceled, or another file was picked while it decoded).
func (m *Model) applyAvatarPreviewLoaded(msg avatarPreviewLoadedMsg) {
	md := m.modals.Find(avatarPreviewModalID)
	if md == nil {
		return
	}
	b := md.Body.(*avatarPreviewBody)
	if b.path != msg.path {
		return
	}
	b.loading = false
	b.size = msg.size
	b.width, b.height = msg.width, msg.height
	b.format = msg.format
	if msg.err != nil {
		b.err = msg.err.Error()
		return
	}
	b.img = msg.img
}

// avatarPreviewSize is the picture's size in cells for a srcW x srcH
// image: the largest box with the image's own aspect ratio that fits the
// popup. Zero when there's no room, in which case the popup shows the
// file's details without a picture.
//
// Letterboxed rather than stretched to a square, unlike the sidebar's
// avatar panel — a preview exists to show what the file is, and a banner
// squashed into a square is a picture of something that isn't in the file.
// A cell carries two pixels vertically, so a square image is half as many
// rows as columns and a wide one fewer still.
func avatarPreviewSize(srcW, srcH, maxCols, maxRows int) (cols, rows int) {
	if srcW <= 0 || srcH <= 0 {
		return 0, 0
	}
	if maxRows < 1 || maxCols < avatarPreviewMinCols {
		return 0, 0
	}
	// Widest that still fits maxRows at this aspect ratio: a picture cols
	// wide is cols*srcH/srcW pixels tall, which is half that many rows.
	cols = min(maxCols, 2*maxRows*srcW/srcH)
	if cols < avatarPreviewMinCols {
		return 0, 0
	}
	// Rounded, not truncated: at a banner's proportions the exact answer is
	// a couple of rows, and truncating there halves the picture's height.
	// A picture too wide even for one row still gets one.
	return cols, max(1, (cols*srcH+srcW)/(2*srcW))
}
