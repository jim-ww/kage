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

// offerVideo makes an offer with audio and one video m-line in direction,
// the way a peer's video call session-initiate arrives.
func offerVideo(t *testing.T, direction webrtc.RTPTransceiverDirection) webrtc.SessionDescription {
	t.Helper()
	peer, err := NewPeerConnection()
	if err != nil {
		t.Fatalf("NewPeerConnection (remote side): %v", err)
	}
	t.Cleanup(func() { peer.Close() })
	if direction == webrtc.RTPTransceiverDirectionSendonly {
		if err := peer.AddVideoTrack(); err != nil {
			t.Fatalf("AddVideoTrack (remote side): %v", err)
		}
	} else if _, err := peer.pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: direction}); err != nil {
		t.Fatalf("adding remote video transceiver: %v", err)
	}
	offer, err := peer.CreateOffer()
	if err != nil {
		skipOfferErrIfNoNetwork(t, err)
		t.Fatalf("remote side creating offer: %v", err)
	}
	return offer
}

func TestAttachVideoTrackAnswersOfferedVideoSendrecv(t *testing.T) {
	offer := offerVideo(t, webrtc.RTPTransceiverDirectionSendrecv)
	p, err := NewPeerConnection()
	if err != nil {
		t.Fatalf("NewPeerConnection: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	if err := p.SetRemoteDescription(offer); err != nil {
		t.Fatalf("applying offer: %v", err)
	}
	if err := p.AttachVideoTrack(); err != nil {
		t.Fatalf("AttachVideoTrack: %v", err)
	}
	answer, err := p.CreateAnswer()
	if err != nil {
		t.Fatalf("CreateAnswer: %v", err)
	}

	if got, want := strings.Count(answer.SDP, "m=video"), 1; got != want {
		t.Errorf("answer has %d video m-lines, want %d:\n%s", got, want, answer.SDP)
	}
	video := answer.SDP[strings.Index(answer.SDP, "m=video"):]
	if !strings.Contains(video, "a=sendrecv") {
		t.Errorf("answer's video isn't sendrecv:\n%s", video)
	}
	if mid := p.VideoMid(); mid == "" || !strings.Contains(video, "a=mid:"+mid) {
		t.Errorf("VideoMid = %q, want the offered video's mid:\n%s", mid, video)
	}
}

func TestAttachVideoTrackRefusesSendonlyOffer(t *testing.T) {
	offer := offerVideo(t, webrtc.RTPTransceiverDirectionSendonly)
	p, err := NewPeerConnection()
	if err != nil {
		t.Fatalf("NewPeerConnection: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	if err := p.SetRemoteDescription(offer); err != nil {
		t.Fatalf("applying offer: %v", err)
	}
	if err := p.AttachVideoTrack(); err == nil {
		t.Fatal("AttachVideoTrack on a sendonly offer succeeded, want an error")
	}
	answer, err := p.CreateAnswer()
	if err != nil {
		t.Fatalf("CreateAnswer: %v", err)
	}
	if got := strings.Count(answer.SDP, "m=video"); got != 1 {
		t.Errorf("answer has %d video m-lines, want 1:\n%s", got, answer.SDP)
	}
	if p.VideoMid() != "" {
		t.Errorf("VideoMid = %q after a refused attach, want empty", p.VideoMid())
	}
}

// vp8OnlyVideoOffer is an offer from a peer that only has VP8, as Dino
// built without its rtp-h264 option (the default, and what distros ship).
func vp8OnlyVideoOffer(t *testing.T) webrtc.SessionDescription {
	t.Helper()
	m := &webrtc.MediaEngine{}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		PayloadType:        111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000},
		PayloadType:        98,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		t.Fatal(err)
	}
	peer, err := webrtc.NewAPI(webrtc.WithMediaEngine(m)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { peer.Close() })
	for _, kind := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeAudio, webrtc.RTPCodecTypeVideo} {
		if _, err := peer.AddTransceiverFromKind(kind); err != nil {
			t.Fatal(err)
		}
	}
	offer, err := peer.CreateOffer(nil)
	if err != nil {
		skipOfferErrIfNoNetwork(t, err)
		t.Fatal(err)
	}
	return offer
}

func TestAttachVideoTrackVP8OnlyOffer(t *testing.T) {
	p, err := NewPeerConnection()
	if err != nil {
		t.Fatalf("NewPeerConnection: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	if err := p.SetRemoteDescription(vp8OnlyVideoOffer(t)); err != nil {
		t.Fatalf("applying offer: %v", err)
	}
	if err := p.AttachVideoTrack(); err == nil {
		t.Error("AttachVideoTrack on a VP8-only offer succeeded, want an error")
	}
	if _, err := p.CreateAnswer(); err != nil {
		t.Fatalf("CreateAnswer: %v", err)
	}
}
