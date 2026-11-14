package call

import (
	"testing"
	"time"

	"github.com/pion/rtcp"
)

const ourSSRC = 1234

// linkSim plays a peer sending receiver reports about ourSSRC once a
// second.
type linkSim struct {
	m   *linkMonitor
	now time.Time
}

func newLinkSim(bitrate func() int) *linkSim {
	return &linkSim{m: newLinkMonitor(func(ssrc uint32) bool { return ssrc == ourSSRC }, bitrate), now: time.Unix(1000, 0)}
}

// report sends a second's report: loss of our packets, and rtt as the
// round trip it echoes back.
func (s *linkSim) report(loss float64, rtt time.Duration) {
	s.now = s.now.Add(time.Second)
	sent := s.now.Add(-rtt)
	s.m.onRTCP([]rtcp.Packet{&rtcp.ReceiverReport{Reports: []rtcp.ReceptionReport{{
		SSRC:             ourSSRC,
		FractionLost:     uint8(loss * 256),
		LastSenderReport: ntpMiddle(sent),
	}}}}, s.now)
}

func (s *linkSim) stats() LinkStats { return s.m.stats(s.now) }

// TestLinkMonitorNothingHeard checks a peer that sends no RTCP leaves the
// link unknown rather than reported fine or congested.
func TestLinkMonitorNothingHeard(t *testing.T) {
	s := newLinkSim(nil)
	if got := s.stats(); got != (LinkStats{}) {
		t.Fatalf("stats %+v before any report, want none", got)
	}
	s.report(0, 50*time.Millisecond)
	s.report(0, 50*time.Millisecond)
	s.now = s.now.Add(feedbackTimeout + time.Second)
	if got := s.stats(); got != (LinkStats{}) {
		t.Fatalf("stats %+v after reports stopped, want none", got)
	}
}

// TestLinkMonitorReports checks loss and round trip are read from receiver
// reports, the feedback every peer sends.
func TestLinkMonitorReports(t *testing.T) {
	s := newLinkSim(nil)
	for range 3 {
		s.report(0.05, 80*time.Millisecond)
	}
	got := s.stats()
	if got.Feedback != LinkFeedbackReports || got.Bitrate != 0 {
		t.Errorf("feedback %v bitrate %d, want reports and no estimate", got.Feedback, got.Bitrate)
	}
	if got.Loss < 0.04 || got.Loss > 0.06 {
		t.Errorf("loss %v, want 0.05", got.Loss)
	}
	if got.RTT < 79*time.Millisecond || got.RTT > 81*time.Millisecond {
		t.Errorf("rtt %v, want 80ms", got.RTT)
	}
}

// TestLinkMonitorIgnoresOtherStreams checks reports about SSRCs that
// aren't ours (a peer reporting on what it received from someone else, or
// its own sending) don't count.
func TestLinkMonitorIgnoresOtherStreams(t *testing.T) {
	s := newLinkSim(nil)
	for range 3 {
		s.now = s.now.Add(time.Second)
		s.m.onRTCP([]rtcp.Packet{&rtcp.ReceiverReport{Reports: []rtcp.ReceptionReport{{SSRC: 99, FractionLost: 200}}}}, s.now)
	}
	if got := s.stats(); got != (LinkStats{}) {
		t.Fatalf("stats %+v from reports on another stream, want none", got)
	}
}

// TestLinkMonitorCongestion checks heavy loss, or a round trip grown far
// past its usual, counts as congested, and the link clears again only once
// loss is low and the round trip back down.
func TestLinkMonitorCongestion(t *testing.T) {
	s := newLinkSim(nil)
	for range 5 {
		s.report(0, 60*time.Millisecond)
	}
	if s.stats().Congested {
		t.Fatal("congested on a clean link")
	}
	s.report(0.2, 60*time.Millisecond)
	s.report(0.2, 60*time.Millisecond)
	if !s.stats().Congested {
		t.Fatal("not congested at 20% loss")
	}
	s.report(0.05, 60*time.Millisecond)
	s.report(0.05, 60*time.Millisecond)
	if !s.stats().Congested {
		t.Error("cleared at 5% loss, want it to stay congested until loss is low")
	}
	s.report(0, 60*time.Millisecond)
	s.report(0, 60*time.Millisecond)
	if s.stats().Congested {
		t.Fatal("still congested once clean")
	}
	// Queues filling: no loss yet, but the round trip grows.
	s.report(0, 400*time.Millisecond)
	s.report(0, 400*time.Millisecond)
	if !s.stats().Congested {
		t.Error("not congested with the round trip up 340ms")
	}
}

// TestLinkMonitorTransportCC checks congestion control's estimate is only
// used while transport-wide feedback actually arrives - a peer that
// negotiated it and then sends none leaves just the reports.
func TestLinkMonitorTransportCC(t *testing.T) {
	s := newLinkSim(func() int { return 750_000 })
	s.report(0, 50*time.Millisecond)
	s.report(0, 50*time.Millisecond)
	if got := s.stats(); got.Feedback != LinkFeedbackReports || got.Bitrate != 0 {
		t.Fatalf("stats %+v with no transport-wide feedback, want reports only", got)
	}
	s.m.onRTCP([]rtcp.Packet{&rtcp.TransportLayerCC{}}, s.now)
	if got := s.stats(); got.Feedback != LinkFeedbackTransportCC || got.Bitrate != 750_000 {
		t.Fatalf("stats %+v with transport-wide feedback, want its estimate", got)
	}
	s.report(0, 50*time.Millisecond)
	s.now = s.now.Add(feedbackTimeout)
	s.report(0, 50*time.Millisecond)
	if got := s.stats(); got.Feedback != LinkFeedbackReports || got.Bitrate != 0 {
		t.Fatalf("stats %+v after transport-wide feedback stopped, want reports only", got)
	}
}
