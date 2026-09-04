package call

import (
	"strings"
	"testing"

	"github.com/pion/webrtc/v4"
)

// skipOfferErrIfNoNetwork skips when CreateOffer's ICE gathering fails
// because the sandbox has no usable network stack to enumerate (seen in some
// containers: "failed to create network: route ip+net: netlinkrib:
// operation not supported") - an environment limitation, not something this
// test is meant to catch. Same check as loopback_test.go's
// skipIfNoNetwork, renamed to coexist with it: that one is integration-
// tagged and this file isn't, so both compile into the same package under
// `go test -tags integration`.
func skipOfferErrIfNoNetwork(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), "netlinkrib") {
		t.Skipf("no usable network stack in this sandbox for an offer/answer test: %v", err)
	}
}

// TestVideoMidIgnoresPreExistingVideoTransceiver reproduces a live bug: a
// call that started as audio+video (the peer offering their own camera from
// session-initiate) already has a video transceiver - recvonly, no local
// track - by the time AddVideoTrack adds our own outbound one for content-
// add. VideoMid must identify our transceiver specifically (the one whose
// Sender carries our local track), not just "a" video transceiver, or the
// content-add re-announces the peer's own already-negotiated content name
// and gets the session torn down (observed live against Conversations:
// "contents with names 0 already exists").
func TestVideoMidIgnoresPreExistingVideoTransceiver(t *testing.T) {
	p, err := NewPeerConnection()
	if err != nil {
		t.Fatalf("NewPeerConnection: %v", err)
	}
	t.Cleanup(func() { p.Close() })

	peer, err := NewPeerConnection()
	if err != nil {
		t.Fatalf("NewPeerConnection (remote side): %v", err)
	}
	t.Cleanup(func() { peer.Close() })

	// Simulate the peer's own inbound video already being part of the call
	// (a recvonly transceiver with no local track), then negotiate it so it
	// gets a real mid - same as what acceptSession leaves in place for an
	// audio+video session-initiate.
	if _, err := p.pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{
		Direction: webrtc.RTPTransceiverDirectionRecvonly,
	}); err != nil {
		t.Fatalf("adding pre-existing recvonly video transceiver: %v", err)
	}
	offer, err := p.CreateOffer()
	if err != nil {
		skipOfferErrIfNoNetwork(t, err)
		t.Fatalf("negotiating pre-existing transceivers: %v", err)
	}
	// A real content-add offer only follows a completed offer/answer cycle
	// (the session-initiate/-accept that established the call), which
	// leaves p in have-remote-answer/stable - not have-local-offer.
	// CreateOffer again without completing this cycle first hits pion's
	// signaling-state guard ("have-local-offer->SetLocal(offer)->
	// have-local-offer"), so drive one here with a second PeerConnection
	// before the content-add CreateOffer below.
	if err := peer.SetRemoteDescription(offer); err != nil {
		t.Fatalf("remote side applying offer: %v", err)
	}
	answer, err := peer.CreateAnswer()
	if err != nil {
		t.Fatalf("remote side creating answer: %v", err)
	}
	if err := p.SetRemoteDescription(answer); err != nil {
		t.Fatalf("applying answer: %v", err)
	}

	if mid := p.VideoMid(); mid != "" {
		t.Fatalf("VideoMid before AddVideoTrack = %q, want empty", mid)
	}

	if err := p.AddVideoTrack(); err != nil {
		t.Fatalf("AddVideoTrack: %v", err)
	}
	if _, err := p.CreateOffer(); err != nil {
		t.Fatalf("negotiating content-add offer: %v", err)
	}

	ourMid := p.VideoMid()
	if ourMid == "" {
		t.Fatal("VideoMid after AddVideoTrack = empty, want our transceiver's mid")
	}

	for _, tr := range p.pc.GetTransceivers() {
		if tr.Kind() != webrtc.RTPCodecTypeVideo {
			continue
		}
		s := tr.Sender()
		hasOurTrack := s != nil && s.Track() != nil && s.Track().ID() == p.videoTrack.ID()
		if hasOurTrack && tr.Mid() != ourMid {
			t.Errorf("our video transceiver has mid %q, VideoMid returned %q", tr.Mid(), ourMid)
		}
		if !hasOurTrack && tr.Mid() == ourMid {
			t.Errorf("VideoMid %q collides with the pre-existing recvonly video transceiver's mid", ourMid)
		}
	}
}
