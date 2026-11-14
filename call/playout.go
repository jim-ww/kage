package call

import (
	"log/slog"
	"sync"
	"time"
)

const (
	// quietLevel is the mean absolute sample level (about -47 dBFS) below
	// which a frame can be shortened as much as accelerate likes.
	quietLevel = 150

	// forceAfter is how many frames in a row the buffer may stay over
	// softExcess with nothing voiced or quiet to accelerate before a frame
	// is shortened anyway, so audio that's neither (music, noise) can't
	// build up a delay either.
	forceAfter = 25

	// catchUpRate is the share of speech accelerate may cut while catching
	// up (1.25x speed), fastCatchUpRate past fastExcess packets of backlog
	// (1.5x): fast enough to catch up in seconds, slow enough to follow.
	// Quiet frames are halved regardless.
	catchUpRate     = 0.2
	fastCatchUpRate = 1.0 / 3
	fastExcess      = 10

	// maxPacketSamples is the longest Opus packet, 120ms.
	maxPacketSamples = SampleRate * 120 / 1000
)

// Playout turns the peer's received Opus packets into PCM for a Speaker
// (see NewSpeakerFrom), through a JitterBuffer: packets are pushed as they
// arrive, and NextFrame decodes the next one when the device needs it, so
// it's the device's clock that sets the pace of playback. It outlives the
// speaker, so switching output devices keeps the stream's state.
type Playout struct {
	jb *JitterBuffer

	mu      sync.Mutex
	dec     *Decoder
	pcm     []int16
	overFor int     // frames in a row over softExcess that weren't shortened
	credit  float64 // samples of speech accelerate may still cut
}

// NewPlayout returns a Playout with nothing received yet.
func NewPlayout() (*Playout, error) {
	dec, err := NewDecoder()
	if err != nil {
		return nil, err
	}
	return &Playout{jb: NewJitterBuffer(), dec: dec, pcm: make([]int16, maxPacketSamples*Channels)}, nil
}

// Push queues one received RTP packet's Opus payload.
func (p *Playout) Push(seq uint16, ts uint32, payload []byte) {
	p.push(seq, ts, payload, time.Now())
}

func (p *Playout) push(seq uint16, ts uint32, payload []byte, now time.Time) {
	p.jb.Push(seq, ts, payload, now)
}

// NextFrame returns the next frame to play. It's called on the audio
// device's own thread, so it never waits for a packet: one that isn't
// there yet is concealed or played as silence.
func (p *Playout) NextFrame() []int16 {
	p.mu.Lock()
	defer p.mu.Unlock()
	payload, kind := p.jb.Pop()
	frame := p.decode(payload, kind)
	excess := p.jb.Excess()
	if kind != popPacket || excess <= softExcess {
		p.overFor = 0
		return frame
	}
	rate := catchUpRate
	if excess > fastExcess {
		rate = fastCatchUpRate
	}
	// Capped at one longest pitch period, so a stretch with nothing to cut
	// doesn't save up for a burst of cuts after it.
	p.credit = min(p.credit+float64(len(frame))*rate, maxPitch)
	wasQuiet := quiet(frame)
	frame, cut := accelerate(frame, p.overFor >= forceAfter, int(p.credit))
	if cut == 0 {
		p.overFor++
		return frame
	}
	p.overFor = 0
	if !wasQuiet {
		p.credit -= float64(cut)
	}
	return frame
}

func (p *Playout) decode(payload []byte, kind popKind) []int16 {
	switch kind {
	case popPacket:
		n, err := p.dec.Decode(payload, p.pcm)
		if err == nil {
			// The speaker keeps the frame until it's played, so it can't
			// be the reused decode buffer.
			return append([]int16(nil), p.pcm[:n*Channels]...)
		}
		slog.Warn("decoding call audio", "err", err)
		fallthrough
	case popLost:
		frame := make([]int16, FrameSamples*Channels)
		if err := p.dec.Conceal(frame); err != nil {
			slog.Warn("concealing call audio", "err", err)
			clear(frame)
		}
		return frame
	default:
		return make([]int16, FrameSamples*Channels)
	}
}

// quiet reports whether frame is near silence.
func quiet(frame []int16) bool {
	var sum int
	for _, s := range frame {
		sum += max(int(s), -int(s))
	}
	return len(frame) == 0 || sum/len(frame) < quietLevel
}
