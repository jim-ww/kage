package call

import (
	"testing"
	"time"
)

// adaptSim feeds a QualityAdapter a second's LinkStats at a time.
type adaptSim struct {
	a   *QualityAdapter
	now time.Time
}

func newAdaptSim(ceiling VideoQuality) *adaptSim {
	return &adaptSim{a: NewQualityAdapter(ceiling), now: time.Unix(1000, 0)}
}

// run feeds s for d, a second at a time, returning when quality changed.
func (sim *adaptSim) run(s LinkStats, d time.Duration) (changes []time.Duration) {
	start := sim.now
	for elapsed := time.Duration(0); elapsed < d; elapsed += time.Second {
		sim.now = sim.now.Add(time.Second)
		if sim.a.Update(s, sim.now) {
			changes = append(changes, sim.now.Sub(start))
		}
	}
	return changes
}

var (
	clean     = LinkStats{Feedback: LinkFeedbackReports}
	congested = LinkStats{Feedback: LinkFeedbackReports, Congested: true, Loss: 0.2}
)

// TestQualityAdapterReportsOnly checks a peer that only sends receiver
// reports gets quality stepped down one step at a time while the link
// struggles, not on a single bad second, and never below the lowest.
func TestQualityAdapterReportsOnly(t *testing.T) {
	sim := newAdaptSim(VideoQualityHigh)
	sim.run(congested, time.Second)
	if sim.run(clean, time.Second); sim.a.Current() != VideoQualityHigh {
		t.Fatalf("quality %v after one bad second, want it kept", sim.a.Current())
	}
	changes := sim.run(congested, 30*time.Second)
	if sim.a.Current() != VideoQualityVeryLow || len(changes) != 3 {
		t.Fatalf("quality %v after %d steps, want very low in 3", sim.a.Current(), len(changes))
	}
	for i := 1; i < len(changes); i++ {
		if gap := changes[i] - changes[i-1]; gap < stepGap {
			t.Errorf("steps %v apart, want at least %v", gap, stepGap)
		}
	}
}

// TestQualityAdapterProbesUp checks quality goes back up a step only after
// the link's been clean a while, and waits longer before the next try
// each time a step up made it struggle again.
func TestQualityAdapterProbesUp(t *testing.T) {
	sim := newAdaptSim(VideoQualityMedium)
	sim.run(congested, 4*time.Second)
	if sim.a.Current() != VideoQualityLow {
		t.Fatalf("quality %v, want low", sim.a.Current())
	}
	if changes := sim.run(clean, probeWait+time.Second); len(changes) != 1 || changes[0] < probeWait || sim.a.Current() != VideoQualityMedium {
		t.Fatalf("stepped up at %v to %v, want medium after %v clean", changes, sim.a.Current(), probeWait)
	}

	// Medium is too much: the step up fails, and the next try waits twice
	// as long.
	sim.run(congested, 4*time.Second)
	if sim.a.Current() != VideoQualityLow {
		t.Fatalf("quality %v after the step up failed, want back to low", sim.a.Current())
	}
	if changes := sim.run(clean, 2*probeWait+time.Second); len(changes) != 1 || changes[0] < 2*probeWait {
		t.Fatalf("stepped up at %v, want only after %v clean", changes, 2*probeWait)
	}
}

// TestQualityAdapterFollowsEstimate checks that with transport-wide
// feedback, an estimate below what's sent takes quality straight down to
// what fits it.
func TestQualityAdapterFollowsEstimate(t *testing.T) {
	sim := newAdaptSim(VideoQualityHigh)
	s := LinkStats{Feedback: LinkFeedbackTransportCC, Bitrate: 1_200_000, Sent: 4_000_000}
	if changes := sim.run(s, 5*time.Second); len(changes) != 1 || sim.a.Current() != VideoQualityLow {
		t.Fatalf("quality %v after %d steps, want low (800k fits 70%% of 1.2M) in one", sim.a.Current(), len(changes))
	}
}

// TestQualityAdapterSendingLittle checks a low estimate while sending as
// little (a still camera picture compresses to next to nothing, and the
// estimate only measures up to what's sent) isn't taken for a struggling
// link.
func TestQualityAdapterSendingLittle(t *testing.T) {
	sim := newAdaptSim(VideoQualityHigh)
	s := LinkStats{Feedback: LinkFeedbackTransportCC, Bitrate: 300_000, Sent: 200_000}
	if changes := sim.run(s, time.Minute); len(changes) != 0 {
		t.Fatalf("quality changed at %v to %v, want high kept", changes, sim.a.Current())
	}
}

// TestQualityAdapterNoFeedback checks a peer that sends no RTCP at all
// leaves quality as picked.
func TestQualityAdapterNoFeedback(t *testing.T) {
	sim := newAdaptSim(VideoQualityMedium)
	if changes := sim.run(LinkStats{}, time.Minute); len(changes) != 0 {
		t.Fatalf("quality changed at %v with no feedback", changes)
	}
}

// TestQualityAdapterCeiling checks quality never goes above the one
// picked, and follows it down at once.
func TestQualityAdapterCeiling(t *testing.T) {
	sim := newAdaptSim(VideoQualityLow)
	if changes := sim.run(clean, 10*time.Minute); len(changes) != 0 {
		t.Fatalf("quality went up to %v at %v, past the one picked", sim.a.Current(), changes)
	}
	sim.a.SetCeiling(VideoQualityVeryLow)
	if sim.a.Current() != VideoQualityVeryLow {
		t.Fatalf("quality %v under a very low ceiling", sim.a.Current())
	}
}
