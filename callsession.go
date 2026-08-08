package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jim-ww/kage/call"
	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/xmpp"
	"github.com/pion/webrtc/v4"
)

// callState is the lifecycle of the one voice call an account can have in
// flight. Kept deliberately flat: there's no call waiting, no hold, and no
// renegotiation in this slice.
type callState int

const (
	callIdle          callState = iota
	callProposing               // outgoing: XEP-0353 propose sent, nothing back yet
	callRingingRemote           // outgoing: peer's device is alerting them
	callRingingLocal            // incoming: we're alerting the user, awaiting AnswerCall/RejectCall
	callNegotiating             // either direction: Jingle IQ/ICE exchange under way
	callConnected               // media flowing
	callEnded
)

func (s callState) String() string {
	switch s {
	case callProposing:
		return "proposing"
	case callRingingRemote:
		return "ringing-remote"
	case callRingingLocal:
		return "ringing-local"
	case callNegotiating:
		return "negotiating"
	case callConnected:
		return "connected"
	case callEnded:
		return "ended"
	default:
		return "idle"
	}
}

// callSession is one voice call: the Jingle signaling state plus the media
// pipeline (pion peer connection, PulseAudio mic/speaker, Opus codec) the
// daemon owns on its behalf. The TUI never touches any of it - it only makes
// the four call RPCs and reacts to the events broadcast from here.
type callSession struct {
	srv        *ipc.Server
	accountIdx int
	sess       *accountSession
	client     *xmpp.Client

	// peer is the contact's bare JID (what the UI shows); remoteJID is the
	// full JID every Jingle IQ of this session must be addressed to, learned
	// from the peer's first directed stanza. Jingle is resource-to-resource,
	// so the bare JID is never a valid target for the IQ half of the flow.
	peer      string
	remoteJID string
	sid       string
	incoming  bool

	mu    sync.Mutex
	state callState
	pc    *call.PeerConnection
	mic   *call.Mic
	spk   *call.Speaker
	enc   *call.Encoder
	dec   *call.Decoder

	// contentName/ufrag/pwd identify our half of the ICE session, repeated on
	// every transport-info we trickle out.
	contentName string
	ufrag       string
	pwd         string

	// signalingReady goes true once our session-initiate/-accept is on the
	// wire; until then locally-gathered candidates queue in pendingLocal
	// rather than being sent for a session the peer doesn't know about yet.
	signalingReady bool
	pendingLocal   []xmpp.ICECandidate

	// pendingRemote holds candidates that arrived by transport-info before we
	// had applied the peer's description - AddICECandidate rejects those.
	pendingRemote []xmpp.ICECandidate
	remoteSet     bool

	mediaStarted bool
	// connectedAt is set the first time onConnectionState sees
	// PeerConnectionStateConnected (guarded the same way as mediaStarted, so
	// a later ICE-recovery blip doesn't reset it) - used by end() to compute
	// the call's duration for the persisted call-log row.
	connectedAt time.Time
	done        chan struct{}
	closeOnce   sync.Once

	// ICE restart bookkeeping (XEP-0166/0176 §6.1). Only the Jingle
	// initiator (!incoming) ever drives a restart; the responder just
	// answers whatever transport-replace arrives. restarting guards against
	// starting a second attempt while one is in flight or re-triggering on
	// a stale connection-state callback; restartAttempts is the retry
	// budget; disconnectTimer is the one-shot grace-period check for
	// PeerConnectionStateDisconnected, which is often transient and
	// resolves on its own within a few seconds.
	restarting      bool
	restartAttempts int
	disconnectTimer *time.Timer

	// muted gates the mic-read goroutine in startMedia: while true, captured
	// frames are dropped instead of encoded+sent. quality is the last
	// computed bucket from the periodic sampler started in onConnectionState
	// (see qualityTicker/sampleQuality) - "" until the first sample lands.
	muted         bool
	quality       string
	qualityTicker *time.Ticker
	qualityDone   chan struct{}

	// ring plays the ringback/ringtone while this call sits in
	// callRingingRemote/callRingingLocal - see startRing/stopRing. Always nil
	// once past the ringing states.
	ring *call.RingTone
}

// Ringback (outgoing call, peer's device is alerting them) and ringtone
// (incoming call, we're alerting the user) use distinct chords/cadences so
// the two are audibly distinguishable. Each plays a soft two-note major-third
// chord rather than one raw sine tone, and ringtone uses a modern
// double-pulse cadence ("brr-brr ... brr-brr ...") instead of one long
// buzz - both changes are what make it read as a phone ringing rather than
// an alarm.
var (
	ringbackFreqsHz = []float64{480, 600}
	ringbackPattern = []time.Duration{time.Second, 3 * time.Second}

	ringtoneFreqsHz = []float64{523.25, 659.25} // C5 + E5
	ringtonePattern = []time.Duration{
		400 * time.Millisecond, 200 * time.Millisecond,
		400 * time.Millisecond, 1800 * time.Millisecond,
	}
)

// startRing begins playing a tone pattern for the current ringing state, if
// one isn't already playing. Runs on its own dedicated speaker (see
// call.RingTone), never the call's real media speaker.
func (c *callSession) startRing(freqsHz []float64, pattern []time.Duration) {
	c.mu.Lock()
	if c.ring != nil {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	r, err := call.NewRingTone(freqsHz, pattern)
	if err != nil {
		slog.Warn("starting ring tone", "sid", c.sid, "err", err)
		return
	}

	c.mu.Lock()
	if c.ring != nil || c.state == callEnded {
		c.mu.Unlock()
		r.Stop()
		return
	}
	c.ring = r
	c.mu.Unlock()
}

// stopRing halts the ring tone, if one is playing - called on every
// transition out of the ringing states (answered, negotiating, ended).
func (c *callSession) stopRing() {
	c.mu.Lock()
	r := c.ring
	c.ring = nil
	c.mu.Unlock()
	if r != nil {
		r.Stop()
	}
}

// qualitySampleInterval is how often the connected call's ICE/RTP stats are
// sampled to recompute the quality bucket - frequent enough to notice a
// degrading link within a few seconds, infrequent enough not to matter for
// CPU/allocation overhead.
const qualitySampleInterval = 3 * time.Second

// Packet-loss/RTT thresholds for the 3-tier quality bucket, picked as round
// numbers rather than tuned against real-world data: under 2% loss and
// under 150ms RTT reads as "good" (typical for a healthy direct or
// STUN-brokered path); under 8% loss and 400ms RTT is "fair" (still
// usable voice, noticeably worse); anything past that is "poor".
const (
	qualityGoodLossPct = 2.0
	qualityGoodRTTMs   = 150.0
	qualityFairLossPct = 8.0
	qualityFairRTTMs   = 400.0
)

func qualityBucket(lossPct, rttMs float64) string {
	switch {
	case lossPct <= qualityGoodLossPct && rttMs <= qualityGoodRTTMs:
		return "good"
	case lossPct <= qualityFairLossPct && rttMs <= qualityFairRTTMs:
		return "fair"
	default:
		return "poor"
	}
}

// maxICERestarts bounds how many transport-replace attempts a call will make
// after PeerConnectionStateFailed before giving up and ending the call.
const maxICERestarts = 3

// iceDisconnectGrace is how long PeerConnectionStateDisconnected is given to
// resolve on its own (ICE can un-disconnect without any action) before it's
// treated as failure worth restarting over.
const iceDisconnectGrace = 5 * time.Second

// --- accountSession call slot -------------------------------------------
//
// One call at a time per account, so the slot is a single pointer. callMu
// guards only the pointer; each callSession's own mu guards its state. Lock
// order is always callMu -> callSession.mu.

func (s *accountSession) currentCall() *callSession {
	s.callMu.Lock()
	defer s.callMu.Unlock()
	return s.call
}

// clearCall drops c from the slot if it's still the current call.
func (s *accountSession) clearCall(c *callSession) {
	s.callMu.Lock()
	if s.call == c {
		s.call = nil
	}
	s.callMu.Unlock()
}

// --- outgoing -----------------------------------------------------------

// startCall places an outgoing call to "to" (bare or full JID), sending the
// XEP-0353 propose that makes the peer's devices ring. The Jingle IQ
// exchange only begins once one of them answers with <proceed/>.
func (s *accountSession) startCall(ctx context.Context, srv *ipc.Server, accountIdx int, to string) error {
	client, err := s.liveClient()
	if err != nil {
		return err
	}

	s.callMu.Lock()
	if s.call != nil {
		s.callMu.Unlock()
		return fmt.Errorf("a call is already in progress")
	}
	c := &callSession{
		srv: srv, accountIdx: accountIdx, sess: s, client: client,
		peer: bareJID(to), sid: callSID(), state: callProposing,
		contentName: jingleContentName, done: make(chan struct{}),
	}
	s.call = c
	s.callMu.Unlock()

	if err := client.ProposeCall(ctx, c.peer, c.sid); err != nil {
		c.end(ctx, "failed", err.Error())
		return err
	}
	c.broadcastState(callProposing, "")
	return nil
}

// --- TUI-driven transitions ---------------------------------------------

// answerCall accepts the ringing incoming call: XEP-0353 <proceed/> tells
// the caller to start the Jingle exchange with this device specifically, and
// <accept/> to our own bare JID stops our other devices ringing.
func (s *accountSession) answerCall(ctx context.Context) error {
	c := s.currentCall()
	if c == nil {
		return fmt.Errorf("no incoming call")
	}
	c.mu.Lock()
	if !c.incoming || c.state != callRingingLocal {
		state := c.state
		c.mu.Unlock()
		return fmt.Errorf("call is not ringing (state %s)", state)
	}
	c.state = callNegotiating
	remote := c.remoteJID
	c.mu.Unlock()
	c.stopRing()

	if err := c.client.ProceedCall(ctx, remote, c.sid); err != nil {
		c.end(ctx, "failed", err.Error())
		return err
	}
	if err := c.client.AcceptCall(ctx, c.client.JID.Bare().String(), c.sid); err != nil {
		slog.Warn("notifying own devices of answered call", "sid", c.sid, "err", err)
	}
	c.broadcastState(callNegotiating, "")
	return nil
}

// hangupCall ends the current call from our side, using whichever of the two
// wire mechanisms applies: a XEP-0353 retract if the Jingle session never
// started, a session-terminate once it did.
func (s *accountSession) hangupCall(ctx context.Context) error {
	c := s.currentCall()
	if c == nil {
		return fmt.Errorf("no call in progress")
	}
	c.mu.Lock()
	state, remote := c.state, c.remoteJID
	c.mu.Unlock()

	switch state {
	case callProposing, callRingingRemote:
		if err := c.client.RetractCall(ctx, c.peer, c.sid); err != nil {
			slog.Warn("retracting call", "sid", c.sid, "err", err)
		}
	case callRingingLocal:
		// An incoming call we haven't answered yet: no Jingle session exists
		// to terminate, only the XEP-0353 propose to decline - same wire
		// action as rejectCall, reached here when hangupCall (ctrl+g) is used
		// instead of the dedicated reject key.
		if err := c.client.RejectCall(ctx, remote, c.sid); err != nil {
			slog.Warn("rejecting call", "sid", c.sid, "err", err)
		}
	default:
		if remote != "" {
			if err := c.client.SendSessionTerminate(ctx, remote, c.sid, &xmpp.JingleReason{Success: &struct{}{}}); err != nil {
				slog.Warn("terminating call", "sid", c.sid, "err", err)
			}
		}
	}
	c.end(ctx, "ended", "hung up")
	return nil
}

// rejectCall declines a ringing incoming call.
func (s *accountSession) rejectCall(ctx context.Context) error {
	c := s.currentCall()
	if c == nil {
		return fmt.Errorf("no call in progress")
	}
	c.mu.Lock()
	remote, state := c.remoteJID, c.state
	c.mu.Unlock()

	if state == callRingingLocal {
		if err := c.client.RejectCall(ctx, remote, c.sid); err != nil {
			slog.Warn("rejecting call", "sid", c.sid, "err", err)
		}
	} else if remote != "" {
		if err := c.client.SendSessionTerminate(ctx, remote, c.sid, &xmpp.JingleReason{Decline: &struct{}{}}); err != nil {
			slog.Warn("declining call", "sid", c.sid, "err", err)
		}
	}
	c.end(ctx, "ended", "declined")
	return nil
}

// muteCall sets or clears the local mic mute on the current call.
func (s *accountSession) muteCall(muted bool) error {
	c := s.currentCall()
	if c == nil {
		return fmt.Errorf("no call in progress")
	}
	c.setMuted(muted)
	return nil
}

// --- incoming signaling -------------------------------------------------

// handleJingleMessage drives the XEP-0353 half of the flow: the ring-before-
// session exchange that happens over <message/> rather than IQs.
func (s *accountSession) handleJingleMessage(ctx context.Context, srv *ipc.Server, accountIdx int, ev xmpp.JingleMessageEvent) {
	if ev.Action == xmpp.JMIPropose {
		s.handlePropose(ctx, srv, accountIdx, ev)
		return
	}

	c := s.currentCall()
	if c == nil || c.sid != ev.SID {
		return
	}

	switch ev.Action {
	case xmpp.JMIRinging:
		c.mu.Lock()
		if c.state == callProposing {
			c.state = callRingingRemote
		}
		c.mu.Unlock()
		c.startRing(ringbackFreqsHz, ringbackPattern)
		c.broadcastState(callRingingRemote, "")

	case xmpp.JMIProceed:
		// The callee picked a device; everything from here is addressed to
		// that full JID, and we're the Jingle initiator.
		c.mu.Lock()
		if c.incoming || c.state == callNegotiating || c.state == callConnected {
			c.mu.Unlock()
			return
		}
		c.remoteJID = ev.From
		c.state = callNegotiating
		c.mu.Unlock()
		c.stopRing()
		c.broadcastState(callNegotiating, "")
		if err := c.initiateSession(ctx); err != nil {
			slog.Warn("starting jingle session", "sid", c.sid, "err", err)
			c.end(ctx, "failed", err.Error())
		}

	case xmpp.JMIReject:
		c.end(ctx, "ended", "declined")

	case xmpp.JMIRetract:
		c.end(ctx, "ended", "caller hung up")

	case xmpp.JMIAccept:
		// <accept/> is sent to our own bare JID so every resource of this
		// account learns the call was answered - including the resource
		// that sent it. answerCall's own AcceptCall call routes straight
		// back to us this way, and matching ev.From against our own JID to
		// tell the two apart isn't reliable (message-carbon unwrapping of
		// our own sent stanza doesn't reliably preserve a comparable
		// "from"). c.state is: answerCall moves it to callNegotiating
		// before ever calling AcceptCall, so by the time our own echo (or
		// anything else) arrives here we're provably past "still ringing" -
		// only end the call if we're still in callRingingLocal, meaning we
		// ourselves haven't answered and this really is a different
		// resource taking it.
		c.mu.Lock()
		stillRinging := c.incoming && c.state == callRingingLocal
		c.mu.Unlock()
		if stillRinging {
			c.end(ctx, "ended", "answered elsewhere")
		}
	}
}

// handlePropose surfaces an incoming call to the TUI. It never auto-answers:
// it only sends <ringing/> (so the caller sees us alerting) and waits for an
// AnswerCall/RejectCall RPC.
func (s *accountSession) handlePropose(ctx context.Context, srv *ipc.Server, accountIdx int, ev xmpp.JingleMessageEvent) {
	client, err := s.liveClient()
	if err != nil {
		return
	}
	from := bareJID(ev.From)
	if from == s.account.JID {
		return // carbon of a propose one of our own devices sent
	}

	s.callMu.Lock()
	if s.call != nil {
		s.callMu.Unlock()
		// Busy: decline rather than leave the caller ringing forever. No call
		// waiting in this slice - just let the TUI show what it missed.
		if err := client.RejectCall(ctx, ev.From, ev.SID); err != nil {
			slog.Warn("rejecting call while busy", "sid", ev.SID, "err", err)
		}
		broadcast(srv, evMissedCall, missedCallEvent{AccountIdx: accountIdx, From: from, SID: ev.SID})
		return
	}
	c := &callSession{
		srv: srv, accountIdx: accountIdx, sess: s, client: client,
		peer: from, remoteJID: ev.From, sid: ev.SID, incoming: true,
		state: callRingingLocal, contentName: jingleContentName, done: make(chan struct{}),
	}
	s.call = c
	s.callMu.Unlock()

	if err := client.RingingCall(ctx, ev.From, ev.SID); err != nil {
		slog.Warn("sending ringing", "sid", ev.SID, "err", err)
	}
	broadcast(srv, evIncomingCall, incomingCallEvent{
		AccountIdx: accountIdx, From: from, SID: ev.SID, Media: ev.Media,
	})
	c.startRing(ringtoneFreqsHz, ringtonePattern)
	c.broadcastState(callRingingLocal, "")
}

// handleJingle drives the XEP-0166 IQ half of the flow.
func (s *accountSession) handleJingle(ctx context.Context, srv *ipc.Server, accountIdx int, ev xmpp.JingleEvent) {
	c := s.currentCall()
	if c == nil || c.sid != ev.Jingle.SID {
		// A session-initiate with no matching propose is a caller that
		// skipped XEP-0353 entirely - terminate rather than silently ignore.
		if ev.Jingle.Action == xmpp.JingleActionSessionInitiate {
			if client, err := s.liveClient(); err == nil {
				_ = client.SendSessionTerminate(ctx, ev.From, ev.Jingle.SID, &xmpp.JingleReason{Decline: &struct{}{}})
			}
		}
		return
	}

	switch ev.Jingle.Action {
	case xmpp.JingleActionSessionInitiate:
		c.mu.Lock()
		c.remoteJID = ev.From
		c.mu.Unlock()
		if err := c.acceptSession(ctx, ev.Jingle); err != nil {
			slog.Warn("accepting jingle session", "sid", c.sid, "err", err)
			c.end(ctx, "failed", err.Error())
		}

	case xmpp.JingleActionSessionAccept:
		if err := c.applyAnswer(ctx, ev.Jingle); err != nil {
			slog.Warn("applying jingle session-accept", "sid", c.sid, "err", err)
			c.end(ctx, "failed", err.Error())
		}

	case xmpp.JingleActionTransportInfo:
		c.addRemoteCandidates(ev.Jingle)

	case xmpp.JingleActionTransportReplace:
		c.applyTransportReplace(ctx, ev.Jingle)

	case xmpp.JingleActionTransportAccept:
		c.applyTransportAccept(ev.Jingle)

	case xmpp.JingleActionTransportReject:
		c.end(ctx, "ended", "peer rejected connection restart")

	case xmpp.JingleActionSessionTerminate:
		c.end(ctx, "ended", terminateReason(ev.Jingle.Reason))
	}
}

func terminateReason(r *xmpp.JingleReason) string {
	switch {
	case r == nil:
		return "terminated"
	case r.Decline != nil:
		return "declined"
	case r.Cancel != nil:
		return "cancelled"
	case r.ConnectivityError != nil:
		return "connectivity error"
	case r.Text != "":
		return r.Text
	default:
		return "terminated"
	}
}

// --- media/SDP negotiation ----------------------------------------------

// setupPeer creates the peer connection and registers every callback before
// any description is applied - OnTrack in particular must be in place before
// SetRemoteDescription, or the remote audio track arrives with nobody
// reading it.
func (c *callSession) setupPeer(ctx context.Context) error {
	pc, err := call.NewPeerConnection()
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.pc = pc
	c.mu.Unlock()

	pc.OnTrack(func(track *webrtc.TrackRemote) { go c.playRemote(track) })
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		// Run off pion's own goroutine: both branches take c.mu and one of
		// them tears the connection down.
		go c.onConnectionState(ctx, state)
	})
	pc.OnICECandidate(func(cand *webrtc.ICECandidate) {
		if cand == nil {
			return // gathering complete
		}
		c.sendCandidate(ctx, jingleCandidateFromICE(cand))
	})
	return nil
}

// initiateSession is the outgoing path once the callee has proceeded: build
// the offer and send it as a session-initiate.
func (c *callSession) initiateSession(ctx context.Context) error {
	if err := c.setupPeer(ctx); err != nil {
		return err
	}
	offer, err := c.pc.CreateOffer()
	if err != nil {
		return err
	}
	contents, err := jingleContentsFromSDP(offer)
	if err != nil {
		return err
	}
	c.rememberTransport(contents)

	c.mu.Lock()
	remote := c.remoteJID
	c.mu.Unlock()
	if err := c.client.SendSessionInitiate(ctx, remote, xmpp.JingleIQ{SID: c.sid, Contents: contents}); err != nil {
		return err
	}
	c.flushLocalCandidates(ctx)
	return nil
}

// acceptSession is the incoming path: apply the caller's offer, answer it,
// and send that answer back as a session-accept.
func (c *callSession) acceptSession(ctx context.Context, jingle xmpp.JingleIQ) error {
	if err := c.setupPeer(ctx); err != nil {
		return err
	}
	offer, err := sdpFromJingleContents(jingle.Contents, webrtc.SDPTypeOffer)
	if err != nil {
		return err
	}
	if err := c.pc.SetRemoteDescription(offer); err != nil {
		return err
	}
	c.markRemoteSet()

	answer, err := c.pc.CreateAnswer()
	if err != nil {
		return err
	}
	contents, err := jingleContentsFromSDP(answer)
	if err != nil {
		return err
	}
	// Echo the initiator's content name so both ends agree on the mid.
	if in, ok := firstContentOfKind(jingle.Contents, "audio"); ok && in.Name != "" {
		contents[0].Name = in.Name
	}
	c.rememberTransport(contents)

	c.mu.Lock()
	c.state = callNegotiating
	remote := c.remoteJID
	c.mu.Unlock()

	if err := c.client.SendSessionAccept(ctx, remote, xmpp.JingleIQ{SID: c.sid, Contents: contents}); err != nil {
		return err
	}
	c.flushLocalCandidates(ctx)
	return nil
}

// applyAnswer is the outgoing path's final signaling step: the callee's
// session-accept becomes our remote description.
func (c *callSession) applyAnswer(ctx context.Context, jingle xmpp.JingleIQ) error {
	answer, err := sdpFromJingleContents(jingle.Contents, webrtc.SDPTypeAnswer)
	if err != nil {
		return err
	}
	c.mu.Lock()
	pc := c.pc
	c.mu.Unlock()
	if pc == nil {
		return fmt.Errorf("session-accept before any offer was sent")
	}
	if err := pc.SetRemoteDescription(answer); err != nil {
		return err
	}
	c.markRemoteSet()
	return nil
}

// rememberTransport records our own ICE credentials and content name from
// the description we just generated, so trickled transport-info IQs can
// repeat them.
func (c *callSession) rememberTransport(contents []xmpp.JingleContent) {
	// ICE-UDP is bundled here (one ICE session shared by every mid via
	// a=group:BUNDLE), so a single ufrag/pwd pair is correct even once a
	// second content (e.g. video) exists - picking the audio one specifically
	// is just a stable, always-present anchor, not a real per-content choice.
	content, ok := firstContentOfKind(contents, "audio")
	if !ok || content.Transport == nil {
		return
	}
	c.mu.Lock()
	c.contentName = content.Name
	c.ufrag = content.Transport.Ufrag
	c.pwd = content.Transport.Pwd
	c.signalingReady = true
	c.mu.Unlock()
}

// sendCandidate trickles one locally-gathered candidate to the peer, or
// queues it if our own session-initiate/-accept isn't on the wire yet.
func (c *callSession) sendCandidate(ctx context.Context, cand xmpp.ICECandidate) {
	c.mu.Lock()
	if !c.signalingReady {
		c.pendingLocal = append(c.pendingLocal, cand)
		c.mu.Unlock()
		return
	}
	remote, name, ufrag, pwd := c.remoteJID, c.contentName, c.ufrag, c.pwd
	c.mu.Unlock()

	if remote == "" {
		return
	}
	if err := c.client.SendTransportInfo(ctx, remote, c.sid, jingleTransportInfoContent(name, ufrag, pwd, cand)); err != nil {
		slog.Warn("trickling ice candidate", "sid", c.sid, "err", err)
	}
}

func (c *callSession) flushLocalCandidates(ctx context.Context) {
	c.mu.Lock()
	pending := c.pendingLocal
	c.pendingLocal = nil
	c.mu.Unlock()
	for _, cand := range pending {
		c.sendCandidate(ctx, cand)
	}
}

// addRemoteCandidates applies (or queues, if the peer's description hasn't
// been set yet) the candidates in a transport-info.
func (c *callSession) addRemoteCandidates(jingle xmpp.JingleIQ) {
	for _, content := range jingle.Contents {
		if content.Transport == nil {
			continue
		}
		for _, cand := range content.Transport.Candidates {
			c.mu.Lock()
			if !c.remoteSet || c.pc == nil {
				c.pendingRemote = append(c.pendingRemote, cand)
				c.mu.Unlock()
				continue
			}
			pc, mid := c.pc, c.contentName
			c.mu.Unlock()
			if err := pc.AddICECandidate(iceCandidateInit(cand, mid)); err != nil {
				slog.Warn("adding remote ice candidate", "sid", c.sid, "err", err)
			}
		}
	}
}

// markRemoteSet unblocks any candidates that arrived ahead of the peer's
// description.
func (c *callSession) markRemoteSet() {
	c.mu.Lock()
	c.remoteSet = true
	pending := c.pendingRemote
	c.pendingRemote = nil
	pc, mid := c.pc, c.contentName
	c.mu.Unlock()

	for _, cand := range pending {
		if err := pc.AddICECandidate(iceCandidateInit(cand, mid)); err != nil {
			slog.Warn("adding queued remote ice candidate", "sid", c.sid, "err", err)
		}
	}
}

func (c *callSession) onConnectionState(ctx context.Context, state webrtc.PeerConnectionState) {
	switch state {
	case webrtc.PeerConnectionStateConnected:
		c.mu.Lock()
		already := c.mediaStarted
		c.mediaStarted = true
		if !already {
			c.connectedAt = time.Now()
		}
		c.state = callConnected
		c.restarting = false
		c.restartAttempts = 0
		if c.disconnectTimer != nil {
			c.disconnectTimer.Stop()
			c.disconnectTimer = nil
		}
		c.mu.Unlock()
		if already {
			return // an ICE recovery after a blip, not a new call
		}
		if err := c.startMedia(); err != nil {
			slog.Warn("starting call audio", "sid", c.sid, "err", err)
			c.end(ctx, "failed", err.Error())
			return
		}
		c.startQualitySampler()
		c.broadcastState(callConnected, "")
	case webrtc.PeerConnectionStateFailed:
		c.handleICEFailure(ctx)
	case webrtc.PeerConnectionStateDisconnected:
		// Often transient - ICE can un-disconnect on its own within a few
		// seconds. Only escalate to a restart attempt if it's still
		// disconnected (or has since failed) once the grace period elapses.
		c.scheduleDisconnectCheck(ctx)
	case webrtc.PeerConnectionStateClosed:
		// Fires as a side effect of DTLS/ICE teardown on ANY hangup, not just
		// a network failure - a peer ending the call normally closes their
		// side's connection before (or without ever) sending the XEP-0166
		// session-terminate that would explain why, and by the time it
		// arrives (observed ~150-200ms later live) this callSession is
		// already gone, so it can't be used to fill in a more specific
		// reason. "connection lost" would wrongly imply a failure here -
		// "call ended" is the honest, direction-agnostic description.
		c.end(ctx, "ended", "call ended")
	}
}

// handleICEFailure reacts to PeerConnectionStateFailed (or a
// still-disconnected connection past its grace period). Before the call ever
// connected, or once the retry budget is exhausted, it ends the call exactly
// as before ICE restart existed. Once connected, only the Jingle initiator
// drives a restart - the responder just waits for the initiator's
// transport-replace (see handleJingle).
func (c *callSession) handleICEFailure(ctx context.Context) {
	c.mu.Lock()
	if c.state != callConnected {
		c.mu.Unlock()
		c.end(ctx, "failed", "ice connection failed")
		return
	}
	if c.incoming || c.restarting {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	c.attemptICERestart(ctx)
}

// scheduleDisconnectCheck arms (if not already armed) a one-shot timer that
// re-checks the connection state after iceDisconnectGrace - most
// disconnects resolve on their own before it fires.
func (c *callSession) scheduleDisconnectCheck(ctx context.Context) {
	c.mu.Lock()
	if c.disconnectTimer != nil {
		c.mu.Unlock()
		return
	}
	c.disconnectTimer = time.AfterFunc(iceDisconnectGrace, func() {
		c.mu.Lock()
		c.disconnectTimer = nil
		pc, state := c.pc, c.state
		c.mu.Unlock()
		if state != callConnected || pc == nil {
			return
		}
		switch pc.ConnectionState() {
		case webrtc.PeerConnectionStateDisconnected, webrtc.PeerConnectionStateFailed:
			c.handleICEFailure(ctx)
		}
	})
	c.mu.Unlock()
}

// attemptICERestart drives one XEP-0166/0176 §6.1 transport restart attempt:
// regenerate our offer with fresh ICE credentials and send it as a
// transport-replace. The peer's transport-accept (applied in
// applyTransportAccept) completes the restart; failure to even get the
// replace out schedules a bounded, backed-off retry via scheduleRestartRetry.
func (c *callSession) attemptICERestart(ctx context.Context) {
	c.mu.Lock()
	if c.restarting {
		c.mu.Unlock()
		return
	}
	c.restartAttempts++
	attempt := c.restartAttempts
	if attempt > maxICERestarts {
		c.mu.Unlock()
		c.end(ctx, "ended", fmt.Sprintf("connection lost after %d restart attempts", maxICERestarts))
		return
	}
	c.restarting = true
	pc, remote := c.pc, c.remoteJID
	c.mu.Unlock()

	if pc == nil || remote == "" {
		c.mu.Lock()
		c.restarting = false
		c.mu.Unlock()
		return
	}

	offer, err := pc.RestartICE()
	if err != nil {
		slog.Warn("restarting ice", "sid", c.sid, "attempt", attempt, "err", err)
		c.scheduleRestartRetry(ctx, attempt)
		return
	}
	contents, err := jingleContentsFromSDP(offer)
	if err != nil || len(contents) == 0 {
		slog.Warn("building transport-replace from restart offer", "sid", c.sid, "attempt", attempt, "err", err)
		c.scheduleRestartRetry(ctx, attempt)
		return
	}
	c.rememberTransport(contents)

	content, ok := firstContentOfKind(contents, "audio")
	if !ok {
		content = contents[0]
	}
	replaceContent := xmpp.JingleContent{Creator: "initiator", Name: content.Name, Transport: content.Transport}
	if err := c.client.SendTransportReplace(ctx, remote, c.sid, replaceContent); err != nil {
		slog.Warn("sending transport-replace", "sid", c.sid, "attempt", attempt, "err", err)
		c.scheduleRestartRetry(ctx, attempt)
		return
	}
	c.flushLocalCandidates(ctx)
}

// scheduleRestartRetry backs off (1s, 2s, 4s, capped at 8s - the same
// doubling shape as reconnectWithBackoff in account.go, just bounded by
// maxICERestarts instead of running forever) and, if the call is still
// connected and hasn't recovered by itself, makes another restart attempt.
func (c *callSession) scheduleRestartRetry(ctx context.Context, attempt int) {
	c.mu.Lock()
	c.restarting = false
	c.mu.Unlock()

	backoff := time.Second << uint(attempt-1)
	if backoff > 8*time.Second {
		backoff = 8 * time.Second
	}
	time.AfterFunc(backoff, func() {
		select {
		case <-c.done:
			return
		default:
		}
		c.mu.Lock()
		state := c.state
		c.mu.Unlock()
		if state != callConnected {
			return
		}
		c.attemptICERestart(ctx)
	})
}

// applyTransportReplace is the responder side of a restart: apply the
// initiator's new offer (pion detects the changed ICE credentials and
// restarts the ICE transport itself, regenerating our local credentials too
// - see PeerConnection.SetRemoteDescription), then answer with
// transport-accept naming our own restarted transport. A malformed replace
// gets a transport-reject rather than silently doing nothing.
func (c *callSession) applyTransportReplace(ctx context.Context, jingle xmpp.JingleIQ) {
	c.mu.Lock()
	pc, remote, state := c.pc, c.remoteJID, c.state
	c.mu.Unlock()
	if pc == nil || state != callConnected {
		return
	}

	offer, err := sdpFromJingleContents(jingle.Contents, webrtc.SDPTypeOffer)
	if err != nil {
		slog.Warn("parsing transport-replace", "sid", c.sid, "err", err)
		if err := c.client.SendTransportReject(ctx, remote, c.sid); err != nil {
			slog.Warn("sending transport-reject", "sid", c.sid, "err", err)
		}
		return
	}
	if err := pc.SetRemoteDescription(offer); err != nil {
		slog.Warn("applying transport-replace", "sid", c.sid, "err", err)
		if err := c.client.SendTransportReject(ctx, remote, c.sid); err != nil {
			slog.Warn("sending transport-reject", "sid", c.sid, "err", err)
		}
		return
	}
	c.markRemoteSet()

	answer, err := pc.CreateAnswer()
	if err != nil {
		slog.Warn("answering transport-replace", "sid", c.sid, "err", err)
		return
	}
	contents, err := jingleContentsFromSDP(answer)
	if err != nil || len(contents) == 0 {
		slog.Warn("building transport-accept from restart answer", "sid", c.sid, "err", err)
		return
	}
	c.rememberTransport(contents)

	content, ok := firstContentOfKind(contents, "audio")
	if !ok {
		content = contents[0]
	}
	acceptContent := xmpp.JingleContent{Creator: "initiator", Name: content.Name, Transport: content.Transport}
	if err := c.client.SendTransportAccept(ctx, remote, c.sid, acceptContent); err != nil {
		slog.Warn("sending transport-accept", "sid", c.sid, "err", err)
	}
	c.flushLocalCandidates(ctx)
}

// applyTransportAccept is the initiator side's final step: the peer's
// answer to our transport-replace becomes the new remote description,
// completing the restart.
func (c *callSession) applyTransportAccept(jingle xmpp.JingleIQ) {
	c.mu.Lock()
	pc := c.pc
	c.mu.Unlock()
	if pc == nil {
		return
	}
	answer, err := sdpFromJingleContents(jingle.Contents, webrtc.SDPTypeAnswer)
	if err != nil {
		slog.Warn("parsing transport-accept", "sid", c.sid, "err", err)
		return
	}
	if err := pc.SetRemoteDescription(answer); err != nil {
		slog.Warn("applying transport-accept", "sid", c.sid, "err", err)
		return
	}
	c.mu.Lock()
	c.restarting = false
	c.mu.Unlock()
}

// startMedia opens the audio devices and starts pumping mic -> Opus -> RTP.
// The receive direction is started by OnTrack instead, since it can't begin
// before the peer's track actually arrives.
func (c *callSession) startMedia() error {
	enc, err := call.NewEncoder()
	if err != nil {
		return err
	}
	dec, err := call.NewDecoder()
	if err != nil {
		return err
	}
	mic, err := call.NewMic()
	if err != nil {
		return err
	}
	spk, err := call.NewSpeaker()
	if err != nil {
		mic.Close()
		return err
	}

	c.mu.Lock()
	c.enc, c.dec, c.mic, c.spk = enc, dec, mic, spk
	pc := c.pc
	c.mu.Unlock()

	go func() {
		const frameDuration = call.FrameMillis * time.Millisecond
		for {
			select {
			case <-c.done:
				return
			case frame := <-mic.Frames():
				c.mu.Lock()
				muted := c.muted
				c.mu.Unlock()
				if muted {
					continue // drop the frame rather than encode+send while muted
				}
				packet, err := enc.Encode(frame)
				if err != nil {
					slog.Warn("encoding call audio", "sid", c.sid, "err", err)
					continue
				}
				if err := pc.WriteSample(packet, frameDuration); err != nil {
					slog.Warn("writing call audio", "sid", c.sid, "err", err)
					return
				}
			}
		}
	}()
	return nil
}

// playRemote drains the peer's RTP track into the speaker. It runs from the
// moment the track appears, which can be before startMedia has opened the
// speaker - packets read in that window are dropped rather than buffered,
// which costs at most a few tens of milliseconds of audio at call setup.
func (c *callSession) playRemote(track *webrtc.TrackRemote) {
	pcm := make([]int16, call.FrameSamples*call.Channels)
	for {
		select {
		case <-c.done:
			return
		default:
		}

		packet, _, err := track.ReadRTP()
		if err != nil {
			return // track ended, or the connection went away
		}
		if len(packet.Payload) == 0 {
			continue
		}

		c.mu.Lock()
		dec, spk := c.dec, c.spk
		c.mu.Unlock()
		if dec == nil || spk == nil {
			continue
		}

		n, err := dec.Decode(packet.Payload, pcm)
		if err != nil {
			slog.Warn("decoding call audio", "sid", c.sid, "err", err)
			continue
		}
		// Speaker.Write keeps the slice until playback consumes it, so it
		// can't be the reused decode buffer.
		frame := make([]int16, n*call.Channels)
		copy(frame, pcm[:n*call.Channels])
		if err := spk.Write(frame); err != nil {
			return
		}
	}
}

// --- teardown -----------------------------------------------------------

// end tears the call down exactly once and tells the TUI why. It does not
// send any stanza itself: whichever caller decided the call is over already
// sent (or received) the terminate/reject/retract that says so on the wire.
func (c *callSession) end(ctx context.Context, state, reason string) {
	c.closeOnce.Do(func() {
		close(c.done)
		c.stopRing()

		c.mu.Lock()
		c.state = callEnded
		pc, mic, spk := c.pc, c.mic, c.spk
		c.pc, c.mic, c.spk, c.enc, c.dec = nil, nil, nil, nil, nil
		if c.disconnectTimer != nil {
			c.disconnectTimer.Stop()
			c.disconnectTimer = nil
		}
		c.mu.Unlock()

		c.stopQualitySampler()

		if mic != nil {
			mic.Close()
		}
		if spk != nil {
			spk.Close()
		}
		if pc != nil {
			if err := pc.Close(); err != nil {
				slog.Warn("closing call peer connection", "sid", c.sid, "err", err)
			}
		}

		c.sess.clearCall(c)
		broadcast(c.srv, evCallState, callStateEvent{
			AccountIdx: c.accountIdx, Peer: c.peer, SID: c.sid, State: state, Reason: reason,
		})

		c.logCall(state, reason)
	})
}

// callOutcome buckets a finished call into one of the four call-log outcomes
// ("answered", "missed", "declined", "failed") from its final lifecycle
// state, whether media ever actually started, and the free-text reason
// string passed to end() - see the various c.end(ctx, "ended"/"failed", ...)
// call sites throughout this file for every reason string actually in use
// ("hung up", "declined", "caller hung up", "answered elsewhere",
// terminateReason's outputs, "call ended", "ice connection failed", the
// restart-exhausted message, "peer rejected connection restart"). A call
// that was ever connected (mediaStarted) is "answered" even if it later
// disconnected - it happened and had a duration. Otherwise state == "failed"
// is "failed"; an explicit decline is "declined"; anything else that never
// connected (we or the peer hung up/retracted/cancelled before pickup, or
// the ringing device answered elsewhere) reads as "missed" - the closest of
// the four buckets to "nothing was ever heard".
func callOutcome(state, reason string, mediaStarted bool) string {
	switch {
	case mediaStarted:
		return "answered"
	case state == "failed":
		return "failed"
	case reason == "declined":
		return "declined"
	default:
		return "missed"
	}
}

// logCall persists this finished call as a call-log row in the same chat as
// the peer, and broadcasts it live via the same event a normal incoming
// message uses, so it appears immediately in the timeline of anyone with
// this chat open. Runs from inside end()'s closeOnce.Do, after teardown.
//
// Every callSession that reaches here represents a real call attempt (an
// outgoing XEP-0353 propose we sent, or one we received from the peer) - a
// purely local pre-flight failure before any signaling happened never gets
// this far, since setupPeer only ever runs after a propose/proceed
// round-trip already occurred - so there's no "too early to log" case to
// filter out here.
func (c *callSession) logCall(state, reason string) {
	c.mu.Lock()
	mediaStarted := c.mediaStarted
	connectedAt := c.connectedAt
	incoming := c.incoming
	c.mu.Unlock()

	outcome := callOutcome(state, reason, mediaStarted)
	var duration time.Duration
	if outcome == "answered" && !connectedAt.IsZero() {
		duration = time.Since(connectedAt)
	}

	direction := "outgoing"
	if incoming {
		direction = "incoming"
	}

	id, err := c.sess.db.InsertCallLog(context.Background(), storage.InsertCallLogParams{
		AccountJid:       c.sess.account.JID,
		Sent:             !incoming,
		RosterJid:        nullString(c.peer),
		CallDirection:    nullString(direction),
		CallOutcome:      nullString(outcome),
		CallDurationSecs: sql.NullInt64{Int64: int64(duration.Seconds()), Valid: outcome == "answered"},
	})
	if err != nil {
		slog.Warn("persisting call log", "sid", c.sid, "peer", c.peer, "err", err)
		return
	}

	broadcast(c.srv, evIncomingMessage, ui.IncomingMessageMsg{
		AccountIdx: c.accountIdx,
		From:       c.peer,
		Message: ui.Message{
			ID:     fmt.Sprintf("call-%d", id),
			Author: c.peer,
			SentAt: time.Now(),
			IsMe:   !incoming,
			CallLog: &ui.CallLogInfo{
				Direction: direction,
				Outcome:   outcome,
				Duration:  duration,
			},
		},
	})
}

func (c *callSession) broadcastState(state callState, reason string) {
	c.mu.Lock()
	muted, quality := c.muted, c.quality
	c.mu.Unlock()
	broadcast(c.srv, evCallState, callStateEvent{
		AccountIdx: c.accountIdx, Peer: c.peer, SID: c.sid, State: state.String(), Reason: reason,
		Muted: muted, Quality: quality,
	})
}

// setMuted flips the mic-gate checked by startMedia's capture loop and
// broadcasts the new state at the call's current lifecycle state (unchanged)
// so the UI's mute icon updates without waiting for some other transition.
func (c *callSession) setMuted(muted bool) {
	c.mu.Lock()
	c.muted = muted
	state := c.state
	c.mu.Unlock()
	c.broadcastState(state, "")
}

// startQualitySampler begins polling GetStats() every qualitySampleInterval
// while the call is connected, updating c.quality and broadcasting it
// alongside the unchanged "connected" state. Stopped by stopQualitySampler
// from end() (or if the call ends before ever connecting, it's simply never
// started).
func (c *callSession) startQualitySampler() {
	c.mu.Lock()
	if c.qualityTicker != nil {
		c.mu.Unlock()
		return
	}
	ticker := time.NewTicker(qualitySampleInterval)
	done := make(chan struct{})
	c.qualityTicker = ticker
	c.qualityDone = done
	c.mu.Unlock()

	go func() {
		for {
			select {
			case <-done:
				return
			case <-c.done:
				return
			case <-ticker.C:
				c.sampleQuality()
			}
		}
	}()
}

func (c *callSession) stopQualitySampler() {
	c.mu.Lock()
	ticker, done := c.qualityTicker, c.qualityDone
	c.qualityTicker, c.qualityDone = nil, nil
	c.mu.Unlock()
	if ticker != nil {
		ticker.Stop()
	}
	if done != nil {
		close(done)
	}
}

// sampleQuality reads the peer connection's current stats and, if it
// produced anything, recomputes and broadcasts the quality bucket.
func (c *callSession) sampleQuality() {
	c.mu.Lock()
	pc, state := c.pc, c.state
	c.mu.Unlock()
	if pc == nil || state != callConnected {
		return
	}
	lossPct, rttMs := pc.Stats()
	bucket := qualityBucket(lossPct, rttMs)

	c.mu.Lock()
	c.quality = bucket
	c.mu.Unlock()
	c.broadcastState(callConnected, "")
}

// callSID generates a Jingle session ID: 128 random bits, hex-encoded, same
// shape as the stanza IDs xmpp.randomID produces.
func callSID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("kage-call-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
