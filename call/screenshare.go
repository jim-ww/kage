package call

import (
	"bufio"
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
// size (and gpu-screen-recorder's limit), libx264's crf for the camera, and
// the bitrate - the screen's, and the most the camera's crf may take
// (wf-recorder captures the output's real size).
type videoQualityProfile struct {
	camWidth, camHeight int
	crf                 int
	bitrateKbps         int
}

func (q VideoQuality) profile() videoQualityProfile {
	switch q {
	case VideoQualityVeryLow:
		return videoQualityProfile{camWidth: 320, camHeight: 240, crf: 32, bitrateKbps: 300}
	case VideoQualityLow:
		return videoQualityProfile{camWidth: 640, camHeight: 480, crf: 26, bitrateKbps: 800}
	case VideoQualityHigh:
		return videoQualityProfile{camWidth: 1920, camHeight: 1080, crf: 18, bitrateKbps: 4000}
	default: // VideoQualityMedium
		return videoQualityProfile{camWidth: 1280, camHeight: 720, crf: 20, bitrateKbps: 1800}
	}
}

// VideoSource is a capture producing encoded frames: ScreenShare or Camera.
type VideoSource interface {
	// Run calls write with each frame until Stop (nil) or the process
	// exits (its error). Blocks.
	Run(write func(frame []byte, sinceLast time.Duration) error) error
	Stop()
}

// captureProcess is the capture behind a VideoSource: a process, or a
// pipeline of them, writing encoded video to stdout.
type captureProcess struct {
	label  string // for log lines, e.g. "screen share", "camera"
	cmds   []*exec.Cmd
	stdout io.ReadCloser
	stderr *syncBuffer
	done   chan struct{}
}

// startCapture starts cmds as a pipeline, each one's stdout the next one's
// stdin, and returns once they run; Run reads the last one's stdout.
func startCapture(label string, cmds ...*exec.Cmd) (*captureProcess, error) {
	// Kept: why a capture failed is only on stderr.
	stderr := &syncBuffer{}
	for i, cmd := range cmds {
		cmd.Stderr = stderr
		if i > 0 {
			in, err := cmds[i-1].StdoutPipe()
			if err != nil {
				return nil, fmt.Errorf("opening %s stdout: %w", cmds[i-1].Path, err)
			}
			cmd.Stdin = in
		}
	}
	last := cmds[len(cmds)-1]
	stdout, err := last.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("opening %s stdout: %w", last.Path, err)
	}
	s := &captureProcess{label: label, stdout: stdout, stderr: stderr, done: make(chan struct{})}
	for _, cmd := range cmds {
		if err := cmd.Start(); err != nil {
			s.Stop()
			return nil, fmt.Errorf("starting %s: %w", cmd.Path, err)
		}
		s.cmds = append(s.cmds, cmd)
	}
	return s, nil
}

// ended is Run's result once reading stdout failed with err: nil after Stop;
// otherwise the capture exited by itself, an error with its stderr.
func (s *captureProcess) ended(err error, frames int) error {
	select {
	case <-s.done:
		return nil
	default:
	}
	if err != io.EOF && err != io.ErrUnexpectedEOF {
		return err
	}
	stderr := s.stderr.String()
	slog.Debug(s.label+": capture process stream ended", "frames", frames, "stderr", stderr)
	if reason := lastLine(stderr); reason != "" {
		return errors.New(reason)
	}
	return errors.New("capture process exited")
}

// Stop terminates the capture and unblocks Run.
func (s *captureProcess) Stop() {
	select {
	case <-s.done:
		return
	default:
		close(s.done)
	}
	for _, cmd := range s.cmds {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
	for _, cmd := range s.cmds {
		_ = cmd.Wait()
	}
}

// annexBSource frames a capture writing raw H.264 Annex-B.
type annexBSource struct {
	*captureProcess
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
			return s.ended(err, frames)
		}
	}
}

// ivfSource frames a capture writing VP8 in IVF: a 32-byte file header,
// then each frame behind a 12-byte header holding its size.
type ivfSource struct {
	*captureProcess
}

// maxIVFFrame bounds a frame's size, against reading a corrupt header's.
const maxIVFFrame = 16 << 20

// Run calls write with each frame until the stream ends or Stop. Blocks.
func (s *ivfSource) Run(write func(frame []byte, sinceLast time.Duration) error) error {
	r := bufio.NewReaderSize(s.stdout, 1<<16)
	var hdr [32]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return s.ended(err, 0)
	}
	if string(hdr[:4]) != "DKIF" {
		return fmt.Errorf("%s: capture didn't write IVF", s.label)
	}
	if n := int(binary.LittleEndian.Uint16(hdr[6:8])); n > len(hdr) {
		if _, err := r.Discard(n - len(hdr)); err != nil {
			return s.ended(err, 0)
		}
	}
	last := time.Now()
	frames := 0
	var fh [12]byte
	for {
		if _, err := io.ReadFull(r, fh[:]); err != nil {
			return s.ended(err, frames)
		}
		size := binary.LittleEndian.Uint32(fh[:4])
		if size > maxIVFFrame {
			return fmt.Errorf("%s: IVF frame of %d bytes", s.label, size)
		}
		frame := make([]byte, size)
		if _, err := io.ReadFull(r, frame); err != nil {
			return s.ended(err, frames)
		}
		now := time.Now()
		sinceLast := now.Sub(last)
		last = now
		frames++
		if frames == 1 || frames%60 == 0 {
			slog.Debug(s.label+": encoded frame", "frames", frames, "bytes", len(frame), "since_last", sinceLast)
		}
		if err := write(frame, sinceLast); err != nil {
			return err
		}
	}
}

// startSource starts cmds as a capture framed for codec.
func startSource(label string, codec VideoCodec, cmds ...*exec.Cmd) (VideoSource, error) {
	p, err := startCapture(label, cmds...)
	if err != nil {
		return nil, err
	}
	if codec == VideoCodecVP8 {
		return &ivfSource{p}, nil
	}
	return &annexBSource{p}, nil
}

// lastLine is the last non-blank line of s - for a capture process that
// died, its stderr's last line is nearly always the actual reason.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
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

// ScreenShare captures the screen, with wf-recorder (libx264, libvpx) or
// gpu-screen-recorder (VAAPI/NVENC).
type ScreenShare struct {
	VideoSource
}

// NewScreenShare starts recorder encoding codec; Run reads its frames.
// gpu-screen-recorder only encodes VP8 on GPUs that have it, and on the CPU
// only H.264, so for VP8 its H.264 is transcoded instead.
func NewScreenShare(recorder ScreenRecorder, quality VideoQuality, codec VideoCodec) (*ScreenShare, error) {
	profile := quality.profile()
	cmds := []*exec.Cmd{gpuScreenRecorderCmd(profile)}
	if recorder == ScreenRecorderWF {
		cmds = []*exec.Cmd{wfRecorderCmd(profile, codec)}
	} else if codec == VideoCodecVP8 {
		cmds = append(cmds, vp8TranscodeCmd(profile))
	}
	src, err := startSource("screen share", codec, cmds...)
	if err != nil {
		return nil, err
	}
	return &ScreenShare{src}, nil
}

// vp8Args are libvpx's settings for a live call, as ffmpeg options
// ("-key", "value", ...): realtime, no frames held back, and a keyframe
// every 2s, as gpu-screen-recorder's default, since a peer's keyframe
// request isn't answered.
func vp8Args(bitrateKbps int) []string {
	return []string{
		"-deadline", "realtime",
		"-cpu-used", "8",
		"-lag-in-frames", "0",
		"-error-resilient", "1",
		"-b", fmt.Sprintf("%dk", bitrateKbps),
		"-g", fmt.Sprint(2 * videoCaptureFramerate),
		"-threads", "4",
	}
}

func wfRecorderCmd(profile videoQualityProfile, codec VideoCodec) *exec.Cmd {
	// "-f -" still prompts to overwrite "-" as if it were a real file
	// without -y.
	args := []string{"-y"}
	if codec == VideoCodecVP8 {
		args = append(args, "-c", "libvpx")
		vp8 := vp8Args(profile.bitrateKbps)
		for i := 0; i+1 < len(vp8); i += 2 {
			args = append(args, "-p", strings.TrimPrefix(vp8[i], "-")+"="+vp8[i+1])
		}
		args = append(args, "-m", "ivf")
	} else {
		args = append(args,
			"-c", "libx264",
			"-p", "tune=zerolatency",
			// Baseline, as the SDP declares (call/peer.go); libx264 would
			// pick High, which the peer then can't decode.
			"-p", "profile=baseline",
			"-p", fmt.Sprintf("b=%dk", profile.bitrateKbps),
			"-m", "h264",
		)
	}
	args = append(args, "-r", fmt.Sprint(videoCaptureFramerate), "-f", "/dev/stdout")
	return exec.Command("wf-recorder", args...)
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
		"-q", fmt.Sprint(profile.bitrateKbps),
		// - profile: Baseline, as for wf-recorder (h264_vaapi's name).
		// - coder: Baseline has no CABAC.
		// - flags: no global_header, which takes SPS/PPS out of the
		//   stream, without which a viewer can't decode.
		"-ffmpeg-video-opts", "profile=constrained_baseline;coder=cavlc;flags=-global_header",
		"-v", "no", // FPS/damage chatter
		"-o", "/dev/stdout",
	)
}

// vp8TranscodeCmd re-encodes raw H.264 on stdin as VP8 in IVF on stdout.
// Probing is cut to the minimum, or the first frame waits seconds for it.
func vp8TranscodeCmd(profile videoQualityProfile) *exec.Cmd {
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-probesize", "32", "-analyzeduration", "0",
		"-f", "h264", "-framerate", fmt.Sprint(videoCaptureFramerate), "-i", "-",
		"-c:v", "libvpx",
	}
	args = append(args, vp8Args(profile.bitrateKbps)...)
	args = append(args, "-f", "ivf", "-flush_packets", "1", "-")
	return exec.Command("ffmpeg", args...)
}

// Camera captures a webcam (v4l2) with ffmpeg, as ScreenShare's stream.
type Camera struct {
	VideoSource
}

// NewCamera starts capturing device (e.g. "/dev/video0") encoding codec;
// Run reads its frames.
func NewCamera(device string, quality VideoQuality, codec VideoCodec) (*Camera, error) {
	src, err := startSource("camera", codec, cameraCmd(device, quality.profile(), codec))
	if err != nil {
		return nil, err
	}
	return &Camera{src}, nil
}

func cameraCmd(device string, profile videoQualityProfile, codec VideoCodec) *exec.Cmd {
	args := []string{
		"-f", "v4l2",
		"-framerate", fmt.Sprint(videoCaptureFramerate),
		"-video_size", fmt.Sprintf("%dx%d", profile.camWidth, profile.camHeight),
		"-i", device,
		// 4:2:0: webcams' 4:2:2 makes libx264 leave Baseline (see
		// wfRecorderCmd).
		"-pix_fmt", "yuv420p",
	}
	if codec == VideoCodecVP8 {
		args = append(args, "-c:v", "libvpx")
		args = append(args, vp8Args(profile.bitrateKbps)...)
		args = append(args, "-f", "ivf", "-flush_packets", "1", "-")
	} else {
		args = append(args,
			"-c:v", "libx264",
			"-profile:v", "baseline",
			"-preset", "superfast",
			"-tune", "zerolatency",
			"-crf", fmt.Sprint(profile.crf),
			// Capped: crf alone spends whatever a busy picture takes,
			// spiking past what the link carries. Half a second's buffer
			// keeps the cap close.
			"-maxrate", fmt.Sprintf("%dk", profile.bitrateKbps),
			"-bufsize", fmt.Sprintf("%dk", profile.bitrateKbps/2),
			"-f", "h264",
			"-",
		)
	}
	return exec.Command("ffmpeg", args...)
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

// VideoCodec is a video track's codec: H.264, or VP8 for a peer without
// it.
type VideoCodec int

const (
	VideoCodecH264 VideoCodec = iota
	VideoCodecVP8
)

// IsKeyframe reports whether an assembled frame of codec is a keyframe,
// one a decoder can start from.
func IsKeyframe(codec VideoCodec, frame []byte) bool {
	if codec == VideoCodecVP8 {
		return isVP8Keyframe(frame)
	}
	return hasH264IDR(frame)
}

// isVP8Keyframe reports whether a VP8 frame is a keyframe: the low bit of
// VP8's first byte (the uncompressed data chunk's frame tag) is 0 for a
// key frame, 1 for an interframe (RFC 6386 §9.1).
func isVP8Keyframe(data []byte) bool {
	return len(data) > 0 && data[0]&0x1 == 0
}

// hasH264IDR reports whether an H.264 access unit (Annex-B) contains an
// IDR slice, a keyframe.
func hasH264IDR(data []byte) bool {
	for i := 0; i+3 < len(data); i++ {
		if data[i] != 0 || data[i+1] != 0 {
			continue
		}
		var nalStart int
		if data[i+2] == 1 {
			nalStart = i + 3
		} else if data[i+2] == 0 && i+3 < len(data) && data[i+3] == 1 {
			nalStart = i + 4
		} else {
			continue
		}
		if nalStart < len(data) && data[nalStart]&0x1f == 5 {
			return true
		}
	}
	return false
}
