package call

import (
	"fmt"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// PeerConnection wraps a pion/webrtc.PeerConnection configured for a single
// audio-only Jingle call: one Opus track, no video, no data channel.
//
// This type stays Jingle-agnostic: it speaks SDP and pion types only. The
// SDP<->Jingle translation and the Mic/Opus/Speaker pumping live in package
// main (jingle_sdp.go, callsession.go), which is the only place allowed to
// know about both this package and xmpp.
type PeerConnection struct {
	pc         *webrtc.PeerConnection
	track      *webrtc.TrackLocalStaticSample
	videoTrack *webrtc.TrackLocalStaticSample
}

// NewPeerConnection creates a PeerConnection with a single outbound Opus
// audio track already added, using Google's public STUN server for NAT
// traversal (fine for this slice; a production deployment would want this
// configurable, and likely a TURN fallback). A video track for screen
// sharing is added later, on demand, via AddVideoTrack - a permanently
// negotiated-but-idle video m-line was tried first and found to reliably
// crash pion's receiver goroutine (see git history), so video is only ever
// added right before it's actually about to carry real samples.
func NewPeerConnection() (*PeerConnection, error) {
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}},
	}

	// pion's default receiver setup spawns a background goroutine that peeks
	// the first RTP packet to auto-detect its codec (PeerConnection.
	// startReceiver, called from startReceiver.func1) - for a track added by
	// a mid-call renegotiation (our screen-share content-add) this races
	// with the receiver's own interceptor setup closely enough to panic on a
	// nil rtpInterceptor (observed live, reproducibly, on every content-add).
	// SetFireOnTrackBeforeFirstRTP skips that goroutine entirely and fires
	// OnTrack immediately instead - codec detection then happens the normal
	// way, from the SDP, which every content-add already carries.
	settingEngine := webrtc.SettingEngine{}
	settingEngine.SetFireOnTrackBeforeFirstRTP(true)
	api := webrtc.NewAPI(webrtc.WithSettingEngine(settingEngine))

	pc, err := api.NewPeerConnection(config)
	if err != nil {
		return nil, fmt.Errorf("creating peer connection: %w", err)
	}

	track, err := webrtc.NewTrackLocalStaticSample(
		// RFC 7587 §7 requires Opus to always be signaled as channels=2 in
		// SDP, regardless of how many channels the encoded audio actually
		// carries - this is unrelated to call.Channels (1, our real mono
		// capture/encode pipeline). Advertising 1 here doesn't match pion's
		// own default-registered Opus codec, so any answer that echoes it
		// back fails SetRemoteDescription with "codec is not supported by
		// remote" once the local track can't be bound to it.
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: SampleRate, Channels: 2},
		"audio", "kage",
	)
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("creating local audio track: %w", err)
	}
	if _, err := pc.AddTrack(track); err != nil {
		pc.Close()
		return nil, fmt.Errorf("adding local audio track: %w", err)
	}

	return &PeerConnection{pc: pc, track: track}, nil
}

// AddVideoTrack adds the outbound H.264 video track (for screen sharing) to
// an already-established connection. The caller must then renegotiate (see
// CreateOffer/AddVideoTrack's caller in callsession.go's startScreenShare,
// which sends the resulting offer as a XEP-0166 content-add) before writing
// any samples to it.
func (p *PeerConnection) AddVideoTrack() error {
	if p.videoTrack != nil {
		return nil
	}
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeH264,
			ClockRate:   90000,
			SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
		},
		"video", "kage",
	)
	if err != nil {
		return fmt.Errorf("creating local video track: %w", err)
	}
	if _, err := p.pc.AddTrack(track); err != nil {
		return fmt.Errorf("adding local video track: %w", err)
	}
	p.videoTrack = track
	return nil
}

// CreateOffer generates a local SDP offer (as the Jingle initiator) and sets
// it as our local description, returning it for translation into a
// XEP-0166 session-initiate.
func (p *PeerConnection) CreateOffer() (webrtc.SessionDescription, error) {
	offer, err := p.pc.CreateOffer(nil)
	if err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("creating offer: %w", err)
	}
	if err := p.pc.SetLocalDescription(offer); err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("setting local description: %w", err)
	}
	return offer, nil
}

// CreateAnswer generates a local SDP answer (as the Jingle responder) after
// a remote offer has been applied via SetRemoteDescription, and sets it as
// our local description.
func (p *PeerConnection) CreateAnswer() (webrtc.SessionDescription, error) {
	answer, err := p.pc.CreateAnswer(nil)
	if err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("creating answer: %w", err)
	}
	if err := p.pc.SetLocalDescription(answer); err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("setting local description: %w", err)
	}
	return answer, nil
}

// SetRemoteDescription applies the peer's SDP (translated from an incoming
// Jingle session-initiate/-accept/transport-replace) to this connection.
// When applied by the non-offering side and the remote ICE credentials
// changed from the currently set ones, pion detects this as an implicit ICE
// restart (see pion/webrtc's PeerConnection.SetRemoteDescription) and
// regenerates its own local ICE credentials too - no separate call needed on
// the responder side of a transport-replace/-accept.
func (p *PeerConnection) SetRemoteDescription(desc webrtc.SessionDescription) error {
	if err := p.pc.SetRemoteDescription(desc); err != nil {
		return fmt.Errorf("setting remote description: %w", err)
	}
	return nil
}

// RestartICE generates a fresh SDP offer with new ICE credentials (pion's
// OfferOptions.ICERestart) and sets it as our local description - the
// Jingle initiator's first step of a XEP-0166/0176 §6.1 transport restart.
// Only meaningful once a first offer/answer has already completed.
func (p *PeerConnection) RestartICE() (webrtc.SessionDescription, error) {
	offer, err := p.pc.CreateOffer(&webrtc.OfferOptions{ICERestart: true})
	if err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("creating ice restart offer: %w", err)
	}
	if err := p.pc.SetLocalDescription(offer); err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("setting local description: %w", err)
	}
	return offer, nil
}

// ConnectionState returns the current ICE/DTLS connection state, for a
// deferred check (e.g. after a "disconnected" grace period) of whether the
// connection actually needs recovering or already un-disconnected on its
// own.
func (p *PeerConnection) ConnectionState() webrtc.PeerConnectionState {
	return p.pc.ConnectionState()
}

// OnICECandidate registers f to be called with each locally-gathered ICE
// candidate as it becomes available (trickle ICE) - f gets a nil candidate
// once gathering completes, per pion/webrtc's convention.
func (p *PeerConnection) OnICECandidate(f func(*webrtc.ICECandidate)) {
	p.pc.OnICECandidate(f)
}

// AddICECandidate applies a remote ICE candidate (translated from an
// incoming Jingle transport-info) to this connection.
func (p *PeerConnection) AddICECandidate(candidate webrtc.ICECandidateInit) error {
	if err := p.pc.AddICECandidate(candidate); err != nil {
		return fmt.Errorf("adding ice candidate: %w", err)
	}
	return nil
}

// WriteSample pushes one Opus-encoded packet onto the outbound audio track.
// The caller reads PCM frames off a Mic, runs them through an Encoder, and
// passes the resulting packet here on a steady FrameMillis cadence.
func (p *PeerConnection) WriteSample(data []byte, duration time.Duration) error {
	return p.track.WriteSample(media.Sample{Data: data, Duration: duration})
}

// WriteVideoSample pushes one H.264 NAL unit onto the outbound video track.
// Returns an error if AddVideoTrack hasn't been called yet.
func (p *PeerConnection) WriteVideoSample(data []byte, duration time.Duration) error {
	if p.videoTrack == nil {
		return fmt.Errorf("no video track added yet")
	}
	return p.videoTrack.WriteSample(media.Sample{Data: data, Duration: duration})
}

// OnTrack registers f to be called when one of the peer's tracks (audio or
// video) starts arriving. The caller checks TrackRemote.Kind() to tell them
// apart - audio payloads go to a Decoder and then a Speaker, video payloads
// get depacketized and piped into a ScreenViewer (mpv).
func (p *PeerConnection) OnTrack(f func(*webrtc.TrackRemote)) {
	p.pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) { f(track) })
}

// OnConnectionStateChange registers f to be called on every peer connection
// state transition - the signal callers use to start pumping audio (on
// Connected) and to tear the call down (on Failed/Closed/Disconnected).
func (p *PeerConnection) OnConnectionStateChange(f func(webrtc.PeerConnectionState)) {
	p.pc.OnConnectionStateChange(f)
}

// LocalDescription returns the local SDP currently applied, or nil before
// CreateOffer/CreateAnswer has run.
func (p *PeerConnection) LocalDescription() *webrtc.SessionDescription {
	return p.pc.LocalDescription()
}

// Close tears down the connection.
func (p *PeerConnection) Close() error {
	return p.pc.Close()
}

// Stats samples pion's GetStats() for a rough call-quality snapshot:
// packet-loss percentage (from the remote endpoint's own report of what it
// received from us, RemoteInboundRTPStreamStats) and round-trip time in
// milliseconds (from the currently nominated ICE candidate pair). Either
// value is 0 if that stat hasn't been produced yet (e.g. straight after
// connecting) - callers should treat an all-zero result as "no data yet"
// rather than "perfect connection".
func (p *PeerConnection) Stats() (lossPct, rttMs float64) {
	report := p.pc.GetStats()
	var lost int32
	var received uint32
	for _, s := range report {
		switch st := s.(type) {
		case webrtc.RemoteInboundRTPStreamStats:
			if st.Kind == "audio" && st.PacketsLost > 0 {
				lost += st.PacketsLost
			}
		case webrtc.InboundRTPStreamStats:
			if st.Kind == "audio" {
				received += st.PacketsReceived
			}
		case webrtc.ICECandidatePairStats:
			if st.Nominated && st.State == webrtc.StatsICECandidatePairStateSucceeded {
				rttMs = st.CurrentRoundTripTime * 1000
			}
		}
	}
	if total := received + uint32(lost); total > 0 {
		lossPct = float64(lost) / float64(total) * 100
	}
	return lossPct, rttMs
}
