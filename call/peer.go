package call

import (
	"fmt"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// PeerConnection is a pion PeerConnection for a call: Opus audio, video
// added when sent. It knows SDP, not Jingle (see jingle_sdp.go).
type PeerConnection struct {
	pc         *webrtc.PeerConnection
	track      *webrtc.TrackLocalStaticSample
	videoTrack *webrtc.TrackLocalStaticSample
}

// NewPeerConnection creates a PeerConnection with an Opus track, using
// Google's public STUN server. Video is added by AddVideoTrack right before
// it's sent: an idle video m-line crashes pion's receiver.
func NewPeerConnection() (*PeerConnection, error) {
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}},
	}

	// pion's goroutine peeking a renegotiated track's first packet for its
	// codec races its interceptor setup and panics. This skips it, firing
	// OnTrack at once; the codec comes from the SDP.
	settingEngine := webrtc.SettingEngine{}
	settingEngine.SetFireOnTrackBeforeFirstRTP(true)
	api := webrtc.NewAPI(webrtc.WithSettingEngine(settingEngine))

	pc, err := api.NewPeerConnection(config)
	if err != nil {
		return nil, fmt.Errorf("creating peer connection: %w", err)
	}

	track, err := webrtc.NewTrackLocalStaticSample(
		// Opus is always channels=2 in SDP (RFC 7587 §7), though we send
		// mono; 1 wouldn't match pion's codec.
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

// AddVideoTrack adds our H.264 video track, to renegotiate before writing
// to it.
func (p *PeerConnection) AddVideoTrack() error {
	if p.videoTrack != nil {
		return nil
	}
	track, err := newVideoTrack()
	if err != nil {
		return err
	}
	// Sendonly: AddTrack's sendrecv would negotiate video back to us too.
	if _, err := p.pc.AddTransceiverFromTrack(track, webrtc.RTPTransceiverInit{
		Direction: webrtc.RTPTransceiverDirectionSendonly,
	}); err != nil {
		return fmt.Errorf("adding local video track: %w", err)
	}
	p.videoTrack = track
	return nil
}

// AttachVideoTrack puts our video track on the video transceiver of the
// remote offer just applied, so the answer accepts that content sendrecv
// instead of us needing a content of our own. Needs an offer that lets us
// send on it (sendrecv or recvonly).
func (p *PeerConnection) AttachVideoTrack() error {
	if p.videoTrack != nil {
		return nil
	}
	track, err := newVideoTrack()
	if err != nil {
		return err
	}
	sender, err := p.pc.AddTrack(track)
	if err != nil {
		return fmt.Errorf("adding local video track: %w", err)
	}
	for _, t := range p.pc.GetTransceivers() {
		if t.Sender() == sender && t.Mid() != "" {
			p.videoTrack = track
			return nil
		}
	}
	// A transceiver of its own, which an answer can't carry.
	_ = p.pc.RemoveTrack(sender)
	return fmt.Errorf("remote offer has no video we can send on")
}

func newVideoTrack() (*webrtc.TrackLocalStaticSample, error) {
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeH264,
			ClockRate: 90000,
			// Level 5.1 (0x33), up to about 4K at 15fps: it must cover the
			// screen's real size, as a hardware decoder sizes its buffers by
			// it and decodes nothing bigger.
			SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e033",
		},
		"video", "kage",
	)
	if err != nil {
		return nil, fmt.Errorf("creating local video track: %w", err)
	}
	return track, nil
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

// SetRemoteDescription applies the peer's SDP. New ICE credentials in an
// offer restart ICE, renewing ours too.
func (p *PeerConnection) SetRemoteDescription(desc webrtc.SessionDescription) error {
	if err := p.pc.SetRemoteDescription(desc); err != nil {
		return fmt.Errorf("setting remote description: %w", err)
	}
	return nil
}

// RestartICE sets an offer with new ICE credentials as our description, to
// send as a transport-replace.
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

// ConnectionState returns the connection's state.
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

// WriteVideoSample sends one H.264 frame (Annex-B) as one sample, lasting
// duration.
func (p *PeerConnection) WriteVideoSample(data []byte, duration time.Duration) error {
	if p.videoTrack == nil {
		return fmt.Errorf("no video track added yet")
	}
	return p.videoTrack.WriteSample(media.Sample{Data: data, Duration: duration})
}

// OnTrack registers f for each of the peer's tracks, audio or video.
func (p *PeerConnection) OnTrack(f func(*webrtc.TrackRemote)) {
	p.pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) { f(track) })
}

// SendPLI asks ssrc's sender for a keyframe (RTCP PLI): its encoder ran
// before we received, and may not send one for minutes.
func (p *PeerConnection) SendPLI(ssrc webrtc.SSRC) error {
	return p.pc.WriteRTCP([]rtcp.Packet{
		&rtcp.PictureLossIndication{MediaSSRC: uint32(ssrc)},
	})
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

// DebugTransceivers describes every transceiver, for logs.
func (p *PeerConnection) DebugTransceivers() []string {
	var out []string
	for _, t := range p.pc.GetTransceivers() {
		sendTrack, recvTrack := "none", "none"
		if s := t.Sender(); s != nil {
			if tr := s.Track(); tr != nil {
				sendTrack = tr.Kind().String() + "/" + tr.ID()
			}
		}
		if r := t.Receiver(); r != nil {
			if tracks := r.Tracks(); len(tracks) > 0 {
				recvTrack = tracks[0].Kind().String() + "/" + tracks[0].ID()
			}
		}
		out = append(out, fmt.Sprintf("mid=%s direction=%s send_track=%s recv_track=%s",
			t.Mid(), t.Direction(), sendTrack, recvTrack))
	}
	return out
}

// Stats is the packet loss the peer reports and the candidate pair's RTT.
// 0 means no data yet.
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

// VideoSenderSSRC is our video sender's SSRC, 0 before it sends. GetStats
// doesn't report a renegotiated track's sending (see loopback_test.go).
func (p *PeerConnection) VideoSenderSSRC() webrtc.SSRC {
	for _, t := range p.pc.GetTransceivers() {
		if t.Kind() != webrtc.RTPCodecTypeVideo {
			continue
		}
		if s := t.Sender(); s != nil {
			if encodings := s.GetParameters().Encodings; len(encodings) > 0 {
				return encodings[0].SSRC
			}
		}
	}
	return 0
}

// VideoMid is the mid of our video transceiver, "" before AddVideoTrack. By
// its sender, not kind: the peer's video may have one too, and
// re-announcing that makes peers end the call.
func (p *PeerConnection) VideoMid() string {
	if p.videoTrack == nil {
		return ""
	}
	for _, t := range p.pc.GetTransceivers() {
		if s := t.Sender(); s != nil {
			if tr := s.Track(); tr != nil && tr.ID() == p.videoTrack.ID() {
				return t.Mid()
			}
		}
	}
	return ""
}
