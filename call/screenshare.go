package call

import (
	"fmt"
	"io"
	"os/exec"
	"time"
)

// screenShareFramerate is a conservative cap for screen-share capture -
// rarely needs more than this to read comfortably, and keeps bandwidth/CPU
// sane over a STUN-brokered path the way FrameMillis does for audio.
const screenShareFramerate = 15

// ScreenShare captures the current output via wf-recorder (wlroots'
// screencopy protocol) as a raw H.264 Annex-B bytestream - no container, so
// the stream can be split into NAL units and pushed onto a WebRTC video
// track directly, the same way call/audio.go's Mic feeds Opus frames to the
// peer connection.
type ScreenShare struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
	done   chan struct{}
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
		"-r", fmt.Sprint(screenShareFramerate),
		"-f", "/dev/stdout",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("opening wf-recorder stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting wf-recorder: %w", err)
	}
	return &ScreenShare{cmd: cmd, stdout: stdout, done: make(chan struct{})}, nil
}

// Run reads Annex-B NAL units off wf-recorder's stdout and calls write for
// each one (with its start code included), until the stream ends or Stop is
// called. Blocks until then - callers run it in its own goroutine.
func (s *ScreenShare) Run(write func(nal []byte, sinceLast time.Duration) error) error {
	buf := make([]byte, 0, 1<<20)
	tmp := make([]byte, 1<<16)
	last := time.Now()
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
				now := time.Now()
				sinceLast := now.Sub(last)
				last = now
				if werr := write(nal, sinceLast); werr != nil {
					return werr
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// Stop terminates wf-recorder and unblocks Run.
func (s *ScreenShare) Stop() {
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

// splitNextNAL extracts the first complete Annex-B NAL unit (start code
// included) from buf, once a following start code (marking the next NAL) has
// arrived to show where it ends. ok is false if there's not a full NAL to
// emit yet - the caller should wait for more data.
func splitNextNAL(buf []byte) (nal, rest []byte, ok bool) {
	start := indexStartCode(buf, 0)
	if start < 0 {
		return nil, buf, false
	}
	next := indexStartCode(buf, start+3)
	if next < 0 {
		return nil, buf, false
	}
	return buf[start:next], buf[next:], true
}

// indexStartCode finds the byte offset right after the next Annex-B start
// code (00 00 01, whether preceded by one extra zero byte or not) at or
// after from, or -1 if none has arrived yet.
func indexStartCode(buf []byte, from int) int {
	for i := from; i+2 < len(buf); i++ {
		if buf[i] == 0 && buf[i+1] == 0 && buf[i+2] == 1 {
			return i + 3
		}
	}
	return -1
}

// ScreenViewer plays a peer's shared-screen video track by piping its
// depacketized H.264 Annex-B stream into mpv, which does its own decode -
// kage never touches a decoded video frame itself, mirroring how gpg is
// shelled out to rather than reimplemented (see crypto/gpg).
type ScreenViewer struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
}

// NewScreenViewer launches mpv reading a raw H.264 stream on stdin.
func NewScreenViewer(title string) (*ScreenViewer, error) {
	cmd := exec.Command(
		"mpv",
		"--title="+title,
		"--force-window=immediate",
		"--profile=low-latency",
		"--demuxer=lavf",
		"--demuxer-lavf-format=h264",
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
	return &ScreenViewer{cmd: cmd, stdin: stdin}, nil
}

// WriteNAL writes one Annex-B NAL unit (start code included) to mpv's input
// stream.
func (v *ScreenViewer) WriteNAL(nal []byte) error {
	_, err := v.stdin.Write(nal)
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
