package call

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
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

// VideoQuality selects the capture profile (resolution/bitrate) used by both
// ScreenShare (wf-recorder) and Camera (ffmpeg/v4l2) - see
// VideoQualityFromString for the config.toml string this maps from.
type VideoQuality int

const (
	VideoQualityMedium VideoQuality = iota
	VideoQualityVeryLow
	VideoQualityLow
	VideoQualityHigh
)

// VideoQualityFromString parses config's video_quality, medium by default.
func VideoQualityFromString(s string) VideoQuality {
	switch s {
	case "very_low":
		return VideoQualityVeryLow
	case "low":
		return VideoQualityLow
	case "high":
		return VideoQualityHigh
	default:
		return VideoQualityMedium
	}
}

// videoQualityProfile is a VideoQuality's capture settings: the camera's
// size (and gpu-screen-recorder's limit), libx264's crf, and the screen's
// bitrate (wf-recorder captures the output's real size).
type videoQualityProfile struct {
	camWidth, camHeight int
	crf                 int
	screenBitrateKbps   int
}

func (q VideoQuality) profile() videoQualityProfile {
	switch q {
	case VideoQualityVeryLow:
		return videoQualityProfile{camWidth: 320, camHeight: 240, crf: 32, screenBitrateKbps: 300}
	case VideoQualityLow:
		return videoQualityProfile{camWidth: 640, camHeight: 480, crf: 26, screenBitrateKbps: 800}
	case VideoQualityHigh:
		return videoQualityProfile{camWidth: 1920, camHeight: 1080, crf: 18, screenBitrateKbps: 4000}
	default: // VideoQualityMedium
		return videoQualityProfile{camWidth: 1280, camHeight: 720, crf: 20, screenBitrateKbps: 1800}
	}
}

// VideoSource is a capture producing encoded frames: ScreenShare or Camera.
type VideoSource interface {
	// Run calls write with each frame until Stop (nil) or the process
	// exits (its error). Blocks.
	Run(write func(frame []byte, sinceLast time.Duration) error) error
	Stop()
}

// annexBSource runs a process writing raw H.264 Annex-B to stdout and frames
// it; the sources differ only in their command.
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
	// Kept: why a capture failed is only on stderr.
	stderr := &syncBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", cmd.Path, err)
	}
	return &annexBSource{label: label, cmd: cmd, stdout: stdout, stderr: stderr, done: make(chan struct{})}, nil
}

// Run groups stdout's NAL units into frames, calling write with each, until
// the stream ends or Stop. A frame ends at its slice NAL: x264 makes one per
// picture, after its SPS/PPS/SEI. Blocks.
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
				// Stop returns above, so EOF here means the process exited
				// by itself: an error, with its stderr.
				stderr := s.stderr.String()
				slog.Debug(s.label+": capture process stream ended", "frames", frames, "stderr", stderr)
				if reason := lastLine(stderr); reason != "" {
					return errors.New(reason)
				}
				return errors.New("capture process exited")
			}
			return err
		}
	}
}

// lastLine is the last non-blank line of s - for a capture process that
// died, its stderr's last line is nearly always the actual reason.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
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

// ScreenRecorder selects the program ScreenShare captures the screen with -
// see ScreenRecorderFromString for the config.toml string this maps from.
type ScreenRecorder int

const (
	ScreenRecorderGPU ScreenRecorder = iota
	ScreenRecorderWF
)

// ScreenRecorderFromString maps config.toml's screen_recorder string onto a
// ScreenRecorder, defaulting to ScreenRecorderGPU for "" or anything
// unrecognized - same lenient default as VideoQualityFromString.
func ScreenRecorderFromString(s string) ScreenRecorder {
	switch s {
	case "wf-recorder":
		return ScreenRecorderWF
	default:
		return ScreenRecorderGPU
	}
}

// ScreenShare captures the screen as raw H.264 Annex-B, with wf-recorder
// (libx264) or gpu-screen-recorder (VAAPI/NVENC).
type ScreenShare struct {
	*annexBSource
}

// NewScreenShare starts recorder; Run reads its frames.
func NewScreenShare(recorder ScreenRecorder, quality VideoQuality) (*ScreenShare, error) {
	var cmd *exec.Cmd
	if recorder == ScreenRecorderWF {
		cmd = wfRecorderCmd(quality.profile())
	} else {
		cmd = gpuScreenRecorderCmd(quality.profile())
	}
	src, err := startAnnexBSource("screen share", cmd)
	if err != nil {
		return nil, err
	}
	return &ScreenShare{src}, nil
}

func wfRecorderCmd(profile videoQualityProfile) *exec.Cmd {
	return exec.Command(
		"wf-recorder",
		"-y", // "-f -" still prompts to overwrite "-" as if it were a real file without this
		"-c", "libx264",
		"-p", "tune=zerolatency",
		// Baseline, as the SDP declares (call/peer.go); libx264 would pick
		// High, which the peer then can't decode.
		"-p", "profile=baseline",
		"-p", fmt.Sprintf("b=%dk", profile.screenBitrateKbps),
		"-m", "h264",
		"-r", fmt.Sprint(videoCaptureFramerate),
		"-f", "/dev/stdout",
	)
}

func gpuScreenRecorderCmd(profile videoQualityProfile) *exec.Cmd {
	return exec.Command(
		"gpu-screen-recorder",
		"-w", "screen",
		"-c", "h264", // ffmpeg's raw Annex-B muxer
		"-k", "h264",
		"-f", fmt.Sprint(videoCaptureFramerate),
		// Unlike wf-recorder, gsr can scale on the GPU before encoding, so
		// quality caps resolution here too, not just bitrate.
		"-s", fmt.Sprintf("%dx%d", profile.camWidth, profile.camHeight),
		"-bm", "cbr",
		"-q", fmt.Sprint(profile.screenBitrateKbps),
		// - profile: Baseline, as for wf-recorder (h264_vaapi's name).
		// - coder: Baseline has no CABAC.
		// - flags: no global_header, which takes SPS/PPS out of the
		//   stream, without which a viewer can't decode.
		"-ffmpeg-video-opts", "profile=constrained_baseline;coder=cavlc;flags=-global_header",
		"-v", "no", // FPS/damage chatter
		"-o", "/dev/stdout",
	)
}

// Camera captures a webcam (v4l2) with ffmpeg, as ScreenShare's stream.
type Camera struct {
	*annexBSource
}

// NewCamera starts capturing device (e.g. "/dev/video0"); Run reads its
// frames.
func NewCamera(device string, quality VideoQuality) (*Camera, error) {
	profile := quality.profile()
	cmd := exec.Command(
		"ffmpeg",
		"-f", "v4l2",
		"-framerate", fmt.Sprint(videoCaptureFramerate),
		"-video_size", fmt.Sprintf("%dx%d", profile.camWidth, profile.camHeight),
		"-i", device,
		// 4:2:0: webcams' 4:2:2 makes libx264 leave Baseline (see
		// wfRecorderCmd).
		"-pix_fmt", "yuv420p",
		"-c:v", "libx264",
		"-profile:v", "baseline",
		"-preset", "superfast",
		"-tune", "zerolatency",
		"-crf", fmt.Sprint(profile.crf),
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

// splitNextNAL takes buf's first NAL unit, start code included, once the next
// start code shows its end; ok is false until then.
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

// VideoCodec is an incoming video track's codec, for mpv: kage sends H.264,
// but a peer may send VP8.
type VideoCodec int

const (
	VideoCodecH264 VideoCodec = iota
	VideoCodecVP8
)

// ScreenViewer plays a peer's video by piping it into mpv, which decodes it.
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

// WriteFrame writes a frame to mpv: H.264 Annex-B NALs as they are, a VP8
// frame in IVF framing, VP8 having no start codes. IVF's size and rate are
// placeholders; decoders use the keyframe's.
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
