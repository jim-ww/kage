package call

import (
	"math/rand/v2"
	"testing"
	"time"
)

// encoded is pcm as one Opus packet.
func encoded(t *testing.T, pcm []int16) []byte {
	t.Helper()
	enc, err := NewEncoder()
	if err != nil {
		t.Fatal(err)
	}
	packet, err := enc.Encode(pcm)
	if err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), packet...)
}

// device plays a Playout's frames at the device's pace: FrameSamples
// every FrameMillis, whatever length the frames come in.
type device struct {
	p       *Playout
	at      time.Duration
	samples int // pulled but not yet played
}

func (d *device) playUntil(at time.Duration) {
	for ; d.at <= at; d.at += frame {
		for d.samples < FrameSamples {
			d.samples += len(d.p.NextFrame())
		}
		d.samples -= FrameSamples
	}
}

// playoutDrift runs a sender whose clock is 0.5% fast against the device
// for two minutes - 600ms it gets ahead - with every packet's payload
// picked by payload(i), and checks it ends up caught up with nothing
// skipped.
func playoutDrift(t *testing.T, payload func(i int) []byte) {
	t.Helper()
	p, err := NewPlayout()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1000, 0)
	fast := frame * 995 / 1000
	d := &device{p: p}
	for i := range 6000 {
		at := time.Duration(i) * fast
		d.playUntil(at)
		p.push(uint16(i), uint32(i*FrameSamples), payload(i), start.Add(at+5*time.Millisecond))
	}
	if excess := p.jb.Excess(); excess > softExcess+1 || p.jb.skipped > 0 {
		t.Errorf("%d packets over target after two minutes of drift, %d skipped; want at most %d over, none skipped", excess, p.jb.skipped, softExcess+1)
	}
}

// TestPlayoutCatchesUpOnVoice checks a sender's fast clock is caught up
// on by cutting pitch periods out of speech, and its pauses.
func TestPlayoutCatchesUpOnVoice(t *testing.T) {
	voiced, silent := encoded(t, tone(FrameSamples, 200, 8000)), encoded(t, make([]int16, FrameSamples))
	playoutDrift(t, func(i int) []byte {
		if i%100 < 80 {
			return voiced
		}
		return silent
	})
}

// TestPlayoutCatchesUpOnNoise checks audio with no pitch or pause to cut
// is caught up on too.
func TestPlayoutCatchesUpOnNoise(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	packets := make([][]byte, 10)
	for i := range packets {
		packets[i] = encoded(t, noise(FrameSamples, 8000, r))
	}
	playoutDrift(t, func(i int) []byte { return packets[i%len(packets)] })
}

// TestPlayoutCatchesUpOnBacklog checks a stall's burst is played out
// faster until caught up, none of it skipped.
func TestPlayoutCatchesUpOnBacklog(t *testing.T) {
	p, err := NewPlayout()
	if err != nil {
		t.Fatal(err)
	}
	voiced := encoded(t, tone(FrameSamples, 200, 8000))
	start := time.Unix(1000, 0)
	d := &device{p: p}
	const stall = 800 * time.Millisecond
	for i := range 1000 {
		at := time.Duration(i) * frame
		arrive := at + 5*time.Millisecond
		if i >= 100 && i < 140 {
			arrive = 100*frame + stall
		}
		d.playUntil(arrive)
		p.push(uint16(i), uint32(i*FrameSamples), voiced, start.Add(arrive))
	}
	if excess := p.jb.Excess(); excess > softExcess+1 || p.jb.skipped > 0 {
		t.Errorf("%d packets over target 17s after an 800ms stall, %d skipped; want at most %d over, none skipped", excess, p.jb.skipped, softExcess+1)
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
