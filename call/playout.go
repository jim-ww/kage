package call

import (
	"log/slog"
	"sync"
	"time"
)

const (
	// quietLevel is the mean absolute sample level (about -47 dBFS) below
	// which a frame can be dropped unnoticed to catch up on a backlog.
	quietLevel = 150

	// driftDropAfter is how many frames in a row the buffer may stay over
	// softExcess with nothing quiet to drop before one is dropped anyway,
	// so a sender who never stops talking can't build up a delay either.
	driftDropAfter = 50

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
	overFor int // frames in a row the buffer's been over softExcess
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
	for {
		payload, kind := p.jb.Pop()
		frame := p.decode(payload, kind)
		if kind != popPacket || p.jb.Excess() <= softExcess {
			p.overFor = 0
			return frame
		}
		// Every packet is still decoded, dropped or not: Opus decoding
		// carries state from one packet to the next.
		p.overFor++
		if quiet(frame) || p.overFor >= driftDropAfter {
			p.overFor = 0
			continue
		}
		return frame
	}
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
