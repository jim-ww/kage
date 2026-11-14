package main

import (
	"testing"
	"time"

	"github.com/jim-ww/kage/call"
)

// TestLinkQuality checks the quality the call bar shows: nothing until the
// peer's feedback says anything, poor while the link is congested, else by
// loss and round trip.
func TestLinkQuality(t *testing.T) {
	for _, tc := range []struct {
		s    call.LinkStats
		want string
	}{
		{call.LinkStats{}, ""},
		{call.LinkStats{Feedback: call.LinkFeedbackReports, RTT: 40 * time.Millisecond}, "good"},
		{call.LinkStats{Feedback: call.LinkFeedbackReports, Loss: 0.05, RTT: 40 * time.Millisecond}, "fair"},
		{call.LinkStats{Feedback: call.LinkFeedbackReports, RTT: 300 * time.Millisecond}, "fair"},
		{call.LinkStats{Feedback: call.LinkFeedbackTransportCC, Loss: 0.15}, "poor"},
		{call.LinkStats{Feedback: call.LinkFeedbackReports, Congested: true}, "poor"},
	} {
		if got := linkQuality(tc.s); got != tc.want {
			t.Errorf("linkQuality(%+v) = %q, want %q", tc.s, got, tc.want)
		}
	}
}
