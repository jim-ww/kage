package call

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"time"
)

// syncBuffer is bytes.Buffer with its own lock, so it's safe to Write from
// exec.Cmd's internal stderr-copying goroutine while Run's EOF handler reads
// it back from a different goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// videoCaptureFramerate is a conservative cap for screen/camera capture -
// rarely needs more than this to read comfortably, and keeps bandwidth/CPU
// sane over a STUN-brokered path the way FrameMillis does for audio.
const videoCaptureFramerate = 15

// VideoSource is anything that can be started, pumped for encoded frames via
// Run, and torn down via Stop - the shape callsession.go's screen-share
// machinery needs, satisfied identically by ScreenShare (wf-recorder) and
// Camera (ffmpeg/v4l2) so the Jingle content-add/content-accept negotiation
// and the WriteVideoSample pump don't need to know or care which one is
// actually feeding them.
type VideoSource interface {
	// Run reads encoded frames until the source's process exits or Stop is
	// called, calling write once per complete access unit. Blocks - callers
	// run it in its own goroutine.
	Run(write func(frame []byte, sinceLast time.Duration) error) error
	Stop()
}

// annexBSource is the process-management and NAL-framing plumbing shared by
// every capture source that produces a raw H.264 Annex-B bytestream on
// stdout: wf-recorder for screen capture, ffmpeg/v4l2 for a webcam. They
// differ only in the command line that produces that stream.
type annexBSource struct {
	label  string // for log lines, e.g. "screen share", "camera"
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stderr *syncBuffer
	done   chan struct{}
}

// startAnnexBSource starts cmd (which must write a raw H.264 Annex-B stream
// to stdout) and returns once the subprocess is running. Run must be called
// (typically in its own goroutine) to actually pump captured NAL units.
func startAnnexBSource(label string, cmd *exec.Cmd) (*annexBSource, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("opening %s stdout: %w", cmd.Path, err)
	}
	// The capture process logs everything of interest - screencopy/portal
	// permission failures, camera device errors - to stderr, not stdout.
	// Left unset, exec discards it, so a process that fails to actually
	// capture anything exits with output indistinguishable from a clean,
	// deliberate stop (see Run's EOF handling below).
	stderr := &syncBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", cmd.Path, err)
	}
	return &annexBSource{label: label, cmd: cmd, stdout: stdout, stderr: stderr, done: make(chan struct{})}, nil
}

// Run reads Annex-B NAL units off the capture process's stdout, groups them
// into complete access units (frames), and calls write once per frame (start
// codes included, one or more NALs concatenated - e.g. SPS/PPS followed by
// the slice) until the stream ends or Stop is called. A frame is flushed as
// soon as its VCL NAL (slice, type 1 or 5 - x264 emits one slice per
// picture, no slice partitioning) has been seen, since any SPS/PPS/SEI NALs
// for that picture always precede it. Blocks until done - callers run it in
// its own goroutine.
func (s *annexBSource) Run(write func(frame []byte, sinceLast time.Duration) error) error {
	buf := make([]byte, 0, 1<<20)
	tmp := make([]byte, 1<<16)
	frame := make([]byte, 0, 1<<16)
	last := time.Now()
	frames := 0
	for {
		select {
		case <-s.done:
			return nil
		default:
		}

		n, err := s.stdout.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			for {
				nal, rest, ok := splitNextNAL(buf)
				if !ok {
					break
				}
				buf = rest
				if len(nal) == 0 {
					continue
				}
				frame = append(frame, nal...)
				if !isVCLNAL(nal) {
					continue
				}
				now := time.Now()
				sinceLast := now.Sub(last)
				last = now
				out := frame
				frame = make([]byte, 0, 1<<16)
				frames++
				if frames == 1 || frames%60 == 0 {
					slog.Debug(s.label+": encoded frame", "frames", frames, "bytes", len(out), "since_last", sinceLast)
				}
				if werr := write(out, sinceLast); werr != nil {
					return werr
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				// A clean Stop() closes s.done first, so a caller-initiated
				// stop returns via the select above before we ever see EOF
				// here - EOF reaching this point means the capture process
				// exited on its own, which is always worth surfacing (0
				// frames especially: permission/device failures exit this
				// way with nothing on stdout and the real reason only on
				// stderr).
				slog.Debug(s.label+": capture process stream ended", "frames", frames, "stderr", s.stderr.String())
				return nil
			}
			return err
		}
	}
}

// Stop terminates the capture process and unblocks Run.
func (s *annexBSource) Stop() {
	select {
	case <-s.done:
		return
	default:
		close(s.done)
	}
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	_ = s.cmd.Wait()
}

// ScreenShare captures the current output via wf-recorder (wlroots'
// screencopy protocol) as a raw H.264 Annex-B bytestream - no container, so
// the stream can be split into NAL units and pushed onto a WebRTC video
// track directly, the same way call/audio.go's Mic feeds Opus frames to the
// peer connection.
type ScreenShare struct {
	*annexBSource
}

// NewScreenShare starts wf-recorder and returns once the subprocess is
// running. Run must be called (typically in its own goroutine) to actually
// pump captured NAL units.
func NewScreenShare() (*ScreenShare, error) {
	cmd := exec.Command(
		"wf-recorder",
		"-y", // "-f -" still prompts to overwrite "-" as if it were a real file without this
		"-c", "libx264",
		"-p", "tune=zerolatency",
		// "-p","preset=fast",
		"-m", "h264",
		"-r", fmt.Sprint(videoCaptureFramerate),
		"-f", "/dev/stdout",
	)
	src, err := startAnnexBSource("screen share", cmd)
	if err != nil {
		return nil, err
	}
	return &ScreenShare{src}, nil
}

// Camera captures a video4linux2 device (a webcam) via ffmpeg, encoding to
// the same raw H.264 Annex-B bytestream ScreenShare produces - the rest of
// the screen-share pipeline (framing, WriteVideoSample, Jingle
// content-add/-accept) doesn't need to know which one is feeding it (see
// VideoSource).
type Camera struct {
	*annexBSource
}

// NewCamera starts ffmpeg capturing from device (e.g. "/dev/video0") and
// returns once the subprocess is running. Run must be called (typically in
// its own goroutine) to actually pump captured NAL units.
func NewCamera(device string) (*Camera, error) {
	cmd := exec.Command(
		"ffmpeg",
		"-f", "v4l2",
		"-framerate", fmt.Sprint(videoCaptureFramerate),
		"-video_size", "1280x720",
		"-i", device,
		// Most webcams deliver MJPEG/YUYV (4:2:2), not 4:2:0 - encoding that
		// as-is lets libx264 silently pick a higher profile than the
		// baseline-ish one call/peer.go declares in SDP
		// (profile-level-id=42e01f), which a peer's decoder then can't
		// parse: RTP arrives, nothing ever gets decoded, no error either
		// side. Forcing 4:2:0 here keeps the actual bitstream profile
		// consistent with what's negotiated, the same as wf-recorder's
		// screen capture already is.
		"-pix_fmt", "yuv420p",
		"-c:v", "libx264",
		"-profile:v", "baseline",
		"-preset", "superfast",
		"-tune", "zerolatency",
		"-crf", "20",
		"-f", "h264",
		"-",
	)
	src, err := startAnnexBSource("camera", cmd)
	if err != nil {
		return nil, err
	}
	return &Camera{src}, nil
}

// isVCLNAL reports whether nal (start code included, 3- or 4-byte) is a
// coded slice - the NAL that marks the end of its access unit.
func isVCLNAL(nal []byte) bool {
	i := 3
	if len(nal) > 2 && nal[2] != 1 {
		i = 4
	}
	if i >= len(nal) {
		return false
	}
	switch nal[i] & 0x1f {
	case 1, 5: // non-IDR / IDR coded slice
		return true
	default:
		return false
	}
}

// splitNextNAL extracts the first complete Annex-B NAL unit (start code
// included) from buf, once a following start code (marking the next NAL) has
// arrived to show where it ends. ok is false if there's not a full NAL to
// emit yet - the caller should wait for more data.
func splitNextNAL(buf []byte) (nal, rest []byte, ok bool) {
	start := indexStartCode(buf, 0)
	if start < 0 {
		return nil, buf, false
	}
	// Search past this NAL's own start code before looking for the next
	// one, so a short start-code-like run at the very front of the NAL
	// payload can't be mistaken for the following NAL's marker.
	next := indexStartCode(buf, start+3)
	if next < 0 {
		return nil, buf, false
	}
	return buf[start:next], buf[next:], true
}

// indexStartCode finds the byte offset of the next Annex-B start code (the
// 00 00 01 itself, extended one byte earlier if preceded by an extra zero
// byte) at or after from, or -1 if none has arrived yet.
func indexStartCode(buf []byte, from int) int {
	for i := from; i+2 < len(buf); i++ {
		if buf[i] == 0 && buf[i+1] == 0 && buf[i+2] == 1 {
			if i > 0 && buf[i-1] == 0 {
				return i - 1
			}
			return i
		}
	}
	return -1
}

// VideoCodec names which codec a peer's incoming video track was actually
// negotiated with (see webrtc.TrackRemote.Codec()) - the peer picks this,
// not us: kage's own screen-share always sends H.264 (wf-recorder/x264),
// but an arbitrary Jingle peer sending video (e.g. a phone placing a video
// call) may have chosen VP8 instead, and mpv needs telling which one it's
// getting fed - feeding VP8 payload through an H.264 demuxer produces no
// visible error, just a viewer that opens and stays black forever.
type VideoCodec int

const (
	VideoCodecH264 VideoCodec = iota
	VideoCodecVP8
)

// ScreenViewer plays a peer's shared-screen or camera video track by piping
// its depacketized stream into mpv, which does its own decode - kage never
// touches a decoded video frame itself, mirroring how gpg is shelled out to
// rather than reimplemented (see crypto/gpg).
type ScreenViewer struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	codec     VideoCodec
	ivfHeader bool
	frameNum  uint64
}

// NewScreenViewer launches mpv reading a raw H.264 Annex-B or IVF-wrapped
// VP8 stream (see WriteFrame) on stdin, depending on codec.
func NewScreenViewer(title string, codec VideoCodec) (*ScreenViewer, error) {
	format := "h264"
	if codec == VideoCodecVP8 {
		format = "ivf"
	}
	cmd := exec.Command(
		"mpv",
		"--title="+title,
		"--force-window=immediate",
		"--profile=low-latency",
		"--demuxer=lavf",
		"--demuxer-lavf-format="+format,
		"--untimed",
		"-",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("opening mpv stdin: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting mpv: %w", err)
	}
	return &ScreenViewer{cmd: cmd, stdin: stdin, codec: codec}, nil
}

// WriteFrame writes one complete decoded access unit to mpv's input stream:
// for H.264, one or more concatenated Annex-B NALs (start codes included);
// for VP8, one raw VP8 frame, which - unlike H.264 - has no self-delimiting
// start codes of its own, so it's wrapped in a minimal IVF container (a
// 32-byte file header written once, then one 12-byte frame header per
// frame) so mpv's lavf/ivf demuxer can find frame boundaries at all. The
// width/height/frame-rate IVF declares are placeholders: VP8's own keyframe
// header carries the real dimensions, which is what decoders actually use.
func (v *ScreenViewer) WriteFrame(data []byte) error {
	if v.codec != VideoCodecVP8 {
		_, err := v.stdin.Write(data)
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
		if _, err := v.stdin.Write(hdr); err != nil {
			return err
		}
		v.ivfHeader = true
	}
	frameHdr := make([]byte, 12)
	binary.LittleEndian.PutUint32(frameHdr[0:4], uint32(len(data)))
	binary.LittleEndian.PutUint64(frameHdr[4:12], v.frameNum)
	v.frameNum++
	if _, err := v.stdin.Write(frameHdr); err != nil {
		return err
	}
	_, err := v.stdin.Write(data)
	return err
}

// Close stops mpv.
func (v *ScreenViewer) Close() {
	_ = v.stdin.Close()
	if v.cmd.Process != nil {
		_ = v.cmd.Process.Kill()
	}
	_ = v.cmd.Wait()
}
