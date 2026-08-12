package call

import (
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

// skipIfNoNetwork skips the test when ICE gathering fails because the
// sandbox has no usable network stack to enumerate (seen in some containers:
// "failed to create network: route ip+net: netlinkrib: operation not
// supported") - an environment limitation, not something these tests are
// meant to catch.
func skipIfNoNetwork(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), "netlinkrib") {
		t.Skipf("no usable network stack in this sandbox for a loopback ICE test: %v", err)
	}
}

// connectLoopback wires two PeerConnections together entirely in-process
// (candidates exchanged via OnICECandidate/AddICECandidate callbacks, no
// signaling server, no XMPP/Jingle involved at all) and drives a normal
// offer/answer to completion. This isolates pion's own behavior from
// everything this package's callers layer on top of it - useful for
// answering "does pion itself do X" independently of our SDP<->Jingle
// translation, which is exactly what's needed to tell a pion-level
// limitation apart from a bug in our own plumbing.
func connectLoopback(t *testing.T) (a, b *PeerConnection) {
	t.Helper()

	a, err := NewPeerConnection()
	if err != nil {
		t.Fatalf("creating pc a: %v", err)
	}
	b, err = NewPeerConnection()
	if err != nil {
		a.Close()
		t.Fatalf("creating pc b: %v", err)
	}
	t.Cleanup(func() {
		a.Close()
		b.Close()
	})

	a.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		_ = b.AddICECandidate(c.ToJSON())
	})
	b.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		_ = a.AddICECandidate(c.ToJSON())
	})

	offer, err := a.CreateOffer()
	skipIfNoNetwork(t, err)
	if err != nil {
		t.Fatalf("creating offer: %v", err)
	}
	if err := b.SetRemoteDescription(offer); err != nil {
		t.Fatalf("b: setting remote offer: %v", err)
	}
	answer, err := b.CreateAnswer()
	if err != nil {
		t.Fatalf("creating answer: %v", err)
	}
	if err := a.SetRemoteDescription(answer); err != nil {
		t.Fatalf("a: setting remote answer: %v", err)
	}

	waitConnected(t, a, "a")
	waitConnected(t, b, "b")
	return a, b
}

func waitConnected(t *testing.T, pc *PeerConnection, name string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if pc.ConnectionState() == webrtc.PeerConnectionStateConnected {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s: peer connection never reached Connected (stuck at %s) - no usable network for a loopback ICE test here", name, pc.ConnectionState())
}

// fakeH264Sample is a minimal, decoder-irrelevant Annex-B "frame" (a bogus
// IDR slice NAL) - pion's payloader only cares about NAL structure enough to
// packetize it, not whether libx264 would call it valid, and nothing here
// decodes it.
var fakeH264Sample = []byte{0, 0, 0, 1, 0x65, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x11, 0x22}

// TestLoopbackVideoAddedAfterConnect reproduces kage's exact screen-share
// shape: an audio-only call connects first (as callSession.setupPeer does),
// and only afterwards is a video track added and renegotiated in (as
// startScreenShare does) - this is the one difference from a track present
// from the start, and is the scenario call/peer.go's AddVideoTrack doc
// already flags as having caused a pion crash before
// (SetFireOnTrackBeforeFirstRTP exists because of it). This test asks the
// next question: given that workaround, does RTP for a track added this way
// actually reach the other side at all?
func TestLoopbackVideoAddedAfterConnect(t *testing.T) {
	a, b := connectLoopback(t)

	var gotTrack chan *webrtc.TrackRemote = make(chan *webrtc.TrackRemote, 1)
	b.OnTrack(func(tr *webrtc.TrackRemote) {
		if tr.Kind() == webrtc.RTPCodecTypeVideo {
			select {
			case gotTrack <- tr:
			default:
			}
		}
	})

	if err := a.AddVideoTrack(); err != nil {
		t.Fatalf("adding video track: %v", err)
	}
	offer, err := a.CreateOffer()
	if err != nil {
		t.Fatalf("creating renegotiation offer: %v", err)
	}
	if err := b.SetRemoteDescription(offer); err != nil {
		t.Fatalf("b: applying renegotiation offer: %v", err)
	}
	answer, err := b.CreateAnswer()
	if err != nil {
		t.Fatalf("creating renegotiation answer: %v", err)
	}
	if err := a.SetRemoteDescription(answer); err != nil {
		t.Fatalf("a: applying renegotiation answer: %v", err)
	}

	t.Logf("a: video sender ssrc after renegotiation = %d", a.VideoSenderSSRC())
	if a.VideoSenderSSRC() == 0 {
		t.Fatalf("RTPSender.Send() was never called for the video track (ssrc still 0) - the sender never got negotiated")
	}

	var track *webrtc.TrackRemote
	select {
	case track = <-gotTrack:
	case <-time.After(5 * time.Second):
		t.Fatalf("b: never got an OnTrack callback for video - renegotiation didn't even register a receiver")
	}

	// Write samples for a few seconds and see if any make it to b as an
	// actual RTP packet - GetStats()'s OutboundRTPStreamStats turned out to
	// be unreliable for this exact scenario (an earlier version of this test
	// checked it and got 0 packets/bytes even while b read RTP fine below,
	// which is why VideoSenderSSRC - proving Send() itself ran - and a real
	// ReadRTP on the other side are the only signals trusted here).
	stop := time.Now().Add(3 * time.Second)
	for time.Now().Before(stop) {
		if err := a.WriteVideoSample(fakeH264Sample, 66*time.Millisecond); err != nil {
			t.Fatalf("writing video sample: %v", err)
		}
		time.Sleep(66 * time.Millisecond)
	}

	readDone := make(chan error, 1)
	go func() {
		_, _, err := track.ReadRTP()
		readDone <- err
	}()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatalf("b: reading RTP from video track failed: %v", err)
		}
		t.Logf("b: successfully read a video RTP packet")
	case <-time.After(3 * time.Second):
		t.Errorf("b: never received a single video RTP packet despite a writing samples for 3s - " +
			"this is pion itself failing to deliver RTP for a track added after the connection was " +
			"already established, not our Jingle/SDP translation")
	}
}

// TestLoopbackVideoFromStart is the control: a video track present from
// before the very first offer (never mid-call renegotiation). If this one
// delivers packets but TestLoopbackVideoAddedAfterConnect doesn't, the
// difference is specifically about adding the track *after* the initial
// connection, not about video/H264/pion in general.
func TestLoopbackVideoFromStart(t *testing.T) {
	a, err := NewPeerConnection()
	if err != nil {
		t.Fatalf("creating pc a: %v", err)
	}
	b, err := NewPeerConnection()
	if err != nil {
		a.Close()
		t.Fatalf("creating pc b: %v", err)
	}
	t.Cleanup(func() {
		a.Close()
		b.Close()
	})

	if err := a.AddVideoTrack(); err != nil {
		t.Fatalf("adding video track before first offer: %v", err)
	}

	a.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		_ = b.AddICECandidate(c.ToJSON())
	})
	b.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		_ = a.AddICECandidate(c.ToJSON())
	})

	var gotTrack = make(chan *webrtc.TrackRemote, 1)
	b.OnTrack(func(tr *webrtc.TrackRemote) {
		if tr.Kind() == webrtc.RTPCodecTypeVideo {
			select {
			case gotTrack <- tr:
			default:
			}
		}
	})

	offer, err := a.CreateOffer()
	skipIfNoNetwork(t, err)
	if err != nil {
		t.Fatalf("creating offer: %v", err)
	}
	if err := b.SetRemoteDescription(offer); err != nil {
		t.Fatalf("b: setting remote offer: %v", err)
	}
	answer, err := b.CreateAnswer()
	if err != nil {
		t.Fatalf("creating answer: %v", err)
	}
	if err := a.SetRemoteDescription(answer); err != nil {
		t.Fatalf("a: setting remote answer: %v", err)
	}

	waitConnected(t, a, "a")
	waitConnected(t, b, "b")

	var track *webrtc.TrackRemote
	select {
	case track = <-gotTrack:
	case <-time.After(5 * time.Second):
		t.Fatalf("b: never got an OnTrack callback for video")
	}

	stop := time.Now().Add(3 * time.Second)
	for time.Now().Before(stop) {
		if err := a.WriteVideoSample(fakeH264Sample, 66*time.Millisecond); err != nil {
			t.Fatalf("writing video sample: %v", err)
		}
		time.Sleep(66 * time.Millisecond)
	}

	readDone := make(chan error, 1)
	go func() {
		_, _, err := track.ReadRTP()
		readDone <- err
	}()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatalf("b: reading RTP from video track failed: %v", err)
		}
		t.Logf("b: successfully read a video RTP packet")
	case <-time.After(3 * time.Second):
		t.Errorf("b: never received a single video RTP packet despite a writing samples for 3s")
	}
}
