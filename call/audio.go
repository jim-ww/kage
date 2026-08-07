package call

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync"

	"github.com/ebitengine/oto/v3"
	"github.com/gen2brain/malgo"
)

// Mic captures audio from the system's default input device via malgo
// (miniaudio bindings) and delivers it as fixed-size (FrameSamples) mono
// int16 frames on Frames(). Callers that want to encode with Opus can read
// directly off that channel.
//
// Not github.com/jfreymuth/pulse (used here in an earlier pass): it only
// speaks the PulseAudio wire protocol, so it's Linux-only by construction,
// and in practice proved unreliable talking to this daemon's audio session
// even there. malgo dlopens the platform's real native backend (ALSA/
// PulseAudio on Linux, CoreAudio on macOS, WASAPI/WinMM on Windows) the same
// way oto's Speaker below already does for playback, which is both more
// robust and actually cross-platform.
type Mic struct {
	ctx    *malgo.AllocatedContext
	device *malgo.Device
	frames chan []int16
}

// NewMic opens the default capture device at SampleRate/Channels. Close
// must be called when the call ends to release it.
func NewMic() (*Mic, error) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(string) {})
	if err != nil {
		return nil, fmt.Errorf("initializing audio capture context: %w", err)
	}

	m := &Mic{
		ctx:    ctx,
		frames: make(chan []int16, 8),
	}

	cfg := malgo.DefaultDeviceConfig(malgo.Capture)
	cfg.Capture.Format = malgo.FormatS16
	cfg.Capture.Channels = Channels
	cfg.SampleRate = SampleRate

	// malgo calls this synchronously on its own audio thread as samples
	// arrive; a frame-sized buffer is accumulated and handed off to
	// Frames() so callers see clean FrameSamples-sized chunks to feed the
	// Opus encoder 20ms at a time. inputSamples is raw little-endian S16
	// bytes per cfg.Capture.Format/Channels above.
	var pending []int16
	callbacks := malgo.DeviceCallbacks{
		Data: func(_, inputSamples []byte, _ uint32) {
			for i := 0; i+1 < len(inputSamples); i += 2 {
				pending = append(pending, int16(binary.LittleEndian.Uint16(inputSamples[i:])))
			}
			for len(pending) >= FrameSamples {
				frame := make([]int16, FrameSamples)
				copy(frame, pending[:FrameSamples])
				pending = pending[FrameSamples:]
				select {
				case m.frames <- frame:
				default: // caller isn't keeping up; drop rather than block the audio thread
				}
			}
		},
	}

	device, err := malgo.InitDevice(ctx.Context, cfg, callbacks)
	if err != nil {
		ctx.Uninit()
		ctx.Free()
		return nil, fmt.Errorf("opening capture device: %w", err)
	}
	if err := device.Start(); err != nil {
		device.Uninit()
		ctx.Uninit()
		ctx.Free()
		return nil, fmt.Errorf("starting capture device: %w", err)
	}
	m.device = device

	return m, nil
}

// Frames returns the channel of captured FrameSamples-sized PCM frames.
func (m *Mic) Frames() <-chan []int16 {
	return m.frames
}

// Close stops capture and releases the underlying device/context.
func (m *Mic) Close() error {
	m.device.Uninit()
	m.ctx.Uninit()
	m.ctx.Free()
	return nil
}

// otoContext is a lazily-initialized, process-wide singleton: oto explicitly
// does not support creating more than one Context, so every Speaker across
// every call in this daemon's lifetime shares the one Context and gets its
// own Player on top of it. On Linux, oto v3 requires cgo (`#cgo pkg-config:
// alsa` in its driver_unix.go) and tries PulseAudio/PipeWire first, falling
// back to dlopening libasound.so directly if no server answers - real extra
// robustness confirmed live (oto's playback worked when the pulse-only Mic
// implementation it replaced did not). On macOS/Windows this gets us
// CoreAudio/WASAPI natively.
var (
	otoCtxOnce sync.Once
	otoCtx     *oto.Context
	otoCtxErr  error
)

func sharedOtoContext() (*oto.Context, error) {
	otoCtxOnce.Do(func() {
		var ready chan struct{}
		otoCtx, ready, otoCtxErr = oto.NewContext(&oto.NewContextOptions{
			SampleRate:   SampleRate,
			ChannelCount: Channels,
			Format:       oto.FormatSignedInt16LE,
		})
		if otoCtxErr == nil {
			<-ready
		}
	})
	return otoCtx, otoCtxErr
}

// Speaker plays back 16-bit PCM frames through the system's default audio
// output, via oto (see sharedOtoContext).
type Speaker struct {
	player  *oto.Player
	samples chan []int16
	closed  chan struct{}
}

// NewSpeaker opens the default playback device at SampleRate/Channels.
// Close must be called when the call ends.
func NewSpeaker() (*Speaker, error) {
	ctx, err := sharedOtoContext()
	if err != nil {
		return nil, fmt.Errorf("initializing audio output: %w", err)
	}

	s := &Speaker{
		samples: make(chan []int16, 8),
		closed:  make(chan struct{}),
	}
	s.player = ctx.NewPlayer(&speakerReader{s: s})
	s.player.Play()

	return s, nil
}

// speakerReader adapts Speaker's frame channel to the io.Reader oto.Player
// pulls from on its own goroutine - same blocking-until-a-frame-arrives shape
// the old jfreymuth/pulse Int16Reader callback used, just over raw
// little-endian bytes instead of []int16 since that's oto's interface.
type speakerReader struct {
	s       *Speaker
	pending []int16
}

func (r *speakerReader) Read(buf []byte) (int, error) {
	for len(r.pending) == 0 {
		select {
		case frame := <-r.s.samples:
			r.pending = frame
		case <-r.s.closed:
			return 0, io.EOF
		}
	}
	n := 0
	for n+2 <= len(buf) && len(r.pending) > 0 {
		binary.LittleEndian.PutUint16(buf[n:], uint16(r.pending[0]))
		r.pending = r.pending[1:]
		n += 2
	}
	return n, nil
}

// Write queues one frame of mono 16-bit PCM samples for playback. It blocks
// if the queue is full (the playback engine isn't keeping up).
func (s *Speaker) Write(frame []int16) error {
	select {
	case s.samples <- frame:
		return nil
	case <-s.closed:
		return fmt.Errorf("speaker closed")
	}
}

// Close stops playback. Any Write in progress will unblock with an error.
// The shared oto.Context itself is never closed - it's reused by the next
// call's Speaker.
func (s *Speaker) Close() error {
	close(s.closed)
	return s.player.Close()
}
