package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/jim-ww/kage/call"
	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/xmpp"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media/samplebuilder"
)

// recoverAndLog runs f, logging a panic instead of crashing the daemon: it's
// the only panic boundary of the detached call goroutines. what names the
// goroutine in the log.
func recoverAndLog(sid, what string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic in call goroutine", "sid", sid, "what", what, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	f()
}

// callState is the lifecycle of an account's one call. No call waiting or
// hold.
type callState int

const (
	callIdle          callState = iota
	callProposing               // outgoing: XEP-0353 propose sent, nothing back yet
	callRingingRemote           // outgoing: peer's device is alerting them
	callRingingLocal            // incoming: we're alerting the user, awaiting CallAnswer/CallReject
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

// callSession is one call: its Jingle signaling state and media pipeline
// (pion peer connection, mic and speaker, Opus).
type callSession struct {
	srv        *ipc.Server
	accountIdx int
	sess       *accountSession
	client     *xmpp.Client

	// peer is the contact's bare JID; remoteJID the full JID every Jingle IQ
	// goes to, learned from the peer's first directed stanza.
	peer      string
	remoteJID string
	sid       string
	incoming  bool

	// ringingFrom is every peer resource that sent <ringing/>: a <propose/>
	// rings all their devices, and a bare-JID <retract/> doesn't reach every
	// one on every server, so it's also sent to each of these. Guarded by mu.
	ringingFrom []string

	mu    sync.Mutex
	state callState
	pc    *call.PeerConnection
	mic   *call.Mic
	spk   *call.Speaker
	enc   *call.Encoder
	dec   *call.Decoder

	// micStop/micDone stop and await the current mic's pump goroutine (see
	// switchMic) - nil while there's no mic.
	micStop chan struct{}
	micDone chan struct{}

	// desktop captures other applications' audio, mixed into the mic, while
	// sharing the screen with desktop audio on (see syncDesktopAudio).
	desktop *call.DesktopAudio

	// contentName/ufrag/pwd identify our half of the ICE session, repeated on
	// every transport-info we trickle out.
	contentName string
	ufrag       string
	pwd         string

	// signalingReady is set once our session-initiate/-accept is sent; until
	// then local candidates queue in pendingLocal.
	signalingReady bool
	pendingLocal   []xmpp.ICECandidate

	// pendingRemote holds candidates that arrived by transport-info before we
	// had applied the peer's description - AddICECandidate rejects those.
	pendingRemote []xmpp.ICECandidate
	remoteSet     bool

	mediaStarted bool
	// connectedAt is when the call first connected, for its duration.
	connectedAt time.Time
	done        chan struct{}
	closeOnce   sync.Once

	// ICE restart (XEP-0176 §6.1), driven only by the initiator. restarting
	// keeps to one attempt at a time; disconnectTimer gives a Disconnected
	// state, often transient, iceDisconnectGrace to recover first.
	restarting      bool
	restartAttempts int
	disconnectTimer *time.Timer

	// muted drops captured mic frames. quality is the last sampled quality
	// bucket (see sampleQuality), "" until the first sample.
	muted         bool
	quality       string
	qualityTicker *time.Ticker
	qualityDone   chan struct{}

	// sharing is set while we send our own video, captured by screenShare.
	// pendingShare is set from sending a content-add offering video until the
	// peer accepts it: capture only starts then (see applyContentAccept).
	// videoNegotiated is set once the peer accepted our video content: it
	// stays for the rest of the call, so sharing again just resumes sending
	// on it (see withdrawVideoShare).
	sharing         bool
	pendingShare    bool
	videoNegotiated bool
	// pendingShareContent is the content our pending content-add offered,
	// for applyContentReject to settle our offer with.
	pendingShareContent xmpp.JingleContent
	// videoUseCamera is the source startVideoShare was asked for, the camera
	// or the screen, for beginScreenShareCapture.
	videoUseCamera bool
	videoSource    call.VideoSource

	// remoteVideo is set while the peer streams video to us. viewerDismissed
	// is set when its window is closed by hand: the track is still drained,
	// but no window reopens until reopenRemoteVideo.
	remoteVideo     bool
	viewerDismissed bool

	// remoteVideoTrack is the incoming video track playRemoteVideo reads, for
	// reopenRemoteVideo to ask a keyframe on.
	remoteVideoTrack *webrtc.TrackRemote

	// remoteVideoViewer is the window showing the peer's video, for
	// applyContentRemove to close: once the content is withdrawn no frame
	// arrives that would make the read loop notice.
	remoteVideoViewer *call.ScreenViewer

	// The two ends' DTLS-SRTP fingerprints (XEP-0320), the short
	// authentication string from both, and whether the peer's differs from
	// the one pinned before - against a signaling server swapping them to
	// intercept the call (see call_fingerprint.go). Set once negotiated.
	localFingerprint   string
	remoteFingerprint  string
	fingerprintSAS     string
	fingerprintChanged bool

	// autoStartVideo makes onConnectionState start our video once the call
	// connects - a call started as a video call.
	autoStartVideo          bool
	autoStartVideoUseCamera bool

	// remoteContents is every Jingle content the peer described so far. A
	// content-add carries only the new one, so it's merged onto these into a
	// full SDP: a partial one reads as the others removed.
	remoteContents []xmpp.JingleContent

	// ring is the ringback or ringtone while the call rings.
	ring *call.RingTone
}

// Ringback (outgoing) and ringtone (incoming) sound different: each a
// two-note chord, the ringtone in a double pulse.
var (
	ringbackFreqsHz = []float64{480, 600}
	ringbackPattern = []time.Duration{time.Second, 3 * time.Second}

	ringtoneFreqsHz = []float64{523.25, 659.25} // C5 + E5
	ringtonePattern = []time.Duration{
		400 * time.Millisecond, 200 * time.Millisecond,
		400 * time.Millisecond, 1800 * time.Millisecond,
	}
)

// startRing plays a tone pattern, if none is playing, on its own speaker
// (see call.RingTone).
func (c *callSession) startRing(freqsHz []float64, pattern []time.Duration) {
	c.mu.Lock()
	if c.ring != nil {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	_, output := currentAudioDevices()
	r, err := call.NewRingTone(output, freqsHz, pattern)
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

// qualitySampleInterval is how often a call's ICE/RTP stats are sampled.
const qualitySampleInterval = 3 * time.Second

// Packet loss and RTT limits of the "good" and "fair" call quality; past
// them it's "poor". Round numbers, not tuned.
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

// iceDisconnectGrace is how long a Disconnected state may recover by itself
// before ICE is restarted.
const iceDisconnectGrace = 5 * time.Second

// --- accountSession call slot -------------------------------------------
//
// callMu guards only the pointer, each callSession's mu its state. Lock
// order is callMu, then callSession.mu.

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
	_, err := s.doStartCall(ctx, srv, accountIdx, to, false, false)
	return err
}

// startVideoCall is startCall, sending our camera or screen video from when
// the call connects.
func (s *accountSession) startVideoCall(ctx context.Context, srv *ipc.Server, accountIdx int, to string, useCamera bool) error {
	_, err := s.doStartCall(ctx, srv, accountIdx, to, true, useCamera)
	return err
}

func (s *accountSession) doStartCall(ctx context.Context, srv *ipc.Server, accountIdx int, to string, autoVideo, autoVideoUseCamera bool) (*callSession, error) {
	client, err := s.liveClient()
	if err != nil {
		return nil, err
	}

	s.callMu.Lock()
	if s.call != nil {
		s.callMu.Unlock()
		return nil, fmt.Errorf("a call is already in progress")
	}
	c := &callSession{
		srv: srv, accountIdx: accountIdx, sess: s, client: client,
		peer: bareJID(to), sid: callSID(), state: callProposing,
		contentName: jingleContentName, done: make(chan struct{}),
		autoStartVideo: autoVideo, autoStartVideoUseCamera: autoVideoUseCamera,
	}
	s.call = c
	s.callMu.Unlock()

	if err := client.ProposeCall(ctx, c.peer, c.sid, autoVideo); err != nil {
		c.end(ctx, "failed", err.Error())
		return nil, err
	}
	c.broadcastState(callProposing, "")
	return c, nil
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

// rejectAndNotifyOwnDevices sends a XEP-0353 <reject/> to the caller, and to
// our own bare JID and each of our other resources (see ownResources) so
// they stop ringing: not every server fans a bare-JID message out to all.
func (c *callSession) rejectAndNotifyOwnDevices(ctx context.Context, remote string) {
	if err := c.client.RejectCall(ctx, remote, c.sid); err != nil {
		slog.Warn("rejecting call", "sid", c.sid, "err", err)
	}
	bare := c.client.JID.Bare().String()
	if err := c.client.RejectCall(ctx, bare, c.sid); err != nil {
		slog.Warn("notifying own devices of declined call", "sid", c.sid, "err", err)
	}
	ownResource := c.client.JID.Resourcepart()
	all := c.sess.ownResources()
	slog.Debug("notifying own devices of decline", "sid", c.sid, "own_resource", ownResource, "known_resources", all)
	for _, res := range all {
		if res == ownResource || res == "" {
			continue
		}
		if err := c.client.RejectCall(ctx, bare+"/"+res, c.sid); err != nil {
			slog.Warn("notifying own device of declined call", "sid", c.sid, "resource", res, "err", err)
		}
	}
}

// retractFromAllRinging sends a XEP-0353 <retract/> to the peer's bare JID
// and to each resource that rang (see ringingFrom).
func (c *callSession) retractFromAllRinging(ctx context.Context) {
	if err := c.client.RetractCall(ctx, c.peer, c.sid); err != nil {
		slog.Warn("retracting call", "sid", c.sid, "err", err)
	}
	c.mu.Lock()
	ringingFrom := append([]string(nil), c.ringingFrom...)
	c.mu.Unlock()
	for _, full := range ringingFrom {
		if err := c.client.RetractCall(ctx, full, c.sid); err != nil {
			slog.Warn("retracting call to ringing resource", "sid", c.sid, "to", full, "err", err)
		}
	}
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
		c.retractFromAllRinging(ctx)
	case callRingingLocal:
		// Unanswered incoming call: no session yet, only the propose to
		// decline.
		c.rejectAndNotifyOwnDevices(ctx, remote)
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

// peerWentOffline ends the call if fullJID, the peer device it's with, went
// unavailable: it can't send a terminate or retract any more.
func (s *accountSession) peerWentOffline(ctx context.Context, fullJID string) {
	c := s.currentCall()
	if c == nil {
		return
	}
	c.mu.Lock()
	match := c.remoteJID != "" && c.remoteJID == fullJID
	c.mu.Unlock()
	if match {
		slog.Debug("call peer went offline", "sid", c.sid, "peer", fullJID)
		c.end(ctx, "ended", "peer went offline")
	}
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

	switch {
	case state == callRingingLocal:
		c.rejectAndNotifyOwnDevices(ctx, remote)
	case state == callProposing || state == callRingingRemote:
		// Our outgoing call, no session yet: retract, as hangupCall does.
		c.retractFromAllRinging(ctx)
	case remote != "":
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

// setScreenShare starts or stops sending our screen or camera video.
func (s *accountSession) setScreenShare(sharing, useCamera bool) error {
	c := s.currentCall()
	if c == nil {
		return fmt.Errorf("no call in progress")
	}
	if sharing {
		return c.startVideoShare(useCamera)
	}
	c.withdrawVideoShare(context.Background())
	return nil
}

// reopenVideo reopens the call's incoming video, see reopenRemoteVideo.
func (s *accountSession) reopenVideo() error {
	c := s.currentCall()
	if c == nil {
		return fmt.Errorf("no call in progress")
	}
	return c.reopenRemoteVideo()
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
		// A callee gets its own <ringing/> back as a carbon (XEP-0280); it
		// mustn't turn its ringing-local into ringing-remote.
		if bareJID(ev.From) == s.account.JID {
			return
		}
		c.mu.Lock()
		// See ringingFrom.
		alreadyTracked := false
		for _, from := range c.ringingFrom {
			if from == ev.From {
				alreadyTracked = true
				break
			}
		}
		if !alreadyTracked {
			c.ringingFrom = append(c.ringingFrom, ev.From)
		}
		wasProposing := c.state == callProposing
		if wasProposing {
			c.state = callRingingRemote
		}
		c.mu.Unlock()
		if !wasProposing {
			return
		}
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
		// The caller ends on any <reject/>. A callee gets the one another of
		// its devices sent (see rejectAndNotifyOwnDevices), and ends only if
		// still ringing, not if this device answered.
		c.mu.Lock()
		stillRinging := c.incoming && c.state == callRingingLocal
		isCaller := !c.incoming
		c.mu.Unlock()
		if isCaller || stillRinging {
			c.end(ctx, "ended", "declined")
		}

	case xmpp.JMIRetract:
		c.end(ctx, "ended", "caller hung up")

	case xmpp.JMIAccept:
		// <accept/> goes to our own bare JID, so this device gets its own
		// back too, with no reliable "from" to tell. answerCall leaves
		// callRingingLocal before sending it, so still ringing means another
		// device answered.
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
// CallAnswer/CallReject action.
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
		// Busy: decline, and tell our other devices (see
		// rejectAndNotifyOwnDevices). The TUI shows it as missed.
		if err := client.RejectCall(ctx, ev.From, ev.SID); err != nil {
			slog.Warn("rejecting call while busy", "sid", ev.SID, "err", err)
		}
		bare := client.JID.Bare().String()
		if err := client.RejectCall(ctx, bare, ev.SID); err != nil {
			slog.Warn("notifying own devices of busy decline", "sid", ev.SID, "err", err)
		}
		ownResource := client.JID.Resourcepart()
		for _, res := range s.ownResources() {
			if res == ownResource || res == "" {
				continue
			}
			if err := client.RejectCall(ctx, bare+"/"+res, ev.SID); err != nil {
				slog.Warn("notifying own device of busy decline", "sid", ev.SID, "resource", res, "err", err)
			}
		}
		broadcast(srv, ui.MissedCallMsg{AccountIdx: accountIdx, From: from, SID: ev.SID})
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
	slog.Debug("incoming call proposed", "sid", ev.SID, "from", from, "accountIdx", accountIdx)
	broadcast(srv, ui.IncomingCallMsg{
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

	case xmpp.JingleActionContentAdd:
		c.applyContentAdd(ctx, ev.Jingle)

	case xmpp.JingleActionContentAccept:
		c.applyContentAccept(ctx, ev.Jingle)

	case xmpp.JingleActionContentReject:
		c.applyContentReject()

	case xmpp.JingleActionContentRemove:
		c.applyContentRemove(ev.Jingle)

	case xmpp.JingleActionSessionInfo:
		if ev.Jingle.Mute != nil {
			c.applyVideoMute(ev.Jingle.Mute.Name)
		}

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

// setupPeer creates the peer connection with every callback registered
// before any description is applied: OnTrack set later misses the remote
// audio track.
func (c *callSession) setupPeer(ctx context.Context) error {
	pc, err := call.NewPeerConnection()
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.pc = pc
	c.mu.Unlock()

	pc.OnTrack(func(track *webrtc.TrackRemote) {
		if track.Kind() == webrtc.RTPCodecTypeVideo {
			go recoverAndLog(c.sid, "playRemoteVideo", func() { c.playRemoteVideo(pc, track) })
			return
		}
		go recoverAndLog(c.sid, "playRemote", func() { c.playRemote(track) })
	})
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		// Run off pion's own goroutine: both branches take c.mu and one of
		// them tears the connection down.
		go recoverAndLog(c.sid, "onConnectionState", func() { c.onConnectionState(ctx, state) })
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

	c.mu.Lock()
	autoVideo, autoCamera := c.autoStartVideo, c.autoStartVideoUseCamera
	c.mu.Unlock()
	// A video call offers its video in the first offer, not as a later
	// content-add: Conversations answers a content-add with an ICE restart
	// that never completes, losing the video.
	if autoVideo {
		if err := c.pc.AddVideoTrack(); err != nil {
			return fmt.Errorf("adding video track: %w", err)
		}
		c.mu.Lock()
		c.videoUseCamera = autoCamera
		// Consumed, or onConnectionState would add a second video m-line.
		c.autoStartVideo = false
		c.mu.Unlock()
	}

	offer, err := c.pc.CreateOffer()
	if err != nil {
		return err
	}
	contents, err := jingleContentsFromSDP(offer, !c.incoming)
	if err != nil {
		return err
	}
	// Stashed for applyAnswer, once the callee's session-accept carries
	// their half of the fingerprint comparison (see checkFingerprints).
	c.mu.Lock()
	c.localFingerprint = firstFingerprint(contents)
	c.mu.Unlock()

	c.mu.Lock()
	remote := c.remoteJID
	c.mu.Unlock()
	if err := c.client.SendSessionInitiate(ctx, remote, xmpp.JingleIQ{SID: c.sid, Contents: contents}); err != nil {
		return err
	}
	// Only once session-initiate is sent, see rememberTransport.
	c.rememberTransport(contents)
	c.flushLocalCandidates(ctx)
	return nil
}

// acceptSession is the incoming path: apply the caller's offer, answer it,
// and send that answer back as a session-accept.
func (c *callSession) acceptSession(ctx context.Context, jingle xmpp.JingleIQ) error {
	if err := c.setupPeer(ctx); err != nil {
		return err
	}
	offer, err := sdpFromJingleContents(jingle.Contents, webrtc.SDPTypeOffer, c.incoming)
	if err != nil {
		return err
	}
	if err := c.pc.SetRemoteDescription(offer); err != nil {
		return err
	}
	c.markRemoteSet()

	// A video call offering video both ways, as Conversations and Dino do,
	// gets our video on that same content: they reject a content-add of
	// our own once the call has video. Capture starts once we share.
	if in, ok := firstContentOfKind(jingle.Contents, "video"); ok && canRespondSend(in.Senders) {
		if err := c.pc.AttachVideoTrack(); err != nil {
			slog.Debug("answering without our video", "sid", c.sid, "err", err)
		} else {
			c.mu.Lock()
			c.videoNegotiated = true
			c.mu.Unlock()
		}
	}

	answer, err := c.pc.CreateAnswer()
	if err != nil {
		return err
	}
	contents, err := jingleContentsFromSDP(answer, !c.incoming)
	if err != nil {
		return err
	}
	// Echo the initiator's content names so both ends agree on the mid,
	// matched by media kind, not position: the offer may list video first.
	for i := range contents {
		if contents[i].Description == nil {
			continue
		}
		if in, ok := firstContentOfKind(jingle.Contents, contents[i].Description.Media); ok && in.Name != "" {
			contents[i].Name = in.Name
		}
	}
	c.checkFingerprints(firstFingerprint(contents), firstFingerprint(jingle.Contents))
	c.broadcastState(c.currentState(), "")

	c.mu.Lock()
	c.remoteContents = jingle.Contents
	c.mu.Unlock()

	c.mu.Lock()
	c.state = callNegotiating
	remote := c.remoteJID
	c.mu.Unlock()

	if err := c.client.SendSessionAccept(ctx, remote, xmpp.JingleIQ{SID: c.sid, Contents: contents}); err != nil {
		return err
	}
	// Only once session-accept is sent, see rememberTransport.
	c.rememberTransport(contents)
	c.flushLocalCandidates(ctx)
	return nil
}

// canRespondSend reports whether an offered content's senders let us, the
// responder, send on it. No senders attribute means both (XEP-0166).
func canRespondSend(senders string) bool {
	return senders == "" || senders == "both" || senders == "responder"
}

// applyAnswer is the outgoing path's final signaling step: the callee's
// session-accept becomes our remote description.
func (c *callSession) applyAnswer(ctx context.Context, jingle xmpp.JingleIQ) error {
	answer, err := sdpFromJingleContents(jingle.Contents, webrtc.SDPTypeAnswer, c.incoming)
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

	c.mu.Lock()
	c.remoteContents = jingle.Contents
	localFP := c.localFingerprint
	c.mu.Unlock()
	c.checkFingerprints(localFP, firstFingerprint(jingle.Contents))
	c.broadcastState(c.currentState(), "")

	// A video call's video was in the offer (see initiateSession): once the
	// answer accepts it, capture starts, as applyContentAccept does.
	if _, ok := firstContentOfKind(jingle.Contents, "video"); ok {
		c.mu.Lock()
		c.videoNegotiated = true
		c.mu.Unlock()
		c.beginScreenShareCapture(pc)
	}
	return nil
}

// checkFingerprints computes the SAS and checks remoteFP against the
// contact's pinned one (see checkAndPinCallFingerprint). No-op without both
// fingerprints, rather than a false "changed" alarm.
func (c *callSession) checkFingerprints(localFP, remoteFP string) {
	if localFP == "" || remoteFP == "" {
		return
	}
	sas := computeSAS(localFP, remoteFP)
	changed := checkAndPinCallFingerprint(context.Background(), c.sess.db, c.sess.account.JID, c.peer, remoteFP)
	c.mu.Lock()
	c.localFingerprint, c.remoteFingerprint = localFP, remoteFP
	c.fingerprintSAS, c.fingerprintChanged = sas, changed
	c.mu.Unlock()
	if changed {
		slog.Warn("call peer's DTLS fingerprint differs from the previously pinned one", "sid", c.sid, "peer", c.peer)
	}
}

// rememberTransport records our ICE credentials and content name for trickled
// transport-info IQs, and sets signalingReady, letting sendCandidate (on
// pion's goroutine) send. Call it only after the stanza establishing these
// credentials is sent: peers silently drop a candidate for a session they
// don't know yet, which can leave ICE stuck.
func (c *callSession) rememberTransport(contents []xmpp.JingleContent) {
	// Bundled, so every content shares the credentials, but candidates must
	// be labelled with the bundle-tag content: the first m= section (RFC
	// 8843), which is contents[0]. Conversations attributes a candidate only
	// to the content it names.
	if len(contents) == 0 || contents[0].Transport == nil {
		return
	}
	content := contents[0]
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
	slog.Debug("peer connection state changed", "sid", c.sid, "state", state.String())
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

		c.mu.Lock()
		autoVideo, autoCamera := c.autoStartVideo, c.autoStartVideoUseCamera
		c.autoStartVideo = false
		c.mu.Unlock()
		if autoVideo {
			if err := c.startVideoShare(autoCamera); err != nil {
				slog.Warn("auto-starting video", "sid", c.sid, "camera", autoCamera, "err", err)
				c.broadcastNotice(fmt.Sprintf("couldn't start %s: %v", videoSourceLabel(autoCamera), err))
			}
		}
	case webrtc.PeerConnectionStateFailed:
		c.handleICEFailure(ctx)
	case webrtc.PeerConnectionStateDisconnected:
		c.scheduleDisconnectCheck(ctx)
	case webrtc.PeerConnectionStateClosed:
		// Also how a normal hangup by the peer looks: their connection closes
		// before their session-terminate arrives. So not "connection lost".
		c.end(ctx, "ended", "call ended")
	}
}

// handleICEFailure reacts to a failed connection, or one still disconnected
// past its grace period: ending the call if it never connected or restarts
// ran out, else restarting ICE if we're the initiator. The responder waits
// for the initiator's transport-replace.
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

// scheduleDisconnectCheck re-checks the connection after iceDisconnectGrace.
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

// attemptICERestart sends a transport-replace with fresh ICE credentials
// (XEP-0176 §6.1), which the peer's transport-accept completes. If it can't
// be sent, scheduleRestartRetry tries again.
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
	contents, err := jingleContentsFromSDP(offer, !c.incoming)
	if err != nil || len(contents) == 0 {
		slog.Warn("building transport-replace from restart offer", "sid", c.sid, "attempt", attempt, "err", err)
		c.scheduleRestartRetry(ctx, attempt)
		return
	}
	// The bundle-tag content (first m-line, whichever kind it is) - see
	// rememberTransport's doc for why this can't be hardcoded to "audio".
	content := contents[0]
	replaceContent := xmpp.JingleContent{Creator: "initiator", Name: content.Name, Transport: content.Transport}
	if err := c.client.SendTransportReplace(ctx, remote, c.sid, replaceContent); err != nil {
		slog.Warn("sending transport-replace", "sid", c.sid, "attempt", attempt, "err", err)
		c.scheduleRestartRetry(ctx, attempt)
		return
	}
	// Only once transport-replace is sent, see rememberTransport.
	c.rememberTransport(contents)
	c.flushLocalCandidates(ctx)
}

// scheduleRestartRetry backs off, then restarts ICE again unless the call
// recovered or ended.
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

// applyTransportReplace is the responder side of an ICE restart: pion
// restarts ICE on the offer's new credentials, and we answer with
// transport-accept, or transport-reject for a malformed replace.
func (c *callSession) applyTransportReplace(ctx context.Context, jingle xmpp.JingleIQ) {
	c.mu.Lock()
	pc, remote, state := c.pc, c.remoteJID, c.state
	c.mu.Unlock()
	if pc == nil || state != callConnected {
		return
	}

	offer, err := sdpFromJingleContents(jingle.Contents, webrtc.SDPTypeOffer, c.incoming)
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
	contents, err := jingleContentsFromSDP(answer, !c.incoming)
	if err != nil || len(contents) == 0 {
		slog.Warn("building transport-accept from restart answer", "sid", c.sid, "err", err)
		return
	}
	// The bundle-tag content (first m-line, whichever kind it is) - see
	// rememberTransport's doc for why this can't be hardcoded to "audio".
	content := contents[0]
	acceptContent := xmpp.JingleContent{Creator: "initiator", Name: content.Name, Transport: content.Transport}
	if err := c.client.SendTransportAccept(ctx, remote, c.sid, acceptContent); err != nil {
		slog.Warn("sending transport-accept", "sid", c.sid, "err", err)
	}
	// Only once transport-accept is sent, see rememberTransport.
	c.rememberTransport(contents)
	c.flushLocalCandidates(ctx)
}

// applyTransportAccept completes our ICE restart with the peer's answer.
func (c *callSession) applyTransportAccept(jingle xmpp.JingleIQ) {
	c.mu.Lock()
	pc := c.pc
	c.mu.Unlock()
	if pc == nil {
		return
	}
	answer, err := sdpFromJingleContents(jingle.Contents, webrtc.SDPTypeAnswer, c.incoming)
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

// startMedia opens the audio devices and sends the mic. Receiving starts in
// OnTrack. Without a mic, the call goes on without it.
func (c *callSession) startMedia() error {
	enc, err := call.NewEncoder()
	if err != nil {
		return err
	}
	dec, err := call.NewDecoder()
	if err != nil {
		return err
	}
	input, output := currentAudioDevices()
	spk, err := call.NewSpeaker(output)
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.enc, c.dec, c.spk = enc, dec, spk
	c.mu.Unlock()

	if spk.UsingDefaultInstead() {
		slog.Info("picked speaker not connected, using the default", "sid", c.sid, "device", output)
		c.broadcastNotice(fmt.Sprintf("speaker %q isn't connected - using the default", output))
	}
	if err := c.switchMic(input); err != nil {
		slog.Warn("opening capture device, proceeding without mic", "sid", c.sid, "err", err)
		c.broadcastNotice(fmt.Sprintf("no microphone - they can't hear you: %v", err))
	}
	return nil
}

// switchMic makes the named device the call's mic. The old pump is awaited
// before the new one starts: both use the one Opus encoder.
func (c *callSession) switchMic(device string) error {
	mic, err := call.NewMic(device)
	if err != nil {
		return err
	}
	if mic.UsingDefaultInstead() {
		slog.Info("picked microphone not connected, using the default", "sid", c.sid, "device", device)
		c.broadcastNotice(fmt.Sprintf("microphone %q isn't connected - using the default", device))
	}
	stop, done := make(chan struct{}), make(chan struct{})
	c.mu.Lock()
	if c.state == callEnded {
		c.mu.Unlock()
		mic.Close()
		return nil
	}
	old, oldStop, oldDone := c.mic, c.micStop, c.micDone
	c.mic, c.micStop, c.micDone = mic, stop, done
	pc, enc := c.pc, c.enc
	c.mu.Unlock()

	if oldStop != nil {
		close(oldStop)
		<-oldDone
	}
	if old != nil {
		old.Close()
	}
	go recoverAndLog(c.sid, "micPump", func() {
		defer close(done)
		c.pumpMic(mic, stop, enc, pc)
	})
	return nil
}

// pumpMic encodes and sends mic's frames until stop or the call ends,
// mixing in desktop audio while there is any (see syncDesktopAudio) - the
// mic's clock paces both, so a call whose mic failed to open sends no
// desktop audio either. Muting silences only the mic: shared desktop audio
// keeps going. With desktop audio mixed in, the encoder is retuned for
// music.
func (c *callSession) pumpMic(mic *call.Mic, stop <-chan struct{}, enc *call.Encoder, pc *call.PeerConnection) {
	const frameDuration = call.FrameMillis * time.Millisecond
	music := false
	for {
		select {
		case <-c.done:
			return
		case <-stop:
			return
		case frame := <-mic.Frames():
			c.mu.Lock()
			muted, desktop := c.muted, c.desktop
			c.mu.Unlock()
			var extra []int16
			if desktop != nil {
				var ended bool
				extra, ended = nextDesktopFrame(desktop.Frames())
				if extra != nil {
					call.ScaleFrame(extra, int(desktopAudioVolume.Load()))
				}
				if ended {
					go c.desktopAudioEnded(desktop)
				}
			}
			switch {
			case muted && extra == nil:
				continue // drop the frame rather than encode+send while muted
			case muted:
				frame = extra
			case extra != nil:
				call.MixFrame(frame, extra)
			}
			if (desktop != nil) != music {
				music = desktop != nil
				if err := enc.SetMusic(music); err != nil {
					slog.Warn("retuning call audio encoder", "sid", c.sid, "music", music, "err", err)
				}
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
}

// nextDesktopFrame takes the next captured desktop audio frame without
// waiting for one, skipping ahead when frames have piled up (the two
// clocks drift) so the desktop audio stays close to real time. ended
// reports the capture has stopped (frames closed).
func nextDesktopFrame(frames <-chan []int16) (frame []int16, ended bool) {
	const maxBacklog = 3
	for {
		select {
		case next, ok := <-frames:
			if !ok {
				return frame, true
			}
			frame = next
			if len(frames) <= maxBacklog {
				return frame, false
			}
		default:
			return frame, false
		}
	}
}

// desktopAudioEnded drops a desktop audio capture that stopped on its own
// (PipeWire restarted, pw-record crashed) and tells the user, who'd
// otherwise keep sharing believing the audio goes along.
func (c *callSession) desktopAudioEnded(d *call.DesktopAudio) {
	c.mu.Lock()
	ours := c.desktop == d
	if ours {
		c.desktop = nil
	}
	c.mu.Unlock()
	if !ours {
		return
	}
	d.Close()
	c.broadcastNotice(fmt.Sprintf("desktop audio stopped: %v", d.Err()))
}

// syncDesktopAudio starts or stops capturing desktop audio to match whether
// it should be flowing right now: while we're sharing the screen (not the
// camera) and desktop audio is enabled. Called on every change to either.
func (c *callSession) syncDesktopAudio() {
	want := func() bool {
		return c.state == callConnected && c.sharing && !c.videoUseCamera && desktopAudioEnabled.Load()
	}
	c.mu.Lock()
	wanted, running := want(), c.desktop
	if !wanted && running != nil {
		c.desktop = nil
	}
	c.mu.Unlock()
	if wanted == (running != nil) {
		return
	}
	if !wanted {
		running.Close()
		c.broadcastUpdate()
		return
	}

	d, err := call.NewDesktopAudio()
	if err != nil {
		slog.Warn("starting desktop audio", "sid", c.sid, "err", err)
		c.broadcastNotice(fmt.Sprintf("no desktop audio: %v", err))
		return
	}
	c.mu.Lock()
	if c.desktop != nil || !want() {
		c.mu.Unlock()
		d.Close()
		return
	}
	c.desktop = d
	c.mu.Unlock()
	c.broadcastUpdate()
}

// switchSpeaker moves the call's playback to the named output device.
// playRemote picks the new speaker up on its next packet.
func (c *callSession) switchSpeaker(device string) error {
	spk, err := call.NewSpeaker(device)
	if err != nil {
		return err
	}
	if spk.UsingDefaultInstead() {
		slog.Info("picked speaker not connected, using the default", "sid", c.sid, "device", device)
		c.broadcastNotice(fmt.Sprintf("speaker %q isn't connected - using the default", device))
	}
	c.mu.Lock()
	if c.state == callEnded || c.spk == nil {
		c.mu.Unlock()
		spk.Close()
		return nil
	}
	old := c.spk
	c.spk = spk
	c.mu.Unlock()
	old.Close()
	return nil
}

// setAudioDevices moves a call's running media to the given devices,
// reopening only what changed.
func (c *callSession) setAudioDevices(input, output string, inputChanged, outputChanged bool) error {
	c.mu.Lock()
	started := c.spk != nil
	c.mu.Unlock()
	if !started {
		return nil
	}
	if outputChanged {
		if err := c.switchSpeaker(output); err != nil {
			return fmt.Errorf("switching speaker: %w", err)
		}
	}
	if inputChanged {
		if err := c.switchMic(input); err != nil {
			return fmt.Errorf("switching microphone: %w", err)
		}
	}
	return nil
}

// playRemote plays the peer's audio track. Packets arriving before startMedia
// opened the speaker are dropped.
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
			c.mu.Lock()
			replaced := c.spk != nil && c.spk != spk
			c.mu.Unlock()
			if replaced {
				continue // switchSpeaker closed the old one under us
			}
			return
		}
	}
}

// currentState returns c.state under lock.
func (c *callSession) currentState() callState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// startVideoShare adds a video track and offers it with a XEP-0166
// content-add. Capture starts once the peer accepts (applyContentAccept), so
// the m-line is never negotiated idle (see call.NewPeerConnection). Already
// sending, or offering, it switches the source (switchVideoSource). Our
// video gets its own content and mid, apart from any the peer sends us.
func (c *callSession) startVideoShare(useCamera bool) error {
	c.mu.Lock()
	if c.pendingShare {
		// Capture hasn't started yet - beginScreenShareCapture reads the
		// source once the peer's content-accept lands.
		c.videoUseCamera = useCamera
		c.mu.Unlock()
		c.broadcastUpdate()
		return nil
	}
	if c.sharing {
		pc, same := c.pc, c.videoUseCamera == useCamera
		c.mu.Unlock()
		if !same && pc != nil {
			c.switchVideoSource(pc, useCamera)
		}
		return nil
	}
	pc, state, remote, negotiated := c.pc, c.state, c.remoteJID, c.videoNegotiated
	c.videoUseCamera = useCamera
	c.mu.Unlock()
	if pc == nil || state != callConnected {
		return fmt.Errorf("call is not connected")
	}
	if negotiated {
		slog.Debug("screen share: resuming on the negotiated video content", "sid", c.sid, "camera", useCamera)
		c.sendVideoMute(context.Background(), false)
		c.beginScreenShareCapture(pc)
		return nil
	}

	slog.Debug("screen share: starting", "sid", c.sid, "camera", useCamera)
	if err := pc.AddVideoTrack(); err != nil {
		return fmt.Errorf("adding video track: %w", err)
	}
	offer, err := pc.CreateOffer()
	if err != nil {
		return fmt.Errorf("creating content-add offer: %w", err)
	}

	contents, err := jingleContentsFromSDP(offer, !c.incoming)
	if err != nil {
		return fmt.Errorf("building content-add: %w", err)
	}
	// By mid, not kind: the peer's own video content may exist already, and
	// re-announcing it makes peers end the call (see VideoMid).
	videoMid := pc.VideoMid()
	var videoContent xmpp.JingleContent
	var ok bool
	for _, ct := range contents {
		if ct.Description != nil && ct.Description.Media == "video" && ct.Name == videoMid {
			videoContent, ok = ct, true
			break
		}
	}
	if !ok {
		return fmt.Errorf("no video content in renegotiation offer")
	}
	slog.Debug("screen share: sending content-add", "sid", c.sid, "to", remote, "content_name", videoContent.Name, "payload_types", len(videoContent.Description.PayloadTypes))

	c.mu.Lock()
	c.pendingShare = true
	c.pendingShareContent = videoContent
	c.mu.Unlock()
	c.broadcastUpdate()

	if err := c.client.SendContentAdd(context.Background(), remote, c.sid, videoContent); err != nil {
		c.mu.Lock()
		c.pendingShare = false
		c.mu.Unlock()
		c.broadcastUpdate()
		return fmt.Errorf("sending content-add: %w", err)
	}
	return nil
}

// applyContentAdd accepts a peer's new content, typically their video: the
// full offer (merged onto remoteContents) is applied and answered with
// content-accept. With no local track, pion answers recvonly.
func (c *callSession) applyContentAdd(ctx context.Context, jingle xmpp.JingleIQ) {
	slog.Debug("screen share: received content-add", "sid", c.sid, "contents", len(jingle.Contents))
	c.mu.Lock()
	pc, remote, merged := c.pc, c.remoteJID, append(append([]xmpp.JingleContent(nil), c.remoteContents...), jingle.Contents...)
	c.mu.Unlock()
	if pc == nil {
		slog.Warn("screen share: content-add with no peer connection", "sid", c.sid)
		return
	}

	offer, err := sdpFromJingleContents(merged, webrtc.SDPTypeOffer, c.incoming)
	if err != nil {
		slog.Warn("parsing content-add", "sid", c.sid, "err", err)
		return
	}
	if err := pc.SetRemoteDescription(offer); err != nil {
		slog.Warn("applying content-add", "sid", c.sid, "err", err)
		return
	}

	c.mu.Lock()
	c.remoteContents = merged
	c.mu.Unlock()

	answer, err := pc.CreateAnswer()
	if err != nil {
		slog.Warn("answering content-add", "sid", c.sid, "err", err)
		return
	}

	answerContents, err := jingleContentsFromSDP(answer, !c.incoming)
	if err != nil {
		slog.Warn("building content-accept", "sid", c.sid, "err", err)
		return
	}
	videoAnswer, ok := firstContentOfKind(answerContents, "video")
	if !ok {
		slog.Warn("no video content in content-add answer", "sid", c.sid)
		return
	}
	// Echo the peer's content name so both ends agree on the mid.
	if in, ok := firstContentOfKind(jingle.Contents, "video"); ok && in.Name != "" {
		videoAnswer.Name = in.Name
	}
	if err := c.client.SendContentAccept(ctx, remote, c.sid, videoAnswer); err != nil {
		slog.Warn("sending content-accept", "sid", c.sid, "err", err)
		return
	}
	slog.Debug("screen share: content-accept sent, waiting for remote video track", "sid", c.sid, "content_name", videoAnswer.Name)
	slog.Debug("screen share: transceiver state after content-add", "sid", c.sid, "transceivers", pc.DebugTransceivers())
}

// applyContentReject handles the peer turning down our video content-add,
// as Conversations does in a call that already has video. pion can't roll
// our offer back, so it's settled with an answer of our own making that
// takes the content as senders=none, as Conversations does: the call is
// back to stable, and sharing can be offered again.
func (c *callSession) applyContentReject() {
	c.mu.Lock()
	pending, pc, useCamera, offered := c.pendingShare, c.pc, c.videoUseCamera, c.pendingShareContent
	remoteContents := c.remoteContents
	c.pendingShare = false
	c.mu.Unlock()
	if !pending {
		slog.Debug("screen share: content-reject but no share was pending, ignoring", "sid", c.sid)
		return
	}
	slog.Debug("screen share: peer rejected our content-add", "sid", c.sid)
	if pc != nil {
		if err := settleRejectedContent(pc, remoteContents, offered, c.incoming); err != nil {
			slog.Warn("screen share: settling rejected content-add", "sid", c.sid, "err", err)
		}
	}
	c.broadcastUpdate()
	c.broadcastNotice(fmt.Sprintf("they declined your %s", videoSourceLabel(useCamera)))
}

// settleRejectedContent answers our pending offer with the peer's
// negotiated contents plus offered, rejected, as senders=none on the peer's
// transport (bundled, it's the same for every content).
func settleRejectedContent(pc *call.PeerConnection, remoteContents []xmpp.JingleContent, offered xmpp.JingleContent, incoming bool) error {
	var transport *xmpp.ICEUDPTransport
	for _, rc := range remoteContents {
		if rc.Transport != nil && rc.Transport.Fingerprint != nil {
			t := *rc.Transport
			t.Candidates = nil
			transport = &t
			break
		}
	}
	if transport == nil || offered.Description == nil {
		return fmt.Errorf("nothing to answer the rejected content with")
	}
	desc := *offered.Description
	desc.Sources = nil
	rejected := xmpp.JingleContent{Creator: offered.Creator, Name: offered.Name, Senders: "none", Description: &desc, Transport: transport}
	merged := append(append([]xmpp.JingleContent(nil), remoteContents...), rejected)
	answer, err := sdpFromJingleContents(merged, webrtc.SDPTypeAnswer, incoming)
	if err != nil {
		return err
	}
	return pc.SetRemoteDescription(answer)
}

// applyContentAccept applies the peer's answer to our content-add and starts
// capture (see beginScreenShareCapture).
func (c *callSession) applyContentAccept(ctx context.Context, jingle xmpp.JingleIQ) {
	slog.Debug("screen share: received content-accept", "sid", c.sid, "contents", len(jingle.Contents))
	c.mu.Lock()
	if !c.pendingShare {
		c.mu.Unlock()
		slog.Debug("screen share: content-accept but no share was pending, ignoring", "sid", c.sid)
		return
	}
	pc, merged := c.pc, append(append([]xmpp.JingleContent(nil), c.remoteContents...), jingle.Contents...)
	c.mu.Unlock()
	if pc == nil {
		return
	}

	answer, err := sdpFromJingleContents(merged, webrtc.SDPTypeAnswer, c.incoming)
	if err != nil {
		slog.Warn("parsing content-accept", "sid", c.sid, "err", err)
		return
	}
	if err := pc.SetRemoteDescription(answer); err != nil {
		slog.Warn("applying content-accept", "sid", c.sid, "err", err)
		return
	}

	c.mu.Lock()
	c.remoteContents = merged
	c.pendingShare = false
	c.videoNegotiated = true
	c.mu.Unlock()

	slog.Debug("screen share: content-add negotiation complete, starting capture", "sid", c.sid)
	slog.Debug("screen share: transceiver state after content-accept", "sid", c.sid, "transceivers", pc.DebugTransceivers())
	c.beginScreenShareCapture(pc)
}

// videoSourceCamera/videoSourceScreen are ui.CallStateMsg.Video's values.
const (
	videoSourceCamera = "camera"
	videoSourceScreen = "screen"
)

// videoSourceLabel names a video source in messages shown to the user.
func videoSourceLabel(useCamera bool) string {
	if useCamera {
		return "camera"
	}
	return "screen share"
}

// defaultCameraDevice is the camera kage uses; there's no picking another.
const defaultCameraDevice = "/dev/video0"

// beginScreenShareCapture starts capturing the screen or camera onto the
// negotiated video track.
func (c *callSession) beginScreenShareCapture(pc *call.PeerConnection) {
	c.mu.Lock()
	useCamera := c.videoUseCamera
	c.mu.Unlock()

	quality := currentVideoQuality()
	var share call.VideoSource
	var err error
	if useCamera {
		share, err = call.NewCamera(defaultCameraDevice, quality, call.VideoCodecH264)
	} else {
		share, err = call.NewScreenShare(currentScreenRecorder(), quality, call.VideoCodecH264)
	}
	if err != nil {
		slog.Warn("starting video capture", "sid", c.sid, "camera", useCamera, "err", err)
		c.stopScreenShare()
		c.sendVideoMute(context.Background(), true)
		c.broadcastNotice(fmt.Sprintf("couldn't start %s: %v", videoSourceLabel(useCamera), err))
		return
	}
	slog.Debug("screen share: capture started", "sid", c.sid, "camera", useCamera, "video_sender_ssrc", pc.VideoSenderSSRC())

	c.mu.Lock()
	c.videoSource = share
	c.sharing = true
	c.mu.Unlock()
	c.syncDesktopAudio()

	go recoverAndLog(c.sid, "screenShareCapture", func() {
		err := share.Run(func(frame []byte, sinceLast time.Duration) error {
			return pc.WriteVideoSample(frame, sinceLast)
		})
		if err != nil {
			slog.Warn("screen share capture ended", "sid", c.sid, "err", err)
		}
		c.mu.Lock()
		stillOurs := c.videoSource == share
		c.mu.Unlock()
		if stillOurs {
			// Capture stopped by itself (the screen picker was closed, ...):
			// to the peer that's a stop like any other.
			c.withdrawVideoShare(context.Background())
			if err != nil {
				c.broadcastNotice(fmt.Sprintf("%s stopped: %v", videoSourceLabel(useCamera), err))
			}
		}
	})

	c.broadcastUpdate()
}

// switchVideoSource swaps camera and screen on our video track without
// renegotiating: both are the same H.264 stream, so the peer just sees a
// new keyframe.
func (c *callSession) switchVideoSource(pc *call.PeerConnection, useCamera bool) {
	c.mu.Lock()
	old := c.videoSource
	// Cleared before Stop, so the old capture's exit doesn't read as the
	// source dying and withdraw the content.
	c.videoSource = nil
	c.videoUseCamera = useCamera
	c.mu.Unlock()
	slog.Debug("screen share: switching source", "sid", c.sid, "camera", useCamera)
	if old != nil {
		old.Stop()
	}
	c.beginScreenShareCapture(pc)
}

// reopenRemoteVideo brings back the peer's video window after it was closed
// by hand, by asking for a keyframe for playRemoteVideo to reopen it on.
func (c *callSession) reopenRemoteVideo() error {
	c.mu.Lock()
	pc, track := c.pc, c.remoteVideoTrack
	if pc != nil && track != nil {
		c.viewerDismissed = false
	}
	c.mu.Unlock()
	if pc == nil || track == nil {
		return fmt.Errorf("no active incoming video to reopen")
	}
	return pc.SendPLI(track.SSRC())
}

// stopScreenShare stops the capture, if any, without telling the peer; see
// withdrawVideoShare for that.
func (c *callSession) stopScreenShare() {
	c.mu.Lock()
	share := c.videoSource
	c.videoSource = nil
	c.sharing = false
	c.mu.Unlock()
	if share != nil {
		share.Stop()
	}
	c.syncDesktopAudio()
	c.broadcastUpdate()
}

// withdrawVideoShare stops our video and tells the peer, or its viewer
// would sit on the last frame. A content-add not accepted yet is retracted
// with a content-remove; an accepted content stays, with a XEP-0167
// <mute/> naming it: Conversations only takes a content-remove as a
// retraction and ends the call on any other.
func (c *callSession) withdrawVideoShare(ctx context.Context) {
	c.mu.Lock()
	sharing, pending := c.sharing, c.pendingShare
	// Cleared here too so a stop pressed while the content-add is still in
	// flight can't have a late content-accept start the capture anyway.
	c.pendingShare = false
	c.mu.Unlock()
	if !sharing && !pending {
		return
	}

	c.stopScreenShare()
	if pending {
		c.removeVideoContent(ctx)
		return
	}
	c.sendVideoMute(ctx, true)
}

// sendVideoMute tells the peer we stopped, or resumed, sending on our video
// content.
func (c *callSession) sendVideoMute(ctx context.Context, muted bool) {
	c.mu.Lock()
	remote, videoMid := c.remoteJID, ""
	if c.pc != nil {
		videoMid = c.pc.VideoMid()
	}
	c.mu.Unlock()
	if videoMid == "" || remote == "" {
		return
	}
	info := xmpp.RTPInfo{Creator: "initiator", Name: videoMid}
	if err := c.client.SendMute(ctx, remote, c.sid, info, muted); err != nil {
		slog.Warn("sending video mute", "sid", c.sid, "muted", muted, "err", err)
	}
}

// removeVideoContent sends the content-remove retracting our video
// content-add and drops it from remoteContents, or sharing again would
// answer two m-lines for one mid.
func (c *callSession) removeVideoContent(ctx context.Context) {
	c.mu.Lock()
	remote, videoMid := c.remoteJID, ""
	if c.pc != nil {
		videoMid = c.pc.VideoMid()
	}
	c.mu.Unlock()
	if videoMid == "" || remote == "" {
		return
	}
	slog.Debug("screen share: withdrawing video content", "sid", c.sid, "content", videoMid)
	// Creator "initiator" matches what jingleContentsFromSDP stamped on the
	// content-add that introduced this content.
	remove := xmpp.JingleContent{Creator: "initiator", Name: videoMid}
	if err := c.client.SendContentRemove(ctx, remote, c.sid, remove); err != nil {
		slog.Warn("withdrawing video content", "sid", c.sid, "err", err)
	}

	c.mu.Lock()
	for i, rc := range c.remoteContents {
		if rc.Name == videoMid {
			c.remoteContents = append(c.remoteContents[:i], c.remoteContents[i+1:]...)
			break
		}
	}
	c.mu.Unlock()
}

// clearRemoteVideoViewer forgets the mpv window playRemoteVideo registered,
// without closing it - for the paths that just closed it themselves.
func (c *callSession) clearRemoteVideoViewer() {
	c.mu.Lock()
	c.remoteVideoViewer = nil
	c.mu.Unlock()
}

// applyContentRemove handles the peer withdrawing a content: its video window
// closes and the content is forgotten, so re-adding the mid works. The call
// goes on: audio is never removed (XEP-0166 §7.2.8).
func (c *callSession) applyContentRemove(jingle xmpp.JingleIQ) {
	for _, req := range jingle.Contents {
		if req.Name == "" {
			continue
		}
		c.mu.Lock()
		found, wasVideo := false, false
		for i, rc := range c.remoteContents {
			if rc.Name == req.Name {
				found = true
				wasVideo = rc.Description != nil && rc.Description.Media == "video"
				c.remoteContents = append(c.remoteContents[:i], c.remoteContents[i+1:]...)
				break
			}
		}
		c.mu.Unlock()
		if !found {
			continue
		}
		slog.Debug("screen share: peer withdrew content", "sid", c.sid, "content", req.Name, "video", wasVideo)
		if wasVideo {
			c.closeRemoteVideo()
		}
	}
}

// applyVideoMute handles the peer's XEP-0167 <mute/> for one of its video
// contents: it stopped sending, so its window closes like on a
// content-remove, and reopens once it sends again.
func (c *callSession) applyVideoMute(name string) {
	c.mu.Lock()
	video := false
	for _, rc := range c.remoteContents {
		if rc.Name == name && rc.Description != nil && rc.Description.Media == "video" {
			video = true
			break
		}
	}
	c.mu.Unlock()
	if !video {
		return
	}
	slog.Debug("screen share: peer muted video", "sid", c.sid, "content", name)
	c.closeRemoteVideo()
}

// closeRemoteVideo closes the peer's video window because its stream
// stopped. A later stream is worth showing, even if this one's window had
// been closed by hand.
func (c *callSession) closeRemoteVideo() {
	c.mu.Lock()
	viewer := c.remoteVideoViewer
	c.remoteVideoViewer = nil
	c.remoteVideo, c.viewerDismissed = false, false
	c.mu.Unlock()
	if viewer != nil {
		viewer.Close()
	}
	c.broadcastUpdate()
}

// playRemoteVideo pipes the peer's video track into an mpv window, which
// decodes it (see call.ScreenViewer). The window only opens on the first
// keyframe, so a negotiated but silent track shows nothing.
func (c *callSession) playRemoteVideo(pc *call.PeerConnection, track *webrtc.TrackRemote) {
	slog.Debug("screen share: remote video track registered, waiting for packets", "sid", c.sid, "track_id", track.ID(), "ssrc", track.SSRC())

	c.mu.Lock()
	c.remoteVideoTrack = track
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		wasActive := c.remoteVideo
		c.remoteVideo = false
		c.remoteVideoTrack = nil
		c.mu.Unlock()
		if wasActive {
			c.broadcastUpdate()
		}
	}()

	// The sender likely encoded its keyframe before this track existed here,
	// so ask for one (see SendPLI), a few times in case one is lost early.
	for _, delay := range []time.Duration{0, 2 * time.Second, 5 * time.Second} {
		time.AfterFunc(delay, func() {
			if err := pc.SendPLI(track.SSRC()); err != nil {
				slog.Debug("screen share: sending PLI failed", "sid", c.sid, "err", err)
				return
			}
			slog.Debug("screen share: PLI sent", "sid", c.sid)
		})
	}

	// track.Codec() is only known after the first packet: OnTrack fires
	// before it (SetFireOnTrackBeforeFirstRTP, see NewPeerConnection).
	var firstPacket *rtp.Packet
	for firstPacket == nil {
		select {
		case <-c.done:
			return
		default:
		}
		packet, _, err := track.ReadRTP()
		if err != nil {
			slog.Debug("screen share: reading remote video RTP ended before any codec was known", "sid", c.sid, "err", err)
			return
		}
		firstPacket = packet
	}

	// The peer may send VP8 rather than H.264; the wrong depacketizer
	// fails silently.
	mimeType := strings.ToLower(track.Codec().RTPCodecCapability.MimeType)
	var depacketizer rtp.Depacketizer
	var videoCodec call.VideoCodec
	var hasKeyframe func([]byte) bool
	switch mimeType {
	case strings.ToLower(webrtc.MimeTypeVP8):
		depacketizer = &codecs.VP8Packet{}
		videoCodec = call.VideoCodecVP8
		hasKeyframe = hasVP8Keyframe
	case strings.ToLower(webrtc.MimeTypeH264):
		depacketizer = &codecs.H264Packet{}
		videoCodec = call.VideoCodecH264
		hasKeyframe = hasH264IDRNAL
	default:
		slog.Warn("screen share: remote video track has an unsupported codec, not viewing it", "sid", c.sid, "mime_type", mimeType)
		c.broadcastNotice(fmt.Sprintf("can't show their video: unsupported codec %s", mimeType))
		return
	}
	slog.Debug("screen share: first remote video RTP packet received", "sid", c.sid, "seq", firstPacket.SequenceNumber, "payload_type", firstPacket.PayloadType, "codec", mimeType, "bytes", len(firstPacket.Payload))

	// maxLate, in packets, must hold a whole keyframe (100+ packets), or
	// every keyframe is dropped. The time cap keeps a lost packet from
	// stalling playback.
	sb := samplebuilder.New(1000, depacketizer, 90000, samplebuilder.WithMaxTimeDelay(500*time.Millisecond))
	sb.Push(firstPacket)
	var viewer *call.ScreenViewer
	packets, samples := 1, 0
	sawKeyframe := false
	defer func() {
		if viewer != nil {
			viewer.Close()
		}
		c.clearRemoteVideoViewer()
	}()
	for {
		for {
			sample := sb.Pop()
			if sample == nil {
				break
			}
			samples++
			if viewer != nil {
				c.mu.Lock()
				closed := c.remoteVideoViewer != viewer
				c.mu.Unlock()
				if closed {
					// By closeRemoteVideo: the stream resumes with a
					// keyframe, possibly this one, for a new window.
					viewer, sawKeyframe = nil, false
				}
			}
			if hasKeyframe(sample.Data) && !sawKeyframe {
				sawKeyframe = true
				slog.Debug("screen share: first keyframe assembled", "sid", c.sid, "sample", samples, "bytes", len(sample.Data))
				c.mu.Lock()
				wasActive := c.remoteVideo
				c.remoteVideo = true
				c.mu.Unlock()
				if !wasActive {
					c.broadcastUpdate()
				}
			}
			if samples == 1 || samples%60 == 0 {
				slog.Debug("screen share: assembled sample", "sid", c.sid, "sample", samples, "bytes", len(sample.Data), "saw_keyframe", sawKeyframe)
			}
			if viewer == nil {
				// A new viewer needs a keyframe to show anything.
				if !sawKeyframe {
					continue
				}
				c.mu.Lock()
				dismissed := c.viewerDismissed
				c.mu.Unlock()
				if dismissed {
					continue
				}
				v, err := call.NewScreenViewer(c.peer+" — video", videoCodec)
				if err != nil {
					slog.Warn("starting screen share viewer", "sid", c.sid, "err", err)
					c.broadcastNotice(fmt.Sprintf("can't show their video: %v", err))
					return
				}
				viewer = v
				c.mu.Lock()
				c.remoteVideoViewer = v
				c.mu.Unlock()
				slog.Debug("screen share: mpv viewer launched", "sid", c.sid)
				c.broadcastUpdate()
			}
			if err := viewer.WriteFrame(sample.Data); err != nil {
				// The window was closed by hand: keep draining the track,
				// without reopening it until reopenRemoteVideo.
				slog.Debug("screen share: writing to mpv failed, closing viewer", "sid", c.sid, "err", err)
				viewer.Close()
				c.mu.Lock()
				dismissed := c.remoteVideoViewer == viewer
				if dismissed {
					c.remoteVideoViewer = nil
					c.viewerDismissed = true
				}
				c.mu.Unlock()
				viewer = nil
				sawKeyframe = false
				if dismissed {
					c.broadcastUpdate()
				}
				continue
			}
		}

		select {
		case <-c.done:
			return
		default:
		}

		packet, _, err := track.ReadRTP()
		if err != nil {
			slog.Debug("screen share: reading remote video RTP ended", "sid", c.sid, "packets", packets, "samples", samples, "saw_keyframe", sawKeyframe, "err", err)
			return // track ended, or the connection went away
		}
		packets++
		sb.Push(packet)
	}
}

// hasVP8Keyframe reports whether an assembled VP8 frame is a keyframe: the
// low bit of VP8's first byte (the uncompressed data chunk's frame tag) is
// 0 for a key frame, 1 for an interframe (RFC 6386 §9.1).
func hasVP8Keyframe(data []byte) bool {
	return len(data) > 0 && data[0]&0x1 == 0
}

// hasH264IDRNAL reports whether an H.264 access unit (Annex-B) contains an
// IDR slice, a keyframe.
func hasH264IDRNAL(data []byte) bool {
	for i := 0; i+3 < len(data); i++ {
		if data[i] != 0 || data[i+1] != 0 {
			continue
		}
		var nalStart int
		if data[i+2] == 1 {
			nalStart = i + 3
		} else if data[i+2] == 0 && i+3 < len(data) && data[i+3] == 1 {
			nalStart = i + 4
		} else {
			continue
		}
		if nalStart < len(data) && data[nalStart]&0x1f == 5 {
			return true
		}
	}
	return false
}

// --- teardown -----------------------------------------------------------

// end tears the call down once and tells the TUI why. It sends nothing: the
// caller already sent or received what ended it.
func (c *callSession) end(ctx context.Context, state, reason string) {
	c.closeOnce.Do(func() {
		close(c.done)
		c.stopRing()

		c.mu.Lock()
		slog.Debug("call ending", "sid", c.sid, "prior_state", c.state.String(), "new_state", state, "reason", reason, "was_sharing", c.sharing)
		if state == "failed" {
			slog.Warn("call failed", "sid", c.sid, "peer", c.peer, "prior_state", c.state.String(), "reason", reason)
		}
		c.state = callEnded
		pc, mic, spk, share, desktop := c.pc, c.mic, c.spk, c.videoSource, c.desktop
		c.pc, c.mic, c.spk, c.enc, c.dec = nil, nil, nil, nil, nil
		c.micStop, c.micDone, c.desktop = nil, nil, nil
		c.videoSource, c.sharing = nil, false
		if c.disconnectTimer != nil {
			c.disconnectTimer.Stop()
			c.disconnectTimer = nil
		}
		c.mu.Unlock()

		c.stopQualitySampler()

		if share != nil {
			share.Stop()
		}
		if desktop != nil {
			desktop.Close()
		}
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
		broadcast(c.srv, ui.CallStateMsg{
			AccountIdx: c.accountIdx, Peer: c.peer, SID: c.sid, State: state, Reason: reason,
		})

		c.logCall(state, reason)
	})
}

// callOutcome is a finished call's log outcome: "answered" if media ever
// started, else "failed", "declined" or, for anything else that never
// connected, "missed". reason is what end() was given.
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

// logCall stores the finished call as a call-log row in the peer's chat and
// broadcasts it like an incoming message. Runs from end().
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

	broadcast(c.srv, ui.IncomingMessageMsg{
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
	ev := c.stateEventLocked(state, reason)
	c.mu.Unlock()
	slog.Debug("call state broadcast", "sid", c.sid, "state", state.String(), "reason", reason, "incoming", c.incoming)
	broadcast(c.srv, ev)
}

// stateEventLocked builds the ui.CallStateMsg for state from everything else
// currently known about the call. c.mu must be held.
func (c *callSession) stateEventLocked(state callState, reason string) ui.CallStateMsg {
	video := ""
	if c.sharing || c.pendingShare {
		video = videoSourceScreen
		if c.videoUseCamera {
			video = videoSourceCamera
		}
	}
	return ui.CallStateMsg{
		AccountIdx: c.accountIdx, Peer: c.peer, SID: c.sid, State: state.String(), Reason: reason,
		Muted: c.muted, Quality: c.quality, Sharing: c.sharing,
		Video: video, VideoPending: c.pendingShare,
		RemoteVideo: c.remoteVideo, ViewerOpen: c.remoteVideoViewer != nil,
		DesktopAudio: c.desktop != nil, DesktopAudioEnabled: desktopAudioEnabled.Load(), DesktopAudioVolume: int(desktopAudioVolume.Load()),
		SAS: c.fingerprintSAS, FingerprintChanged: c.fingerprintChanged,
	}
}

// broadcastUpdate re-broadcasts the call's state after something else about
// it changed. Silent once ended, so a late goroutine can't follow end()'s
// final state with a stale one.
func (c *callSession) broadcastUpdate() {
	if state := c.currentState(); state != callEnded {
		c.broadcastState(state, "")
	}
}

// broadcastNotice is broadcastUpdate with a message for the user.
func (c *callSession) broadcastNotice(notice string) {
	c.mu.Lock()
	state := c.state
	ev := c.stateEventLocked(state, "")
	c.mu.Unlock()
	if state == callEnded {
		return
	}
	ev.Notice = notice
	broadcast(c.srv, ev)
}

// setMuted mutes or unmutes the mic and broadcasts it.
func (c *callSession) setMuted(muted bool) {
	c.mu.Lock()
	c.muted = muted
	state := c.state
	c.mu.Unlock()
	c.broadcastState(state, "")
}

// startQualitySampler samples the connection's quality every
// qualitySampleInterval until stopQualitySampler, broadcasting it.
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
