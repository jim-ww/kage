package call

import (
	"math"
	"testing"
	"time"
)

// encodedFrame is one Opus packet of a steady tone at the given amplitude.
func encodedFrame(t *testing.T, amplitude float64) []byte {
	t.Helper()
	enc, err := NewEncoder()
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]int16, FrameSamples)
	for i := range pcm {
		pcm[i] = int16(amplitude * math.Sin(2*math.Pi*440*float64(i)/SampleRate))
	}
	packet, err := enc.Encode(pcm)
	if err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), packet...)
}

// playoutDrift runs a sender whose clock is 0.5% fast against the device
// for two minutes - 600ms it gets ahead - with every packet's payload
// picked by payload(i), and returns how far over target it ends up.
func playoutDrift(t *testing.T, payload func(i int) []byte) int {
	t.Helper()
	p, err := NewPlayout()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1000, 0)
	fast := frame * 995 / 1000
	var pops time.Duration
	for i := range 6000 {
		at := time.Duration(i) * fast
		for ; pops <= at; pops += frame {
			p.NextFrame()
		}
		p.push(uint16(i), uint32(i*FrameSamples), payload(i), start.Add(at+5*time.Millisecond))
	}
	return p.jb.Excess()
}

// TestPlayoutCatchesUpInPauses checks a sender's fast clock is caught up
// on by dropping quiet frames, the gaps between words.
func TestPlayoutCatchesUpInPauses(t *testing.T) {
	loud, silent := encodedFrame(t, 8000), encodedFrame(t, 0)
	excess := playoutDrift(t, func(i int) []byte {
		if i%100 < 80 {
			return loud
		}
		return silent
	})
	if excess > softExcess+1 {
		t.Errorf("%d packets over target after two minutes of drift, want at most %d", excess, softExcess+1)
	}
}

// TestPlayoutCatchesUpWithoutPauses checks the delay stays bounded even
// with no quiet frame to drop.
func TestPlayoutCatchesUpWithoutPauses(t *testing.T) {
	loud := encodedFrame(t, 8000)
	excess := playoutDrift(t, func(int) []byte { return loud })
	if excess > softExcess+1 {
		t.Errorf("%d packets over target after two minutes of drift, want at most %d", excess, softExcess+1)
	}
}

// TestPlayoutPlaysSilenceBeforeAnyPacket checks the device gets full
// frames of silence before the peer's audio starts.
func TestPlayoutPlaysSilenceBeforeAnyPacket(t *testing.T) {
	p, err := NewPlayout()
	if err != nil {
		t.Fatal(err)
	}
	f := p.NextFrame()
	if len(f) != FrameSamples*Channels || !quiet(f) {
		t.Fatalf("got %d samples (quiet: %v), want a silent frame", len(f), quiet(f))
	}
}
