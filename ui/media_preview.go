package ui

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Media previews: a message's image or video attachment drawn under its
// name in the same half-block cells as avatars (renderImageBlocks). PNG,
// JPEG and GIF are decoded in process; a video's frame, and the image
// formats the standard library can't read, come from ffmpeg (already on
// PATH for calls), and without it those just get no preview.
//
// Only an attachment already in the local cache (attachmentLocalPath) is
// previewed - an own upload, or one opened or saved - never fetched for it:
// downloading a peer's upload tells its host that it was looked at, the
// reason renderAttachmentLine doesn't show sizes either.

// mediaPreviewsOn gates the previews; see DisplayOptions.MediaPreviewsDisabled.
var mediaPreviewsOn atomic.Bool

func init() { mediaPreviewsOn.Store(true) }

func setMediaPreviewsEnabled(on bool) { mediaPreviewsOn.Store(on) }

// mediaPreviewMaxCols and mediaPreviewMaxRows bound a preview in the chat:
// big enough to tell what the picture is, small enough that a chat of
// photos stays a chat.
const (
	mediaPreviewMaxCols = 40
	mediaPreviewMaxRows = 10
)

// mediaThumbEdge is what a decoded image is downscaled to before it's
// kept: mediaPreviewMaxCols wide and two pixels per row is all a preview
// ever samples, with room to spare.
const mediaThumbEdge = 96

// mediaThumb is one cached file's decoded preview, or that it has none.
type mediaThumb struct {
	img           image.Image
	width, height int // source dimensions, for the aspect ratio
	failed        bool
}

// mediaThumbs holds decoded previews by local path. Written only from
// Update (applyMediaThumb), so a row's height can't change between a full
// render and a refreshViewportSelection splice of it; renderers only read,
// and queue what's missing for Update to decode.
var (
	mediaThumbMu      sync.Mutex
	mediaThumbs       = map[string]mediaThumb{}
	mediaThumbPending = map[string]bool{}
	mediaThumbQueue   []string
	// mediaBlockCache is each rendered preview, by path and size: every
	// loaded message is rendered on a refresh, not only those on screen.
	mediaBlockCache = map[mediaBlockKey]string{}
)

type mediaBlockKey struct {
	path       string
	cols, rows int
}

// mediaBlockCacheLimit caps mediaBlockCache; past it it's cleared.
const mediaBlockCacheLimit = 256

// mediaKind is how a file's preview is made.
type mediaKind int

const (
	mediaNone   mediaKind = iota
	mediaImage            // decoded in process
	mediaFFmpeg           // an image format the standard library lacks
	mediaVideo            // one frame, through ffmpeg
)

func previewKind(name string) mediaKind {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif":
		return mediaImage
	case ".webp", ".bmp":
		return mediaFFmpeg
	case ".mp4", ".mkv", ".webm", ".mov", ".avi", ".m4v":
		return mediaVideo
	}
	return mediaNone
}

// lookupMediaThumb returns path's preview, queueing it for decoding when it
// hasn't been tried; see takeMediaThumbRequests.
func lookupMediaThumb(path string) (mediaThumb, bool) {
	mediaThumbMu.Lock()
	defer mediaThumbMu.Unlock()
	if t, ok := mediaThumbs[path]; ok {
		return t, true
	}
	if !mediaThumbPending[path] {
		mediaThumbPending[path] = true
		mediaThumbQueue = append(mediaThumbQueue, path)
	}
	return mediaThumb{}, false
}

// takeMediaThumbRequests drains the paths renders found missing, as one
// decode command each.
func takeMediaThumbRequests() tea.Cmd {
	mediaThumbMu.Lock()
	queue := mediaThumbQueue
	mediaThumbQueue = nil
	mediaThumbMu.Unlock()
	if len(queue) == 0 {
		return nil
	}
	cmds := make([]tea.Cmd, len(queue))
	for i, path := range queue {
		cmds[i] = decodeMediaThumbCmd(path)
	}
	return tea.Batch(cmds...)
}

// mediaThumbMsg is a finished decode, applied by applyMediaThumb.
type mediaThumbMsg struct {
	path  string
	thumb mediaThumb
}

// mediaDecodeSlots bounds concurrent decodes: opening a chat of photos
// asks for all of them at once.
var mediaDecodeSlots = make(chan struct{}, 2)

func decodeMediaThumbCmd(path string) tea.Cmd {
	return func() tea.Msg {
		mediaDecodeSlots <- struct{}{}
		defer func() { <-mediaDecodeSlots }()
		var thumb mediaThumb
		var err error
		switch previewKind(path) {
		case mediaImage:
			thumb, err = decodeMediaThumb(path)
		case mediaFFmpeg:
			thumb, err = ffmpegThumb(path, false)
		case mediaVideo:
			thumb, err = ffmpegThumb(path, true)
		default:
			err = fmt.Errorf("no preview for %s", filepath.Ext(path))
		}
		if err != nil {
			return mediaThumbMsg{path: path, thumb: mediaThumb{failed: true}}
		}
		return mediaThumbMsg{path: path, thumb: thumb}
	}
}

// decodeMediaThumb decodes path downscaled, refusing from its header alone
// what would be too costly to decode (see avatarPreviewMaxPixels).
func decodeMediaThumb(path string) (mediaThumb, error) {
	f, err := os.Open(path)
	if err != nil {
		return mediaThumb{}, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return mediaThumb{}, err
	}
	if px := int64(cfg.Width) * int64(cfg.Height); px <= 0 || px > avatarPreviewMaxPixels {
		return mediaThumb{}, fmt.Errorf("%d×%d is too large to preview", cfg.Width, cfg.Height)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return mediaThumb{}, err
	}
	img, _, err := image.Decode(f)
	if err != nil {
		return mediaThumb{}, err
	}
	return mediaThumb{img: downscale(img, mediaThumbEdge), width: cfg.Width, height: cfg.Height}, nil
}

// ffmpegTimeout bounds one ffmpeg run: a file that takes longer isn't
// worth the wait for a thumbnail.
const ffmpegTimeout = 10 * time.Second

// ffmpegThumb has ffmpeg decode path to one PNG frame, already downscaled.
// A video's is taken a second in, past a fade from black, or its first
// frame when it's shorter than that.
func ffmpegThumb(path string, video bool) (mediaThumb, error) {
	frame := func(seek bool) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), ffmpegTimeout)
		defer cancel()
		args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
		if seek {
			args = append(args, "-ss", "1")
		}
		args = append(args, "-i", path, "-frames:v", "1",
			"-vf", fmt.Sprintf("scale='min(%d,iw)':-2", mediaThumbEdge*2),
			"-f", "image2pipe", "-c:v", "png", "-")
		cmd := exec.CommandContext(ctx, "ffmpeg", args...)
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err != nil {
			return nil, err
		}
		return out.Bytes(), nil
	}
	data, err := frame(video)
	if video && (err != nil || len(data) == 0) {
		data, err = frame(false)
	}
	if err != nil {
		return mediaThumb{}, err
	}
	if len(data) == 0 {
		return mediaThumb{}, fmt.Errorf("ffmpeg gave no frame for %s", filepath.Base(path))
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return mediaThumb{}, err
	}
	b := img.Bounds()
	return mediaThumb{img: downscale(img, mediaThumbEdge), width: b.Dx(), height: b.Dy()}, nil
}

// applyMediaThumb stores a finished decode; true when it's a picture the
// chat should be redrawn for.
func applyMediaThumb(msg mediaThumbMsg) bool {
	mediaThumbMu.Lock()
	defer mediaThumbMu.Unlock()
	delete(mediaThumbPending, msg.path)
	mediaThumbs[msg.path] = msg.thumb
	return !msg.thumb.failed
}

// retryFailedMediaThumbs forgets every failed decode, for after a download
// lands: a render while it was still being written saw a truncated file.
func retryFailedMediaThumbs() {
	mediaThumbMu.Lock()
	defer mediaThumbMu.Unlock()
	for path, t := range mediaThumbs {
		if t.failed {
			delete(mediaThumbs, path)
		}
	}
}

// renderMediaPreview is target's preview at most maxCols wide, "" when
// there's none (yet): previews off, not an image or video, not cached,
// still decoding, or not decodable.
func renderMediaPreview(target, jid string, maxCols int) string {
	if !mediaPreviewsOn.Load() || previewKind(attachmentDisplayName(target)) == mediaNone {
		return ""
	}
	path := attachmentLocalPath(target, jid)
	if path == "" {
		return ""
	}
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	thumb, ok := lookupMediaThumb(path)
	if !ok || thumb.failed || thumb.img == nil {
		return ""
	}
	cols, rows := avatarPreviewSize(thumb.width, thumb.height, min(maxCols, mediaPreviewMaxCols), mediaPreviewMaxRows)
	if cols <= 0 {
		return ""
	}
	key := mediaBlockKey{path: path, cols: cols, rows: rows}
	mediaThumbMu.Lock()
	block, ok := mediaBlockCache[key]
	mediaThumbMu.Unlock()
	if ok {
		return block
	}
	block = renderImageBlocks(thumb.img, cols, rows)
	mediaThumbMu.Lock()
	if len(mediaBlockCache) >= mediaBlockCacheLimit {
		clear(mediaBlockCache)
	}
	mediaBlockCache[key] = block
	mediaThumbMu.Unlock()
	return block
}
