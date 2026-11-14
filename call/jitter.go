package call

import (
	"math"
	"slices"
	"sync"
	"time"
)

// The jitter buffer's sizes are in packets, which are FrameMillis long
// unless the sender says otherwise (see JitterBuffer.frameDur).
const (
	// jitterSlots is the ring the buffer keeps packets in, indexed by
	// sequence number: it bounds how far ahead of playback a packet can
	// arrive and still be kept, so it must exceed maxTarget+hardExcess.
	jitterSlots = 64

	minTarget     = 2
	maxTarget     = 15
	initialTarget = 3

	// softExcess is how far over target the buffer may run before
	// Playout starts dropping frames to catch up (see JitterBuffer.Excess);
	// past hardExcess, Pop skips straight back to target.
	softExcess = 2
	hardExcess = 10

	// maxConceal is how many frames are made up by packet loss concealment
	// once the buffer runs dry, before playing silence: enough to hide a
	// late packet, not so many a pause turns into a drawn-out smear.
	maxConceal = 3

	// delayWindow is how many recent packets the target is sized from, and
	// delayPercentile the share of them it must have been early enough for.
	delayWindow     = 500
	delayPercentile = 0.95
	retargetEvery   = 25

	// pauseGap is the arrival gap that counts as the sender pausing (kage
	// sends nothing while muted, other clients use DTX) rather than as
	// network delay: a sender's timestamps don't count the pause, so it
	// would otherwise read as one packet arriving that much late.
	pauseGap = 250 * time.Millisecond
)

// popKind is what Pop hands out for the next frame.
type popKind int

const (
	popSilence popKind = iota // nothing to play: buffering, or the sender paused
	popPacket                 // the next packet, to decode
	popLost                   // a packet that never came, to conceal
)

// JitterBuffer reorders received RTP audio packets and hands them out one
// per frame at the playback device's pace (Pop), holding just enough of
// them to ride out the network's jitter. How much that is comes from how
// late recent packets arrived relative to the earliest one, so it grows
// while the network (or a busy CPU on either end) is jittery and shrinks
// back once it calms down. Whatever piles up beyond that - a burst after a
// stall, or the sender's sound card running slightly faster than ours -
// is dropped rather than played late, so latency never accumulates.
type JitterBuffer struct {
	mu sync.Mutex

	slots     [jitterSlots]jitterSlot
	started   bool
	buffering bool   // filling up to target before playing
	next      uint16 // sequence number Pop hands out next
	highest   uint16 // highest sequence number pushed
	target    int    // packets to hold, including the one about to play
	concealed int    // frames concealed since a packet last played

	// Delay estimation: transits are arrival time minus RTP timestamp for
	// the last delayWindow packets, against an arbitrary epoch.
	lastTS      uint32
	extTS       int64 // lastTS unwrapped
	highestExt  int64 // highest's timestamp, unwrapped
	gotPacket   bool  // highest is a packet pushed since restart
	epoch       time.Time
	lastArrival time.Time
	transits    []time.Duration
	transitIdx  int
	pushes      int
	frameDur    time.Duration
}

type jitterSlot struct {
	seq     uint16
	payload []byte
	ok      bool
}

// NewJitterBuffer returns an empty buffer.
func NewJitterBuffer() *JitterBuffer {
	return &JitterBuffer{target: initialTarget, concealed: maxConceal, frameDur: FrameMillis * time.Millisecond}
}

// Push stores a received packet, arrived at now. Packets whose turn has
// already passed are dropped.
func (b *JitterBuffer) Push(seq uint16, ts uint32, payload []byte, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.started {
		b.restart(seq, ts, now)
	}
	ext := b.extTS + int64(int32(ts-b.lastTS))
	if d := int16(seq - b.next); d < -jitterSlots || d >= jitterSlots {
		// Far outside the window: the stream started over (or we've been
		// away long enough that nothing buffered still matters).
		b.restart(seq, ts, now)
		ext = b.extTS
	} else if d < 0 {
		return // too late, played (or concealed) already
	}
	if int16(seq-b.highest) > 0 {
		if seq == b.highest+1 && b.gotPacket {
			if d := ext - b.highestExt; d > 0 && d <= SampleRate*120/1000 {
				b.frameDur = time.Duration(d) * time.Second / SampleRate
			}
		}
		b.highest, b.highestExt, b.gotPacket = seq, ext, true
		b.lastTS, b.extTS = ts, ext
	}
	b.slots[seq%jitterSlots] = jitterSlot{seq: seq, payload: payload, ok: true}

	if now.Sub(b.lastArrival) > pauseGap {
		b.transits, b.transitIdx = b.transits[:0], 0
	}
	b.lastArrival = now
	b.addTransit(now.Sub(b.epoch) - time.Duration(ext)*time.Second/SampleRate)
}

// restart empties the buffer for a stream (re)starting at seq.
func (b *JitterBuffer) restart(seq uint16, ts uint32, now time.Time) {
	b.slots = [jitterSlots]jitterSlot{}
	b.started, b.buffering = true, true
	b.next, b.highest = seq, seq-1
	b.lastTS, b.extTS, b.gotPacket = ts, int64(ts), false
	b.epoch = now
	b.transits, b.transitIdx = b.transits[:0], 0
}

func (b *JitterBuffer) addTransit(t time.Duration) {
	if len(b.transits) < delayWindow {
		b.transits = append(b.transits, t)
	} else {
		b.transits[b.transitIdx] = t
		b.transitIdx = (b.transitIdx + 1) % delayWindow
	}
	b.pushes++
	if b.pushes%retargetEvery == 0 {
		b.retarget()
	}
}

// retarget sizes target to hold delayPercentile of recent packets' delay
// past the earliest of them, plus the one about to play.
func (b *JitterBuffer) retarget() {
	if len(b.transits) < retargetEvery {
		return
	}
	sorted := slices.Clone(b.transits)
	slices.Sort(sorted)
	jitter := sorted[int(delayPercentile*float64(len(sorted)-1))] - sorted[0]
	b.target = max(minTarget, min(maxTarget, int(math.Ceil(float64(jitter)/float64(b.frameDur)))+1))
}

// depth is how many packets there are from next to highest, the missing
// ones included.
func (b *JitterBuffer) depth() int {
	return int(int16(b.highest-b.next)) + 1
}

// Pop hands out the next frame: a packet to decode, a lost one to conceal,
// or silence while there's nothing to play.
func (b *JitterBuffer) Pop() ([]byte, popKind) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.started {
		return nil, popSilence
	}
	depth := b.depth()
	if depth <= 0 {
		b.buffering = true
	}
	if b.buffering {
		if depth < b.target {
			if b.concealed < maxConceal {
				b.concealed++
				return nil, popLost
			}
			return nil, popSilence
		}
		b.buffering = false
	}
	if excess := depth - b.target; excess > hardExcess {
		for range excess {
			b.slots[b.next%jitterSlots] = jitterSlot{}
			b.next++
		}
	}

	s := b.slots[b.next%jitterSlots]
	b.slots[b.next%jitterSlots] = jitterSlot{}
	seq := b.next
	b.next++
	if !s.ok || s.seq != seq {
		return nil, popLost
	}
	b.concealed = 0
	return s.payload, popPacket
}

// Excess is how many packets past target the buffer holds - Playout drops
// frames to catch up while it's over softExcess.
func (b *JitterBuffer) Excess() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.started || b.buffering {
		return 0
	}
	return b.depth() - b.target
}

// Target is how many packets the buffer currently aims to hold.
func (b *JitterBuffer) Target() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.target
}
