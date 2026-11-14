package call

import (
	"math/rand/v2"
	"slices"
	"testing"
	"time"
)

const frame = FrameMillis * time.Millisecond

// jitterSim plays a sender pushing packets into a JitterBuffer against a
// device popping one frame every FrameMillis, on a fake clock.
type jitterSim struct {
	b     *JitterBuffer
	start time.Time
	seq   uint16
	ts    uint32
	now   time.Duration // receiver clock
	pops  time.Duration // when the device next pops
	sent  map[uint16]time.Duration
	// latencies are, per played packet, how long after it was sent it
	// played.
	latencies []time.Duration
	played    []uint16
	lost      int
}

func newJitterSim(seq uint16) *jitterSim {
	return &jitterSim{b: NewJitterBuffer(), start: time.Unix(1000, 0), seq: seq, ts: 12345, sent: map[uint16]time.Duration{}}
}

// advance runs the device's pops up to t.
func (s *jitterSim) advance(t time.Duration) {
	for s.pops <= t {
		payload, kind := s.b.Pop()
		switch kind {
		case popPacket:
			seq := uint16(payload[0])<<8 | uint16(payload[1])
			s.played = append(s.played, seq)
			s.latencies = append(s.latencies, s.pops-s.sent[seq])
		case popLost:
			s.lost++
		}
		s.pops += frame
	}
	s.now = t
}

// send makes the next packet, sent at the sender's time at, without
// delivering it.
func (s *jitterSim) send(at time.Duration) (seq uint16, ts uint32) {
	seq, ts = s.seq, s.ts
	s.sent[seq] = at
	s.seq++
	s.ts += FrameSamples
	return seq, ts
}

// deliver pushes a sent packet, arriving at.
func (s *jitterSim) deliver(seq uint16, ts uint32, at time.Duration) {
	s.advance(at)
	s.b.Push(seq, ts, []byte{byte(seq >> 8), byte(seq)}, s.start.Add(at))
}

// talk sends n packets one frame apart from the sender's time at, each
// delayed by delay(i).
func (s *jitterSim) talk(at time.Duration, n int, delay func(i int) time.Duration) time.Duration {
	type arrival struct {
		seq uint16
		ts  uint32
		at  time.Duration
	}
	var arrivals []arrival
	for i := range n {
		sentAt := at + time.Duration(i)*frame
		seq, ts := s.send(sentAt)
		arrivals = append(arrivals, arrival{seq, ts, sentAt + delay(i)})
	}
	slices.SortStableFunc(arrivals, func(a, b arrival) int { return int(a.at - b.at) })
	for _, a := range arrivals {
		s.deliver(a.seq, a.ts, a.at)
	}
	return at + time.Duration(n)*frame
}

func noDelay(int) time.Duration { return 5 * time.Millisecond }

// TestJitterBufferSteadyStream checks a steady stream plays in order, every
// packet, with only the minimum target's delay.
func TestJitterBufferSteadyStream(t *testing.T) {
	s := newJitterSim(100)
	end := s.talk(0, 500, noDelay)
	s.advance(end + time.Second)
	if len(s.played) != 500 || !slices.IsSorted(s.played) {
		t.Fatalf("played %d packets, sorted %v; want all 500 in order", len(s.played), slices.IsSorted(s.played))
	}
	if got := s.b.Target(); got != minTarget {
		t.Errorf("target %d, want %d on a jitter-free stream", got, minTarget)
	}
	if l := s.latencies[len(s.latencies)-1]; l > time.Duration(minTarget+1)*frame {
		t.Errorf("settled latency %v, want at most %v", l, time.Duration(minTarget+1)*frame)
	}
}

// TestJitterBufferReorders checks packets arriving out of order still play
// in order.
func TestJitterBufferReorders(t *testing.T) {
	s := newJitterSim(0)
	end := s.talk(0, 200, func(i int) time.Duration {
		if i%10 == 3 {
			return 25 * time.Millisecond // overtaken by the next one
		}
		return 5 * time.Millisecond
	})
	s.advance(end + time.Second)
	if len(s.played) != 200 || !slices.IsSorted(s.played) {
		t.Fatalf("played %v; want all 200 in order", s.played)
	}
}

// TestJitterBufferConcealsLoss checks a lost packet is concealed and the
// rest still play.
func TestJitterBufferConcealsLoss(t *testing.T) {
	s := newJitterSim(0)
	at := s.talk(0, 50, noDelay)
	s.send(at) // lost
	end := s.talk(at+frame, 50, noDelay)
	s.advance(end + time.Second)
	if len(s.played) != 100 || slices.Contains(s.played, 50) {
		t.Fatalf("played %d packets (50 among them: %v), want the 100 that arrived", len(s.played), slices.Contains(s.played, 50))
	}
	if s.lost < 1 {
		t.Errorf("concealed %d frames, want the lost one", s.lost)
	}
}

// TestJitterBufferKeepsBacklog checks a stall's burst is all kept, for
// Playout to catch up on rather than lose.
func TestJitterBufferKeepsBacklog(t *testing.T) {
	s := newJitterSim(0)
	at := s.talk(0, 100, noDelay)
	stall := 600 * time.Millisecond
	at = s.talk(at, 30, func(i int) time.Duration { return stall - time.Duration(i)*frame })
	end := s.talk(at, 100, noDelay)
	s.advance(end + time.Second)
	if len(s.played) != 230 || s.b.skipped != 0 {
		t.Fatalf("played %d packets, skipped %d; want all 230 played", len(s.played), s.b.skipped)
	}
}

// TestJitterBufferCapsBacklog checks a backlog longer than hardExcess is
// cut down to it, so a long stall can't leave the call that far behind.
func TestJitterBufferCapsBacklog(t *testing.T) {
	s := newJitterSim(0)
	at := s.talk(0, 100, noDelay)
	stall := 2 * time.Second
	at = s.talk(at, 100, func(i int) time.Duration { return stall - time.Duration(i)*frame })
	end := s.talk(at, 500, noDelay)
	s.advance(end + time.Second)
	limit := time.Duration(minTarget+hardExcess+1) * frame
	if l := slices.Max(s.latencies[len(s.latencies)-100:]); l > limit {
		t.Errorf("latency after the stall %v, want at most %v", l, limit)
	}
	if s.b.skipped == 0 {
		t.Error("nothing skipped, want the backlog past hardExcess skipped")
	}
}

// TestJitterBufferAdaptsToJitter checks the target grows while packets
// arrive irregularly, so they don't play late as losses, and shrinks back
// once they're steady again.
func TestJitterBufferAdaptsToJitter(t *testing.T) {
	s := newJitterSim(0)
	r := rand.New(rand.NewPCG(1, 2))
	at := s.talk(0, 1000, func(int) time.Duration { return time.Duration(r.IntN(80)) * time.Millisecond })
	jittery := s.b.Target()
	if jittery < 4 {
		t.Errorf("target %d under 0-80ms jitter, want at least 4", jittery)
	}
	lostBefore := s.lost
	at = s.talk(at, 500, func(int) time.Duration { return time.Duration(r.IntN(80)) * time.Millisecond })
	if lost := s.lost - lostBefore; lost > 25 {
		t.Errorf("%d of 500 frames concealed once adapted, want under 5%%", lost)
	}
	end := s.talk(at, 1000, noDelay)
	s.advance(end + time.Second)
	if got := s.b.Target(); got != minTarget {
		t.Errorf("target %d after jitter ended, want back to %d", got, minTarget)
	}
}

// TestJitterBufferPauseIsNotJitter checks a sender pausing (kage sends
// nothing while muted, and its timestamps don't count the pause) doesn't
// read as a packet that much late, growing the buffer.
func TestJitterBufferPauseIsNotJitter(t *testing.T) {
	s := newJitterSim(0)
	at := s.talk(0, 100, noDelay)
	end := s.talk(at+3*time.Second, 500, noDelay)
	s.advance(end + time.Second)
	if got := s.b.Target(); got != minTarget {
		t.Errorf("target %d after a pause, want %d", got, minTarget)
	}
	if l := s.latencies[len(s.latencies)-1]; l > 100*time.Millisecond {
		t.Errorf("latency after the pause %v, want under 100ms", l)
	}
}

// TestJitterBufferSequenceWraps checks sequence numbers wrapping past
// 65535 play on in order.
func TestJitterBufferSequenceWraps(t *testing.T) {
	s := newJitterSim(65500)
	end := s.talk(0, 100, noDelay)
	s.advance(end + time.Second)
	if len(s.played) != 100 || s.played[99] != 63 {
		t.Fatalf("played %d packets ending at %d, want 100 ending at 63", len(s.played), s.played[len(s.played)-1])
	}
}

// TestJitterBufferStreamRestart checks a jump in sequence numbers (the
// sender's stream started over) is followed rather than dropped as late.
func TestJitterBufferStreamRestart(t *testing.T) {
	s := newJitterSim(30000)
	at := s.talk(0, 50, noDelay)
	s.seq = 100
	end := s.talk(at, 50, noDelay)
	s.advance(end + time.Second)
	if n := len(s.played); n < 95 || s.played[n-1] != 149 {
		t.Fatalf("played %d packets ending at %d, want both streams through 149", n, s.played[n-1])
	}
}
