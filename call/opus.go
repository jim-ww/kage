// Package call is the (not yet wired up) audio-call package: Opus
// encode/decode, mic/speaker I/O, and a pion/webrtc peer connection for a
// single audio-only Jingle call. It has no dependency on xmpp, ui, or the
// daemon's adapter yet - see call/peer.go for what's stubbed out.
package call

import (
	"encoding/binary"
	"fmt"

	"github.com/pion/opus"
)

// SampleRate and FrameSamples are the standard Opus/WebRTC voice settings
// this package is built around: 48kHz mono, 20ms frames.
const (
	SampleRate   = 48000
	Channels     = 1
	FrameMillis  = 20
	FrameSamples = SampleRate * FrameMillis / 1000 // 960 samples/frame

	// maxPacketBytes bounds a single encoded Opus frame at this bitrate/frame
	// size with headroom - RFC 6716 packets are self-delimiting, this is just
	// a caller-owned buffer size for Encoder.Encode's out param.
	maxPacketBytes = 1500
)

// Encoder turns 16-bit PCM frames into Opus packets for the outgoing RTP
// track. github.com/pion/opus only grew a public Encoder after its v0.1.0
// tag (see NewEncoder in encoder.go on later commits) - go.mod pins a
// post-v0.1.0 pseudo-version specifically for this.
type Encoder struct {
	enc *opus.Encoder
}

// NewEncoder creates an Encoder tuned for voice at SampleRate/Channels.
func NewEncoder() (*Encoder, error) {
	enc, err := opus.NewEncoder(
		opus.WithSampleRate(SampleRate),
		opus.WithChannels(Channels),
		opus.WithApplication(opus.ApplicationVoIP),
		opus.WithVBR(true),
	)
	if err != nil {
		return nil, fmt.Errorf("creating opus encoder: %w", err)
	}
	return &Encoder{enc: enc}, nil
}

// Encode encodes one FrameSamples frame of 16-bit PCM into an Opus packet,
// returning the packet bytes. The returned slice is only valid until the
// next call to Encode.
func (e *Encoder) Encode(pcm []int16) ([]byte, error) {
	if len(pcm) != FrameSamples*Channels {
		return nil, fmt.Errorf("encoding opus frame: got %d samples, want %d", len(pcm), FrameSamples*Channels)
	}
	in := make([]byte, len(pcm)*2)
	for i, s := range pcm {
		binary.LittleEndian.PutUint16(in[i*2:], uint16(s))
	}
	out := make([]byte, maxPacketBytes)
	n, err := e.enc.Encode(in, out)
	if err != nil {
		return nil, fmt.Errorf("encoding opus frame: %w", err)
	}
	return out[:n], nil
}

// Decoder turns received Opus packets back into 16-bit PCM frames.
type Decoder struct {
	dec opus.Decoder
}

// NewDecoder creates a Decoder producing SampleRate/Channels PCM.
func NewDecoder() (*Decoder, error) {
	dec, err := opus.NewDecoderWithOutput(SampleRate, Channels)
	if err != nil {
		return nil, fmt.Errorf("creating opus decoder: %w", err)
	}
	return &Decoder{dec: dec}, nil
}

// Decode decodes one Opus packet into out, a caller-owned buffer of at
// least FrameSamples*Channels int16s, returning the number of samples
// written per channel.
func (d *Decoder) Decode(packet []byte, out []int16) (int, error) {
	n, err := d.dec.DecodeToInt16(packet, out)
	if err != nil {
		return 0, fmt.Errorf("decoding opus packet: %w", err)
	}
	return n, nil
}
