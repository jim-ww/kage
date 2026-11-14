package call

import (
	"slices"
	"sync"
	"time"

	"github.com/pion/rtcp"
)

// LinkFeedback is what the peer tells us about how our media arrives.
type LinkFeedback int

const (
	// LinkFeedbackNone: nothing heard yet.
	LinkFeedbackNone LinkFeedback = iota
	// LinkFeedbackReports: RTCP receiver reports (RFC 3550), which every
	// RTP stack sends: loss and round-trip time, about once a second.
	LinkFeedbackReports
	// LinkFeedbackTransportCC: transport-wide congestion control feedback
	// too, when the peer negotiated it: every packet's arrival time, from
	// which congestion control estimates the bandwidth (Bitrate).
	LinkFeedbackTransportCC
)

// LinkStats is what's known about the link to the peer, as seen from what
// they report back about our media.
type LinkStats struct {
	Feedback LinkFeedback
	// Bitrate is congestion control's estimate of what can be sent, in
	// bits/s - only with LinkFeedbackTransportCC, 0 otherwise.
	Bitrate int
	// Loss is the share of our packets lost, 0-1, over the last interval.
	Loss float64
	// RTT is the round-trip time, 0 until measured.
	RTT time.Duration
	// Congested says the link is struggling: heavy loss, or the round trip
	// grown well past its usual, as queues fill somewhere on the way.
	Congested bool
}

const (
	// linkInterval is how often reports are summed up into LinkStats.
	linkInterval = time.Second

	// congestedLoss and clearLoss bound the loss at which the link counts
	// as congested and as clear again (GCC's loss thresholds), in between
	// staying as it was.
	congestedLoss = 0.10
	clearLoss     = 0.02

	// congestedRTTRise is how far past its minimum the round trip may grow
	// before the link counts as congested.
	congestedRTTRise = 250 * time.Millisecond

	// minRTTWindow is how many recent RTT samples the minimum is taken
	// over, so it follows a route change.
	minRTTWindow = 60

	// feedbackTimeout is how long feedback of a kind may be missing before
	// it no longer counts.
	feedbackTimeout = 5 * time.Second
)

// linkMonitor sums up the RTCP a peer sends about our streams into
// LinkStats.
type linkMonitor struct {
	mu sync.Mutex

	// ours reports whether an SSRC is one of our streams.
	ours func(ssrc uint32) bool
	// bitrate is congestion control's current estimate, nil without one.
	bitrate func() int

	lastReport, lastTWCC time.Time
	windowStart          time.Time
	windowLoss           float64
	windowRTT            time.Duration
	rtts                 []time.Duration

	loss      float64
	rtt       time.Duration
	congested bool
}

func newLinkMonitor(ours func(uint32) bool, bitrate func() int) *linkMonitor {
	return &linkMonitor{ours: ours, bitrate: bitrate}
}

// onRTCP takes in RTCP packets the peer sent, arrived at now.
func (m *linkMonitor) onRTCP(pkts []rtcp.Packet, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, pkt := range pkts {
		switch p := pkt.(type) {
		case *rtcp.ReceiverReport:
			m.onReports(p.Reports, now)
		case *rtcp.SenderReport:
			m.onReports(p.Reports, now)
		case *rtcp.TransportLayerCC:
			m.lastTWCC = now
		}
	}
}

func (m *linkMonitor) onReports(reports []rtcp.ReceptionReport, now time.Time) {
	for _, r := range reports {
		if !m.ours(r.SSRC) {
			continue
		}
		if m.windowStart.IsZero() {
			m.windowStart = now
		}
		m.lastReport = now
		// The worst stream counts: audio and video share the link.
		m.windowLoss = max(m.windowLoss, float64(r.FractionLost)/256)
		if rtt, ok := reportRTT(r, now); ok {
			m.windowRTT = max(m.windowRTT, rtt)
		}
	}
	if now.Sub(m.windowStart) >= linkInterval {
		m.endWindow()
		m.windowStart = now
	}
}

// endWindow sums up the reports since the last one.
func (m *linkMonitor) endWindow() {
	m.loss = m.windowLoss
	if m.windowRTT > 0 {
		m.rtt = m.windowRTT
		m.rtts = append(m.rtts, m.windowRTT)
		if len(m.rtts) > minRTTWindow {
			m.rtts = m.rtts[1:]
		}
	}
	rttRisen := m.rtt > 0 && m.rtt > slices.Min(m.rtts)+congestedRTTRise
	switch {
	case m.loss > congestedLoss || rttRisen:
		m.congested = true
	case m.loss < clearLoss:
		m.congested = false
	}
	m.windowLoss, m.windowRTT = 0, 0
}

// stats is the link as of now.
func (m *linkMonitor) stats(now time.Time) LinkStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := LinkStats{Loss: m.loss, RTT: m.rtt, Congested: m.congested}
	switch {
	case !m.lastTWCC.IsZero() && now.Sub(m.lastTWCC) < feedbackTimeout && m.bitrate != nil:
		s.Feedback = LinkFeedbackTransportCC
		s.Bitrate = m.bitrate()
	case !m.lastReport.IsZero() && now.Sub(m.lastReport) < feedbackTimeout:
		s.Feedback = LinkFeedbackReports
	default:
		// Without word from the peer, what was last heard says nothing of
		// now.
		return LinkStats{}
	}
	return s
}

// reportRTT is the round-trip time a reception report tells, arriving at
// now (RFC 3550 §6.4.1): it echoes the time of our last sender report it
// had seen and how long the peer held it, so what's left is the trip.
func reportRTT(r rtcp.ReceptionReport, now time.Time) (time.Duration, bool) {
	if r.LastSenderReport == 0 {
		return 0, false
	}
	rtt := ntpMiddle(now) - r.LastSenderReport - r.Delay
	if int32(rtt) < 0 {
		return 0, false
	}
	return time.Duration(rtt) * time.Second / 65536, true
}

// ntpMiddle is the middle 32 bits of t as an NTP timestamp, the 16.16
// fixed-point seconds reception reports use.
func ntpMiddle(t time.Time) uint32 {
	const ntpEpochOffset = 2208988800 // 1900 to 1970, in seconds
	secs := uint64(t.Unix()) + ntpEpochOffset
	frac := uint64(t.Nanosecond()) << 32 / uint64(time.Second)
	return uint32((secs<<32 | frac) >> 16)
}
