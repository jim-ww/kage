package call

import "time"

// videoQualities are the VideoQualities from lowest to highest.
var videoQualities = []VideoQuality{VideoQualityVeryLow, VideoQualityLow, VideoQualityMedium, VideoQualityHigh}

func qualityRank(q VideoQuality) int {
	for i, v := range videoQualities {
		if v == q {
			return i
		}
	}
	return 0
}

const (
	// badFor is how long the link must struggle before quality steps down,
	// so one late report doesn't cost it.
	badFor = 2 * time.Second
	// stepGap is the least time between two steps, letting the link show
	// what the last one did.
	stepGap = 3 * time.Second

	// probeWait is how long the link must be clean before trying a step
	// up, doubled up to maxProbeWait each time a step up made it struggle
	// (probeFailedWithin), so a link at its limit isn't pushed past it
	// over and over. Steady for probeResetAfter, it starts over.
	probeWait         = 20 * time.Second
	maxProbeWait      = 5 * time.Minute
	probeFailedWithin = 15 * time.Second
	probeResetAfter   = 5 * time.Minute

	// estimateHeadroom is the share of congestion control's estimate video
	// may take, leaving the rest for audio and the estimate's error.
	estimateHeadroom = 0.7
)

// QualityAdapter steps sent video's quality to what the link carries, from
// the peer's feedback (LinkStats), never above the quality picked
// (SetCeiling): down soon after the link starts struggling, back up only
// after it's been clean for a while. With transport-wide feedback, the
// estimate falling below what we send is struggling too, and decides how
// far down; without it, all a peer's receiver reports can tell is
// struggling or not, and quality goes down a step at a time. A step up has
// to be tried to find out whether the link takes it: the estimate can't
// tell, only ever measuring up to what's being sent.
type QualityAdapter struct {
	ceiling, current VideoQuality

	badSince, cleanSince time.Time
	lastChange, lastUp   time.Time
	wait                 time.Duration
}

// NewQualityAdapter starts at ceiling, the quality picked.
func NewQualityAdapter(ceiling VideoQuality) *QualityAdapter {
	return &QualityAdapter{ceiling: ceiling, current: ceiling, wait: probeWait}
}

// Current is the quality to send at.
func (a *QualityAdapter) Current() VideoQuality { return a.current }

// SetCeiling changes the quality picked: never sent above, and stepped up
// to as the link allows.
func (a *QualityAdapter) SetCeiling(q VideoQuality) {
	a.ceiling = q
	if qualityRank(a.current) > qualityRank(q) {
		a.current = q
	}
}

// Update takes in the link as of now, reporting whether Current changed.
func (a *QualityAdapter) Update(s LinkStats, now time.Time) bool {
	if s.Feedback == LinkFeedbackNone {
		// Nothing to go by: hold, and start over once there is.
		a.badSince, a.cleanSince = time.Time{}, time.Time{}
		return false
	}
	target := qualityRank(a.current)
	fits := len(videoQualities) - 1
	bad := s.Congested
	if s.Feedback == LinkFeedbackTransportCC && s.Bitrate > 0 && s.Bitrate < s.Sent {
		bad = true
		fits = 0
		for i, q := range videoQualities {
			if float64(q.profile().bitrateKbps*1000) <= estimateHeadroom*float64(s.Bitrate) {
				fits = i
			}
		}
	}

	if bad {
		a.cleanSince = time.Time{}
		if a.badSince.IsZero() {
			a.badSince = now
		}
		if now.Sub(a.badSince) < badFor || now.Sub(a.lastChange) < stepGap || target == 0 {
			return false
		}
		if !a.lastUp.IsZero() && now.Sub(a.lastUp) < probeFailedWithin {
			a.wait = min(2*a.wait, maxProbeWait)
		}
		a.lastUp = time.Time{}
		a.step(min(fits, target-1), now)
		return true
	}

	a.badSince = time.Time{}
	if a.cleanSince.IsZero() {
		a.cleanSince = now
	}
	if now.Sub(a.cleanSince) >= probeResetAfter {
		a.wait = probeWait
	}
	if target >= qualityRank(a.ceiling) || now.Sub(a.cleanSince) < a.wait || now.Sub(a.lastChange) < a.wait {
		return false
	}
	a.lastUp = now
	a.step(target+1, now)
	return true
}

func (a *QualityAdapter) step(rank int, now time.Time) {
	a.current = videoQualities[rank]
	a.lastChange = now
	a.badSince, a.cleanSince = time.Time{}, time.Time{}
}
