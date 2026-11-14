package call

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// ScreenViewer plays a peer's video: ffmpeg decodes it into raw pictures
// (YUV4MPEG), and mpv shows them. Decoding is kept apart from display so
// a display that can't keep up - a window its compositor throttles, a slow
// GPU upload - costs frames rather than delay: every frame is decoded, but
// only the newest picture is handed to mpv, skipping whatever it had no
// time to show. Only if decoding itself can't keep up are frames dropped
// before it, back to a keyframe (see frameQueue).
type ScreenViewer struct {
	codec     VideoCodec
	decoder   *exec.Cmd
	display   *exec.Cmd
	decoderIn io.WriteCloser
	displayIn io.WriteCloser
	stderr    *syncBuffer
	queue     *frameQueue
	latest    *latestPicture
	ivfHeader bool
	frameNum  uint64
	wg        sync.WaitGroup

	mu      sync.Mutex
	err     error // why decoding or display stopped
	closing bool
}

// NewScreenViewer opens a window titled title playing a stream of codec
// frames (see WriteFrame). needKeyframe is called when decoding has fallen
// so far behind that frames are dropped until the next keyframe, to ask
// the sender for one sooner.
func NewScreenViewer(title string, codec VideoCodec, needKeyframe func()) (*ScreenViewer, error) {
	return startScreenViewer(codec, videoDecoderCmd(codec), videoDisplayCmd(title), needKeyframe)
}

// videoDecoderCmd decodes an H.264 Annex-B or IVF-wrapped VP8 stream on
// stdin into YUV4MPEG on stdout, a picture as soon as its frame is in:
// low_delay keeps the decoder from holding frames back for threading, and
// passthrough keeps them coming as they're decoded rather than paced to a
// frame rate. Its output stays the size of the first picture even if the
// stream's changes (as libwebrtc senders' does under load): ffmpeg scales
// to it.
func videoDecoderCmd(codec VideoCodec) *exec.Cmd {
	format := "h264"
	if codec == VideoCodecVP8 {
		format = "ivf"
	}
	return exec.Command("ffmpeg",
		"-hide_banner", "-loglevel", "error",
		"-probesize", "32", "-analyzeduration", "0", "-flags", "low_delay",
		"-f", format, "-i", "-",
		"-fps_mode", "passthrough",
		"-f", "yuv4mpegpipe", "-pix_fmt", "yuv420p", "-flush_packets", "1", "-",
	)
}

// videoDisplayCmd shows YUV4MPEG pictures from stdin as they come, reading
// no further ahead than the next one.
func videoDisplayCmd(title string) *exec.Cmd {
	return exec.Command("mpv",
		"--title="+title,
		"--force-window=immediate",
		"--profile=low-latency",
		"--demuxer=lavf",
		"--demuxer-lavf-format=yuv4mpegpipe",
		"--untimed",
		"--cache=no",
		"--demuxer-readahead-secs=0",
		"--demuxer-max-bytes=16MiB",
		"--demuxer-max-back-bytes=0",
		"-",
	)
}

func startScreenViewer(codec VideoCodec, decoder, display *exec.Cmd, needKeyframe func()) (*ScreenViewer, error) {
	v := &ScreenViewer{codec: codec, decoder: decoder, display: display, stderr: &syncBuffer{}, latest: newLatestPicture()}
	decoder.Stderr = v.stderr
	var err error
	if v.decoderIn, err = decoder.StdinPipe(); err != nil {
		return nil, fmt.Errorf("opening video decoder stdin: %w", err)
	}
	decoderOut, err := decoder.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("opening video decoder stdout: %w", err)
	}
	if v.displayIn, err = display.StdinPipe(); err != nil {
		return nil, fmt.Errorf("opening video display stdin: %w", err)
	}
	if err := decoder.Start(); err != nil {
		return nil, fmt.Errorf("starting video decoder: %w", err)
	}
	if err := display.Start(); err != nil {
		_ = decoder.Process.Kill()
		_ = decoder.Wait()
		return nil, fmt.Errorf("starting video display: %w", err)
	}
	v.queue = newFrameQueue(v.writeFrame, func(frame []byte) bool { return IsKeyframe(codec, frame) }, needKeyframe)
	v.wg.Add(2)
	go v.readPictures(decoderOut)
	go v.showPictures()
	return v, nil
}

// WriteFrame queues a frame for decoding without waiting for it. It
// returns why decoding or display stopped, as when the window's been
// closed.
func (v *ScreenViewer) WriteFrame(data []byte) error {
	v.mu.Lock()
	err := v.err
	v.mu.Unlock()
	if err != nil {
		return err
	}
	return v.queue.Push(data)
}

// h264AUD is an H.264 access unit delimiter NAL: after a frame, it tells
// the decoder's parser the frame is complete, which it would otherwise
// only know once the next frame starts coming in.
var h264AUD = []byte{0, 0, 0, 1, 9, 0xf0}

// writeFrame writes a frame to the decoder: H.264 Annex-B NALs as they
// are, followed by an access unit delimiter, a VP8 frame in IVF framing,
// VP8 having no start codes. IVF's size and rate are placeholders;
// decoders use the keyframe's.
func (v *ScreenViewer) writeFrame(data []byte) error {
	if v.codec != VideoCodecVP8 {
		if _, err := v.decoderIn.Write(data); err != nil {
			return err
		}
		_, err := v.decoderIn.Write(h264AUD)
		return err
	}
	if !v.ivfHeader {
		hdr := make([]byte, 32)
		copy(hdr[0:4], "DKIF")
		binary.LittleEndian.PutUint16(hdr[4:6], 0)
		binary.LittleEndian.PutUint16(hdr[6:8], 32)
		copy(hdr[8:12], "VP80")
		binary.LittleEndian.PutUint16(hdr[12:14], 1280)
		binary.LittleEndian.PutUint16(hdr[14:16], 720)
		binary.LittleEndian.PutUint32(hdr[16:20], 30) // frame rate numerator
		binary.LittleEndian.PutUint32(hdr[20:24], 1)  // timebase denominator
		binary.LittleEndian.PutUint32(hdr[24:28], 0)  // frame count: unknown, streaming
		if _, err := v.decoderIn.Write(hdr); err != nil {
			return err
		}
		v.ivfHeader = true
	}
	frameHdr := make([]byte, 12)
	binary.LittleEndian.PutUint32(frameHdr[0:4], uint32(len(data)))
	binary.LittleEndian.PutUint64(frameHdr[4:12], v.frameNum)
	v.frameNum++
	if _, err := v.decoderIn.Write(frameHdr); err != nil {
		return err
	}
	_, err := v.decoderIn.Write(data)
	return err
}

// fail records why the viewer stopped, unless it's being closed anyway.
func (v *ScreenViewer) fail(err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.err == nil && !v.closing {
		v.err = err
	}
}

// readPictures reads the decoder's pictures into latest, passing the
// stream header straight on to the display.
func (v *ScreenViewer) readPictures(out io.Reader) {
	defer v.wg.Done()
	r := bufio.NewReaderSize(out, 1<<16)
	header, err := r.ReadString('\n')
	if err != nil {
		v.fail(v.decoderStopped(err))
		return
	}
	size, err := y4mPictureSize(header)
	if err != nil {
		v.fail(err)
		return
	}
	if _, err := io.WriteString(v.displayIn, header); err != nil {
		v.fail(fmt.Errorf("video display: %w", err))
		return
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			v.fail(v.decoderStopped(err))
			return
		}
		if !strings.HasPrefix(line, "FRAME") {
			v.fail(fmt.Errorf("video decoder wrote %q, not a picture", strings.TrimSpace(line)))
			return
		}
		pic := v.latest.buffer(size)
		if _, err := io.ReadFull(r, pic); err != nil {
			v.fail(v.decoderStopped(err))
			return
		}
		v.latest.Put(pic)
	}
}

func (v *ScreenViewer) decoderStopped(err error) error {
	if reason := lastLine(v.stderr.String()); reason != "" {
		return fmt.Errorf("video decoder stopped: %s", reason)
	}
	return fmt.Errorf("video decoder stopped: %w", err)
}

// showPictures hands the display the newest picture each time it's ready
// for one.
func (v *ScreenViewer) showPictures() {
	defer v.wg.Done()
	for {
		pic, ok := v.latest.Take()
		if !ok {
			return
		}
		_, err := io.WriteString(v.displayIn, "FRAME\n")
		if err == nil {
			_, err = v.displayIn.Write(pic)
		}
		v.latest.Release(pic)
		if err != nil {
			v.fail(fmt.Errorf("video display: %w", err))
			return
		}
	}
}

// Close closes the window and stops decoding.
func (v *ScreenViewer) Close() {
	v.mu.Lock()
	v.closing = true
	v.mu.Unlock()
	v.queue.Close()
	v.latest.Close()
	_ = v.decoderIn.Close()
	_ = v.displayIn.Close()
	_ = v.decoder.Process.Kill()
	_ = v.display.Process.Kill()
	v.wg.Wait()
	v.queue.Wait()
	_ = v.decoder.Wait()
	_ = v.display.Wait()
}

// y4mPictureSize is the size of each picture in a YUV4MPEG stream with
// this header, in 4:2:0 as the decoder is told to write.
func y4mPictureSize(header string) (int, error) {
	fields := strings.Fields(header)
	if len(fields) == 0 || fields[0] != "YUV4MPEG2" {
		return 0, fmt.Errorf("video decoder didn't write YUV4MPEG: %q", strings.TrimSpace(header))
	}
	var w, h int
	for _, f := range fields[1:] {
		switch f[0] {
		case 'W':
			w, _ = strconv.Atoi(f[1:])
		case 'H':
			h, _ = strconv.Atoi(f[1:])
		case 'C':
			if !strings.HasPrefix(f, "C420") {
				return 0, fmt.Errorf("video decoder wrote %s pictures, not 4:2:0", f[1:])
			}
		}
	}
	if w <= 0 || h <= 0 {
		return 0, fmt.Errorf("video decoder wrote a header without a size: %q", strings.TrimSpace(header))
	}
	return w*h + 2*((w+1)/2)*((h+1)/2), nil
}

// latestPicture holds the newest decoded picture until the display takes
// it: one put before the last was taken is dropped, its buffer reused.
type latestPicture struct {
	mu      sync.Mutex
	cond    *sync.Cond
	pending []byte
	free    [][]byte
	closed  bool
	dropped int // pictures replaced before being shown, for tests
}

func newLatestPicture() *latestPicture {
	l := &latestPicture{}
	l.cond = sync.NewCond(&l.mu)
	return l
}

// buffer returns a buffer of size bytes to decode a picture into.
func (l *latestPicture) buffer(size int) []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	for len(l.free) > 0 {
		b := l.free[len(l.free)-1]
		l.free = l.free[:len(l.free)-1]
		if cap(b) >= size {
			return b[:size]
		}
	}
	return make([]byte, size)
}

// Put makes pic the newest picture, dropping one not yet taken.
func (l *latestPicture) Put(pic []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pending != nil {
		l.free = append(l.free, l.pending)
		l.dropped++
	}
	l.pending = pic
	l.cond.Signal()
}

// Take waits for a picture newer than the last one taken, reporting false
// once closed.
func (l *latestPicture) Take() ([]byte, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for l.pending == nil && !l.closed {
		l.cond.Wait()
	}
	if l.closed {
		return nil, false
	}
	pic := l.pending
	l.pending = nil
	return pic, true
}

// Release gives a taken picture's buffer back for reuse.
func (l *latestPicture) Release(pic []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.free = append(l.free, pic)
}

// Close wakes a waiting Take, ending it.
func (l *latestPicture) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	l.cond.Broadcast()
}
