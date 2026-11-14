//go:build cgo

// Package call's Mic/Speaker need cgo: malgo (miniaudio bindings) is cgo-only.
// See audio_nocgo.go for the stub used when cross-compiling with
// CGO_ENABLED=0 (goreleaser's Windows/macOS release builds).

package call

// #include <stdlib.h>
import "C"

import (
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"unsafe"

	"github.com/gen2brain/malgo"
)

// audioBackends is the backend list every malgo context here is initialized
// with - nil lets miniaudio pick (PulseAudio, then ALSA, ... on Linux;
// CoreAudio/WASAPI elsewhere). Tests swap in the null backend (see
// UseNullAudio).
var (
	audioBackendsMu sync.Mutex
	audioBackends   []malgo.Backend
)

// UseNullAudio makes audio devices opened from now on use miniaudio's null
// backend: real device plumbing, keeping time, that plays and records
// nothing - for tests, which may run where there's no sound card at all.
// It returns what undoes it.
func UseNullAudio() (restore func()) {
	audioBackendsMu.Lock()
	defer audioBackendsMu.Unlock()
	prev := audioBackends
	// malgo's BackendNull is off by one: its enum skips miniaudio's
	// ma_backend_custom, so the constant names custom and null is the next.
	audioBackends = []malgo.Backend{malgo.BackendNull + 1}
	return func() {
		audioBackendsMu.Lock()
		defer audioBackendsMu.Unlock()
		audioBackends = prev
	}
}

// audioAppName is the application name kage's streams carry on a
// PulseAudio/PipeWire server, so they're identifiable in its mixer. C
// memory, allocated once: the context config holding it is passed to C,
// which may not be handed a Go pointer nested inside another.
var audioAppName = C.CString("kage")

// newAudioContext opens a malgo context on audioBackends.
func newAudioContext() (*malgo.AllocatedContext, error) {
	var cfg malgo.ContextConfig
	cfg.Pulse.PApplicationName = (*byte)(unsafe.Pointer(audioAppName))
	audioBackendsMu.Lock()
	backends := audioBackends
	audioBackendsMu.Unlock()
	return malgo.InitContext(backends, cfg, func(string) {})
}

// AudioDevices lists the capture and playback devices the audio backend can
// open, by the names Mic/Speaker accept. PulseAudio/PipeWire's monitor
// sources ("Monitor of ...", a copy of what an output plays) are left out
// of the inputs: they aren't microphones. So is anything without a usable
// name (ALSA lists some PCMs with a blank one), since a name is all a
// device is picked by.
func AudioDevices() (inputs, outputs []AudioDevice, err error) {
	ctx, err := newAudioContext()
	if err != nil {
		return nil, nil, fmt.Errorf("initializing audio context: %w", err)
	}
	defer func() {
		ctx.Uninit()
		ctx.Free()
	}()
	capture, err := ctx.Devices(malgo.Capture)
	if err != nil {
		return nil, nil, fmt.Errorf("listing capture devices: %w", err)
	}
	playback, err := ctx.Devices(malgo.Playback)
	if err != nil {
		return nil, nil, fmt.Errorf("listing playback devices: %w", err)
	}
	for _, d := range capture {
		if strings.TrimSpace(d.Name()) == "" || strings.HasPrefix(d.Name(), "Monitor of ") {
			continue
		}
		inputs = append(inputs, AudioDevice{Name: d.Name(), Default: d.IsDefault != 0})
	}
	for _, d := range playback {
		if strings.TrimSpace(d.Name()) == "" {
			continue
		}
		outputs = append(outputs, AudioDevice{Name: d.Name(), Default: d.IsDefault != 0})
	}
	return inputs, outputs, nil
}

// findDevice resolves a device name to the ID malgo opens it by - nil (the
// system default) for "" or a device that isn't there anymore (unplugged
// headset), so a stale choice degrades to the default instead of failing
// the call.
func findDevice(ctx *malgo.AllocatedContext, kind malgo.DeviceType, name string) *malgo.DeviceID {
	if name == "" {
		return nil
	}
	devices, err := ctx.Devices(kind)
	if err != nil {
		return nil
	}
	for i := range devices {
		if devices[i].Name() == name {
			id := devices[i].ID
			return &id
		}
	}
	return nil
}

// openDevice initializes and starts a malgo device on its own context.
// DeviceID.Pointer hands back a C copy of the ID that's never freed - one
// small allocation per opened device, kept alive for as long as the device
// is, which is what miniaudio expects of it anyway.
// fellBack reports a named device that isn't there, opened as the default
// instead.
func openDevice(kind malgo.DeviceType, name string, data malgo.DataProc) (_ *malgo.AllocatedContext, _ *malgo.Device, fellBack bool, _ error) {
	ctx, err := newAudioContext()
	if err != nil {
		return nil, nil, false, fmt.Errorf("initializing audio context: %w", err)
	}
	cfg := malgo.DefaultDeviceConfig(kind)
	cfg.SampleRate = SampleRate
	id := findDevice(ctx, kind, name)
	fellBack = name != "" && id == nil
	if kind == malgo.Capture {
		cfg.Capture.Format, cfg.Capture.Channels = malgo.FormatS16, Channels
		if id != nil {
			cfg.Capture.DeviceID = id.Pointer()
		}
	} else {
		cfg.Playback.Format, cfg.Playback.Channels = malgo.FormatS16, Channels
		if id != nil {
			cfg.Playback.DeviceID = id.Pointer()
		}
	}
	device, err := malgo.InitDevice(ctx.Context, cfg, malgo.DeviceCallbacks{Data: data})
	if err != nil {
		ctx.Uninit()
		ctx.Free()
		return nil, nil, false, fmt.Errorf("opening audio device: %w", err)
	}
	if err := device.Start(); err != nil {
		device.Uninit()
		ctx.Uninit()
		ctx.Free()
		return nil, nil, false, fmt.Errorf("starting audio device: %w", err)
	}
	return ctx, device, fellBack, nil
}

// Mic captures audio from an input device via malgo (miniaudio bindings) and
// delivers it as fixed-size (FrameSamples) mono int16 frames on Frames().
// Callers that want to encode with Opus can read directly off that channel.
//
// Not github.com/jfreymuth/pulse (used here in an earlier pass): it only
// speaks the PulseAudio wire protocol, so it's Linux-only by construction,
// and in practice proved unreliable talking to this daemon's audio session
// even there. malgo dlopens the platform's real native backend (PulseAudio/
// ALSA on Linux, CoreAudio on macOS, WASAPI/WinMM on Windows), which is both
// more robust and actually cross-platform.
type Mic struct {
	ctx      *malgo.AllocatedContext
	device   *malgo.Device
	frames   chan []int16
	fellBack bool
}

// NewMic opens the named capture device ("" for the system default, see
// AudioDevices for names) at SampleRate/Channels. Close must be called when
// the call ends to release it.
func NewMic(device string) (*Mic, error) {
	m := &Mic{frames: make(chan []int16, 8)}

	// malgo calls this synchronously on its own audio thread as samples
	// arrive; a frame-sized buffer is accumulated and handed off to
	// Frames() so callers see clean FrameSamples-sized chunks to feed the
	// Opus encoder 20ms at a time. inputSamples is raw little-endian S16.
	var pending []int16
	data := func(_, inputSamples []byte, _ uint32) {
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
	}

	ctx, dev, fellBack, err := openDevice(malgo.Capture, device, data)
	if err != nil {
		return nil, fmt.Errorf("capture: %w", err)
	}
	m.ctx, m.device, m.fellBack = ctx, dev, fellBack
	return m, nil
}

// UsingDefaultInstead reports the device asked for wasn't there, so the
// system default was opened in its place.
func (m *Mic) UsingDefaultInstead() bool { return m.fellBack }

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

// Speaker plays back 16-bit PCM frames through an output device via malgo.
// Playback is pulled by the device's own audio thread: whenever it runs dry
// it plays silence rather than waiting, so a late frame is a gap, not a
// stall.
type Speaker struct {
	ctx       *malgo.AllocatedContext
	device    *malgo.Device
	samples   chan []int16
	closed    chan struct{}
	closeOnce sync.Once
	fellBack  bool
}

// NewSpeaker opens the named playback device ("" for the system default,
// see AudioDevices for names) at SampleRate/Channels. Close must be called
// when the call ends.
func NewSpeaker(device string) (*Speaker, error) {
	return newSpeaker(device, nil)
}

// NewSpeakerFrom opens a speaker like NewSpeaker, playing whatever next
// returns instead of what's written to it: next is called on the device's
// own audio thread each time the frame before has been played, so the
// device's clock sets the pace and next must never block. A nil or empty
// frame plays silence until the device next needs samples.
func NewSpeakerFrom(device string, next func() []int16) (*Speaker, error) {
	return newSpeaker(device, next)
}

func newSpeaker(device string, next func() []int16) (*Speaker, error) {
	s := &Speaker{
		samples: make(chan []int16, 8),
		closed:  make(chan struct{}),
	}
	if next == nil {
		next = func() []int16 {
			select {
			case frame := <-s.samples:
				return frame
			default:
				return nil
			}
		}
	}

	var pending []int16
	data := func(out, _ []byte, _ uint32) {
		n := 0
		for n+1 < len(out) {
			if len(pending) == 0 {
				if pending = next(); len(pending) == 0 {
					clear(out[n:])
					return
				}
			}
			binary.LittleEndian.PutUint16(out[n:], uint16(pending[0]))
			pending = pending[1:]
			n += 2
		}
	}

	ctx, dev, fellBack, err := openDevice(malgo.Playback, device, data)
	if err != nil {
		return nil, fmt.Errorf("playback: %w", err)
	}
	s.ctx, s.device, s.fellBack = ctx, dev, fellBack
	return s, nil
}

// UsingDefaultInstead reports the device asked for wasn't there, so the
// system default was opened in its place.
func (s *Speaker) UsingDefaultInstead() bool { return s.fellBack }

// Write queues one frame of mono 16-bit PCM samples for playback, on a
// speaker opened by NewSpeaker. It blocks if the queue is full (the
// playback engine isn't keeping up).
func (s *Speaker) Write(frame []int16) error {
	select {
	case s.samples <- frame:
		return nil
	case <-s.closed:
		return fmt.Errorf("speaker closed")
	}
}

// Close stops playback. Any Write in progress unblocks with an error.
func (s *Speaker) Close() error {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.device.Uninit()
		s.ctx.Uninit()
		s.ctx.Free()
	})
	return nil
}
