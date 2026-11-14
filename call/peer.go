package call

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/interceptor/pkg/gcc"

	"github.com/pion/rtcp"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// PeerConnection is a pion PeerConnection for a call: Opus audio, video
// added when sent. It knows SDP, not Jingle (see jingle_sdp.go).
type PeerConnection struct {
	pc         *webrtc.PeerConnection
	track      *webrtc.TrackLocalStaticSample
	videoTrack *webrtc.TrackLocalStaticSample
	videoCodec VideoCodec
	link       *linkMonitor

	mu       sync.Mutex
	ours     map[uint32]bool              // our senders' SSRCs, for link
	draining map[*webrtc.RTPReceiver]bool // receivers drainReceiverRTCP reads

	// sent counts the media bytes written, sentRate what LinkStats last
	// made of it as of sentAt.
	sent     atomic.Int64
	sentAt   time.Time
	sentLast int64
	sentRate int
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
	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterDefaultCodecs(); err != nil {
		return nil, fmt.Errorf("registering codecs: %w", err)
	}
	registry := &interceptor.Registry{}
	bitrate, err := registerInterceptors(mediaEngine, registry)
	if err != nil {
		return nil, err
	}
	api := webrtc.NewAPI(webrtc.WithSettingEngine(settingEngine), webrtc.WithMediaEngine(mediaEngine), webrtc.WithInterceptorRegistry(registry))

	pc, err := api.NewPeerConnection(config)
	if err != nil {
		return nil, fmt.Errorf("creating peer connection: %w", err)
	}
	p := &PeerConnection{pc: pc, ours: map[uint32]bool{}}
	p.link = newLinkMonitor(p.isOurs, bitrate)

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
	sender, err := pc.AddTrack(track)
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("adding local audio track: %w", err)
	}
	p.track = track
	p.readRTCP(sender)
	return p, nil
}

// registerInterceptors sets up pion's RTP/RTCP processing: its defaults
// (NACK, RTCP reports, transport-wide feedback on what we receive), plus
// transport-wide sequence numbers on what we send, for congestion control
// (GCC) to estimate the bandwidth from the peer's feedback on them. Each
// only takes effect if the peer negotiated it. The returned func reads
// GCC's estimate.
func registerInterceptors(m *webrtc.MediaEngine, registry *interceptor.Registry) (func() int, error) {
	congestion, err := cc.NewInterceptor(func() (cc.BandwidthEstimator, error) {
		// No pacing: GCC's pacer queues what's over its estimate, which
		// our encoders, not following it, would only fill with delay.
		return gcc.NewSendSideBWE(
			gcc.SendSideBWEInitialBitrate(initialBitrate),
			gcc.SendSideBWEMinBitrate(minBitrate),
			gcc.SendSideBWEMaxBitrate(maxBitrate),
			gcc.SendSideBWEPacer(gcc.NewNoOpPacer()),
		)
	})
	if err != nil {
		return nil, fmt.Errorf("creating congestion control: %w", err)
	}
	var estimator atomic.Pointer[cc.BandwidthEstimator]
	congestion.OnNewPeerConnection(func(_ string, e cc.BandwidthEstimator) { estimator.Store(&e) })
	// The last added runs first on what's sent: sequence numbers have to
	// be on a packet by the time congestion control sees it go out.
	registry.Add(congestion)
	if err := webrtc.ConfigureTWCCHeaderExtensionSender(m, registry); err != nil {
		return nil, fmt.Errorf("registering transport-wide sequence numbers: %w", err)
	}
	if err := webrtc.RegisterDefaultInterceptors(m, registry); err != nil {
		return nil, fmt.Errorf("registering interceptors: %w", err)
	}
	return func() int {
		if e := estimator.Load(); e != nil {
			return (*e).GetTargetBitrate()
		}
		return 0
	}, nil
}

// Congestion control's bounds, in bits/s.
const (
	initialBitrate = 1_000_000
	minBitrate     = 50_000
	maxBitrate     = 8_000_000
)

// readRTCP reads what the peer sends back about sender's stream, for as
// long as it lasts: pion's interceptors (answering NACKs, congestion
// control) only see RTCP that's read, and link sums it up.
func (p *PeerConnection) readRTCP(sender *webrtc.RTPSender) {
	p.mu.Lock()
	for _, e := range sender.GetParameters().Encodings {
		p.ours[uint32(e.SSRC)] = true
		if e.RTX.SSRC != 0 {
			p.ours[uint32(e.RTX.SSRC)] = true
		}
	}
	p.mu.Unlock()
	go func() {
		for {
			pkts, _, err := sender.ReadRTCP()
			if err != nil {
				return
			}
			p.link.onRTCP(pkts, time.Now())
		}
	}()
}

func (p *PeerConnection) isOurs(ssrc uint32) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ours[ssrc]
}

// LinkStats is what the peer's feedback says about the link to them, and
// what we've been sending over it since the last call (meant to be about
// once a second).
func (p *PeerConnection) LinkStats() LinkStats {
	now := time.Now()
	s := p.link.stats(now)
	p.mu.Lock()
	defer p.mu.Unlock()
	sent := p.sent.Load()
	switch elapsed := now.Sub(p.sentAt); {
	case p.sentAt.IsZero():
		p.sentAt, p.sentLast = now, sent
	case elapsed >= 500*time.Millisecond:
		p.sentRate = int(float64(sent-p.sentLast) * 8 / elapsed.Seconds())
		p.sentAt, p.sentLast = now, sent
	}
	s.Sent = p.sentRate
	return s
}

// AddVideoTrack adds our video track, to renegotiate before writing to it:
// H.264, or VP8 once the answer turns out to have no H.264 (see
// SetRemoteDescription).
func (p *PeerConnection) AddVideoTrack() error {
	if p.videoTrack != nil {
		return nil
	}
	track, err := newVideoTrack(VideoCodecH264)
	if err != nil {
		return err
	}
	// Sendonly: AddTrack's sendrecv would negotiate video back to us too.
	transceiver, err := p.pc.AddTransceiverFromTrack(track, webrtc.RTPTransceiverInit{
		Direction: webrtc.RTPTransceiverDirectionSendonly,
	})
	if err != nil {
		return fmt.Errorf("adding local video track: %w", err)
	}
	p.readRTCP(transceiver.Sender())
	p.videoTrack, p.videoCodec = track, VideoCodecH264
	return nil
}

// VideoCodec is the codec our video track sends, for its capture to
// encode.
func (p *PeerConnection) VideoCodec() VideoCodec {
	return p.videoCodec
}

// AttachVideoTrack puts our video track on the video transceiver of the
// remote offer just applied, so the answer accepts that content sendrecv
// instead of us needing a content of our own. Needs an offer that lets us
// send on it (sendrecv or recvonly), with H.264 or VP8 among its codecs:
// pion fails the whole answer over a track it can't bind.
func (p *PeerConnection) AttachVideoTrack() error {
	if p.videoTrack != nil {
		return nil
	}
	codec, ok := sendableVideoCodec(p.pc.RemoteDescription(), "")
	if !ok {
		return fmt.Errorf("remote offer's video has neither H.264 nor VP8")
	}
	track, err := newVideoTrack(codec)
	if err != nil {
		return err
	}
	sender, err := p.pc.AddTrack(track)
	if err != nil {
		return fmt.Errorf("adding local video track: %w", err)
	}
	for _, t := range p.pc.GetTransceivers() {
		if t.Sender() == sender && t.Mid() != "" {
			p.videoTrack, p.videoCodec = track, codec
			p.readRTCP(sender)
			return nil
		}
	}
	// A transceiver of its own, which an answer can't carry.
	_ = p.pc.RemoveTrack(sender)
	return fmt.Errorf("remote offer has no video we can send on")
}

// sendableVideoCodec picks what we send on desc's video m-line mid (the
// first video one for ""): H.264 if pion can bind our track to one of its
// H.264s, else VP8.
func sendableVideoCodec(desc *webrtc.SessionDescription, mid string) (VideoCodec, bool) {
	if desc == nil {
		return 0, false
	}
	var parsed sdp.SessionDescription
	if err := parsed.UnmarshalString(desc.SDP); err != nil {
		return 0, false
	}
	for _, m := range parsed.MediaDescriptions {
		if m.MediaName.Media != "video" {
			continue
		}
		if v, _ := m.Attribute("mid"); mid != "" && v != mid {
			continue
		}
		names, fmtps := map[string]string{}, map[string]string{}
		for _, a := range m.Attributes {
			pt, rest, _ := strings.Cut(a.Value, " ")
			switch a.Key {
			case "rtpmap":
				name, _, _ := strings.Cut(rest, "/")
				names[pt] = strings.ToUpper(name)
			case "fmtp":
				fmtps[pt] = rest
			}
		}
		vp8 := false
		for pt, name := range names {
			switch {
			case name == "H264" && bindableH264(fmtps[pt]):
				return VideoCodecH264, true
			case name == "VP8":
				vp8 = true
			}
		}
		return VideoCodecVP8, vp8
	}
	return 0, false
}

// bindableH264 reports whether pion matches our track's H.264 to one with
// fmtp: none at all, or the same packetization-mode and profile (the level
// is free).
func bindableH264(fmtp string) bool {
	if fmtp == "" {
		return true
	}
	params := map[string]string{}
	for _, kv := range strings.Split(fmtp, ";") {
		k, v, _ := strings.Cut(strings.TrimSpace(kv), "=")
		params[k] = strings.ToLower(v)
	}
	return params["packetization-mode"] == "1" && strings.HasPrefix(params["profile-level-id"], "42e0")
}

func newVideoTrack(codec VideoCodec) (*webrtc.TrackLocalStaticSample, error) {
	capability := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}
	if codec == VideoCodecH264 {
		capability = webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeH264,
			ClockRate: 90000,
			// Level 5.1 (0x33), up to about 4K at 15fps: it must cover the
			// screen's real size, as a hardware decoder sizes its buffers by
			// it and decodes nothing bigger.
			SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e033",
		}
	}
	track, err := webrtc.NewTrackLocalStaticSample(capability, "video", "kage")
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
	if desc.Type == webrtc.SDPTypeAnswer {
		if err := p.matchVideoCodec(&desc); err != nil {
			return err
		}
	}
	if err := p.pc.SetRemoteDescription(desc); err != nil {
		return fmt.Errorf("setting remote description: %w", err)
	}
	return nil
}

// matchVideoCodec switches the video track we offered to the codec answer
// takes, before applying it binds the track: VP8 for a peer without H.264.
func (p *PeerConnection) matchVideoCodec(answer *webrtc.SessionDescription) error {
	mid := p.VideoMid()
	if mid == "" {
		return nil
	}
	codec, ok := sendableVideoCodec(answer, mid)
	if !ok || codec == p.videoCodec {
		return nil
	}
	track, err := newVideoTrack(codec)
	if err != nil {
		return err
	}
	for _, t := range p.pc.GetTransceivers() {
		if t.Mid() == mid && t.Sender() != nil {
			if err := t.Sender().ReplaceTrack(track); err != nil {
				return fmt.Errorf("switching video to the answer's codec: %w", err)
			}
			p.videoTrack, p.videoCodec = track, codec
			return nil
		}
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
	p.sent.Add(int64(len(data)))
	return p.track.WriteSample(media.Sample{Data: data, Duration: duration})
}

// WriteVideoSample sends one H.264 frame (Annex-B) as one sample, lasting
// duration.
func (p *PeerConnection) WriteVideoSample(data []byte, duration time.Duration) error {
	if p.videoTrack == nil {
		return fmt.Errorf("no video track added yet")
	}
	p.sent.Add(int64(len(data)))
	return p.videoTrack.WriteSample(media.Sample{Data: data, Duration: duration})
}

// OnTrack registers f for each of the peer's tracks, audio or video.
func (p *PeerConnection) OnTrack(f func(*webrtc.TrackRemote)) {
	p.pc.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		p.drainReceiverRTCP(receiver)
		f(track)
	})
}

// drainReceiverRTCP reads what the peer sends about the stream receiver
// gets, for as long as it lasts: pion's interceptors only see RTCP that's
// read, and its sender reports are what our receiver reports echo for the
// peer to tell the round trip by.
func (p *PeerConnection) drainReceiverRTCP(receiver *webrtc.RTPReceiver) {
	p.mu.Lock()
	if p.draining == nil {
		p.draining = map[*webrtc.RTPReceiver]bool{}
	}
	started := p.draining[receiver]
	p.draining[receiver] = true
	p.mu.Unlock()
	if started {
		return
	}
	go func() {
		for {
			if _, _, err := receiver.ReadRTCP(); err != nil {
				return
			}
		}
	}()
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
