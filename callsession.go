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

// recoverAndLog runs f under a recover, logging a full stack trace on panic
// instead of crashing the whole daemon - every call-related goroutine below
// (media pumps, screen-share capture/playback, connection-state handling)
// runs detached from any request that could otherwise catch it, so this is
// their only panic boundary. what identifies which goroutine paniced in the
// log, since a stack trace alone doesn't say which call site started it.
func recoverAndLog(sid, what string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic in call goroutine", "sid", sid, "what", what, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	f()
}

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

	// ringingFrom collects the full JID of every one of the peer's resources
	// that sent a <ringing/> for this call (XEP-0353 fans <propose/> out to
	// all their devices, so more than one can start ringing). A <retract/>
	// addressed to the bare JID alone isn't reliably redelivered to every
	// resource - not every server broadcasts a bare-JID message to all of a
	// contact's connected resources - so hangupCall/rejectCall also retract
	// directly to each JID here, otherwise devices that never proceeded keep
	// ringing after the call is called off. Guarded by mu.
	ringingFrom []string

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

	// sharing is true while we're actively capturing and sending our own
	// screen; screenShare is the wf-recorder subprocess driving it, nil
	// whenever sharing is false. The peer's incoming screen share (if any)
	// has no session-level flag here - it's just whatever video arrives on
	// OnTrack, played via playRemoteVideo/ScreenViewer independently.
	// pendingShare is true from the moment a content-add offering video is
	// sent until the peer's content-accept lands - startVideoShare only
	// actually starts wf-recorder once negotiation completes (see
	// applyContentAccept), so an idle-forever video m-line never gets
	// negotiated in the first place (see call.PeerConnection.AddVideoTrack).
	sharing      bool
	pendingShare bool
	// videoUseCamera records which source startVideoShare was asked for, so
	// beginScreenShareCapture (invoked later, once the peer's content-accept
	// lands) knows whether to spin up call.NewCamera or call.NewScreenShare.
	videoUseCamera bool
	videoSource    call.VideoSource

	// receivingRemoteVideo is true for as long as playRemoteVideo is
	// actively reading the peer's own video track - startVideoShare refuses
	// to run while it's set (see that doc for why: a second video content
	// added on top of an already-negotiated one from the peer was observed
	// live to collide on the same mid and get the whole call terminated).
	receivingRemoteVideo bool

	// remoteVideoTrack is the peer's currently active incoming video track,
	// set for as long as playRemoteVideo is reading it - lets reopenRemoteVideo
	// (the "peer closed mpv by accident, press the key again" recovery path)
	// request a fresh keyframe without needing its own copy of the track.
	remoteVideoTrack *webrtc.TrackRemote

	// localFingerprint/remoteFingerprint are the two ends' DTLS-SRTP
	// certificate fingerprints (XEP-0320), fingerprintSAS is the short
	// authentication string derived from both (see computeSAS), and
	// fingerprintChanged is true if remoteFingerprint didn't match this
	// contact's previously pinned one (see checkAndPinCallFingerprint) - the
	// TOFU + manual-verification mitigation for a malicious/compromised
	// signaling server swapping fingerprints to MITM the call (see
	// call_fingerprint.go). Set once negotiation completes: acceptSession
	// for the callee, applyAnswer for the caller.
	localFingerprint   string
	remoteFingerprint  string
	fingerprintSAS     string
	fingerprintChanged bool

	// autoStartVideo/autoStartVideoUseCamera: set once at call creation by
	// startVideoCall (VideoCallToggle's action - camera/screen chosen before
	// dialing), consumed exactly once by onConnectionState the first time
	// this call reaches callConnected, then cleared. A plain startCall call
	// leaves autoStartVideo false, so nothing here changes for a normal
	// voice call.
	autoStartVideo          bool
	autoStartVideoUseCamera bool

	// remoteContents is the peer's description of every Jingle content
	// established so far (initially just audio, from session-initiate/
	// -accept). A later content-add/content-accept only carries the new
	// content, so this is the base it gets merged onto to reconstruct a full
	// SDP for pion's SetRemoteDescription (see applyContentAdd/
	// applyContentAccept) - a partial SDP would look like every other m-line
	// got removed.
	remoteContents []xmpp.JingleContent

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
	_, err := s.doStartCall(ctx, srv, accountIdx, to, false, false)
	return err
}

// startVideoCall places a call exactly like startCall, but additionally
// arms autoStartVideo so the moment it reaches callConnected, onConnectionState
// starts sending our own video (camera or screen, see useCamera) without a
// separate ScreenShare call - VideoCallToggle's action.
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

// rejectAndNotifyOwnDevices sends a XEP-0353 <reject/> to the caller (remote)
// declining the call, then, mirroring answerCall's AcceptCall self-notify,
// tells every other online resource of this account that it can stop
// ringing on the same fanned-out propose. That self-notify used to go only
// to our own bare JID, same as AcceptCall - but unlike a <retract/> to the
// peer's bare JID (see retractFromAllRinging), there was no way to verify
// the server actually redelivers a bare-JID message to *every* resource,
// and evidently at least one doesn't: other devices kept ringing after a
// decline. So this sends directly to each sibling resource's full JID too,
// using the same live presence tracking (setRosterPresence) that already
// keeps sess.roster's entry for our own bare JID up to date with which of
// our resources are online - the self-presence every server pushes about
// an account's own other connected resources (RFC 6121 4.4.2).
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
// (the "should reach every resource" address) plus directly to every full
// JID that's individually confirmed ringing (see ringingFrom) - belt and
// suspenders against servers that don't fan a bare-JID message out to all of
// a contact's connected resources, which otherwise left devices that never
// answered ringing forever after the call was called off.
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
		// An incoming call we haven't answered yet: no Jingle session exists
		// to terminate, only the XEP-0353 propose to decline - same wire
		// action as rejectCall, reached here when hangupCall (ctrl+g) is used
		// instead of the dedicated reject key.
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
		// Our own outgoing call, not yet proceeded to a Jingle session - same
		// wire action as hangupCall's equivalent branch (there's no
		// session-terminate to send yet, and remoteJID isn't even known
		// until the callee proceeds). Without this case, calling rejectCall
		// on an outgoing call silently sent nothing at all.
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

// setScreenShare starts or stops capturing and sending our own video (screen
// or camera, see useCamera) on the current call's (always-negotiated, see
// call.NewPeerConnection) video track. useCamera is ignored when stopping.
func (s *accountSession) setScreenShare(sharing, useCamera bool) error {
	c := s.currentCall()
	if c == nil {
		return fmt.Errorf("no call in progress")
	}
	if sharing {
		return c.startVideoShare(useCamera)
	}
	c.stopScreenShare()
	return nil
}

// reopenVideo re-requests a keyframe for the current call's incoming video
// (see callSession.reopenRemoteVideo) - the "peer closed mpv by accident"
// recovery action.
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
		// Only meaningful for the caller: a callee that sent its own
		// <ringing/> can get it back as a carbon of its own stanza (XEP-0280
		// self-copy, "from" left as our own address by the copy), which used
		// to unconditionally re-broadcast ringing-remote over the correct
		// ringing-local state moments after handlePropose set it - the TUI
		// showed the caller's "hang up only" bar instead of answer/reject.
		if bareJID(ev.From) == s.account.JID {
			return
		}
		c.mu.Lock()
		// <propose/> fans out to every resource of the callee, so more than
		// one can answer with <ringing/> - track each so hangupCall/
		// rejectCall can retract directly to devices a bare-JID retract
		// might not reach (see ringingFrom's doc comment).
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
		// Mirrors JMIAccept below: as the caller, a <reject/> from the callee
		// always ends the call. As the callee (c.incoming), rejectCall now
		// also sends <reject/> to our own bare JID so every other resource
		// that's also ringing on this propose learns one of them declined -
		// only end on that self-notify if we're still the one ringing,
		// otherwise a device that already answered would hang up on itself.
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
		// waiting in this slice - just let the TUI show what it missed. Also
		// tell our own other devices directly (see rejectAndNotifyOwnDevices
		// for why a bare-JID message alone isn't enough) so they don't keep
		// ringing on a call we've already declined as busy.
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
	slog.Debug("incoming call proposed", "sid", ev.SID, "from", from, "accountIdx", accountIdx)
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

	case xmpp.JingleActionContentAdd:
		c.applyContentAdd(ctx, ev.Jingle)

	case xmpp.JingleActionContentAccept:
		c.applyContentAccept(ctx, ev.Jingle)

	case xmpp.JingleActionContentModify:
		c.applyContentModify(ctx, ev.Jingle)

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
	// A video call (startVideoCall/VideoCallToggle) bundles its video track
	// into this very first offer, rather than sending it later as a
	// content-add once the audio-only call is already connected. The two
	// ought to be equivalent per XEP-0166, but a real peer (Conversations)
	// was observed live mishandling the content-add path specifically -
	// reacting to it with its own ICE restart that uses inconsistent
	// credentials across its own bundled m-lines and never completes,
	// silently dropping the call's video with no error on either side. Video
	// negotiated in the original offer/answer never hits that path at all.
	if autoVideo {
		if err := c.pc.AddVideoTrack(); err != nil {
			return fmt.Errorf("adding video track: %w", err)
		}
		c.mu.Lock()
		c.videoUseCamera = autoCamera
		// Consumed here, bundled into the offer below - onConnectionState's
		// own autoStartVideo check (the content-add path) must not also
		// fire once this call connects, or we'd try to add a second video
		// m-line on top of the one already negotiated.
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
	c.rememberTransport(contents)
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

	answer, err := c.pc.CreateAnswer()
	if err != nil {
		return err
	}
	contents, err := jingleContentsFromSDP(answer, !c.incoming)
	if err != nil {
		return err
	}
	// Echo the initiator's content name for each content so both ends agree
	// on the mid - matched by media kind, not position: contents[0] isn't
	// necessarily audio (e.g. an incoming audio+video call whose offer lists
	// video first), and blindly renaming contents[0] to the initiator's
	// audio name produced two contents with the same name (collapsing to
	// the same mid) whenever it wasn't, which some peers (Conversations)
	// reject outright with "Multiple entries with same key".
	for i := range contents {
		if contents[i].Description == nil {
			continue
		}
		if in, ok := firstContentOfKind(jingle.Contents, contents[i].Description.Media); ok && in.Name != "" {
			contents[i].Name = in.Name
		}
	}
	c.rememberTransport(contents)
	c.checkFingerprints(firstFingerprint(contents), firstFingerprint(jingle.Contents))
	c.broadcastState(c.currentState(), "")

	c.mu.Lock()
	// The peer's description of every content established so far - the base
	// a later content-add (e.g. video for screen sharing) gets merged onto
	// to reconstruct the full remote SDP (see applyContentAdd).
	c.remoteContents = jingle.Contents
	c.mu.Unlock()

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

	// A video call (see initiateSession) bundled its video track into the
	// original offer rather than a later content-add - if the callee's
	// answer accepted it, this is the equivalent of applyContentAccept's
	// job for that path: negotiation just completed, so it's finally safe
	// to actually start capturing and sending frames.
	if _, ok := firstContentOfKind(jingle.Contents, "video"); ok {
		c.beginScreenShareCapture(pc)
	}
	return nil
}

// checkFingerprints computes the SAS and TOFU-checks remoteFP against this
// contact's pinned fingerprint (see checkAndPinCallFingerprint), storing the
// result on c for callBarLine to display. No-op if either fingerprint is
// missing (shouldn't happen - sdpFromJingleContents/jingleContentsFromSDP
// both require one - but a call session with nothing to compare is better
// than a false "changed" alarm from an empty string).
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

// rememberTransport records our own ICE credentials and content name from
// the description we just generated, so trickled transport-info IQs can
// repeat them.
func (c *callSession) rememberTransport(contents []xmpp.JingleContent) {
	// ICE-UDP is bundled here (one ICE session shared by every mid via
	// a=group:BUNDLE), so the ufrag/pwd pair is the same regardless of which
	// content we anchor to - but the *content name* used to label trickled
	// candidates is not a free choice: it has to be the bundle-tag content
	// (the first m= section - RFC 8843 - so the first one here, since our
	// content order always mirrors SDP m-line order). Hardcoding "audio"
	// here used to seem harmless for exactly that reason, until a call
	// where video is the first content (e.g. an incoming video call):
	// candidates kept arriving labeled for the audio content, the peer
	// never got any candidate for the actual bundle-tag line, and ICE sat
	// at "checking" for the whole call - observed live against Conversations,
	// which strictly attributes every candidate to the content name it came
	// labeled with rather than just applying it to the whole bundle.
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
			}
		}
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
	contents, err := jingleContentsFromSDP(offer, !c.incoming)
	if err != nil || len(contents) == 0 {
		slog.Warn("building transport-replace from restart offer", "sid", c.sid, "attempt", attempt, "err", err)
		c.scheduleRestartRetry(ctx, attempt)
		return
	}
	c.rememberTransport(contents)

	// The bundle-tag content (first m-line, whichever kind it is) - see
	// rememberTransport's doc for why this can't be hardcoded to "audio".
	content := contents[0]
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
	c.rememberTransport(contents)

	// The bundle-tag content (first m-line, whichever kind it is) - see
	// rememberTransport's doc for why this can't be hardcoded to "audio".
	content := contents[0]
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

// startMedia opens the audio devices and starts pumping mic -> Opus -> RTP.
// The receive direction is started by OnTrack instead, since it can't begin
// before the peer's track actually arrives.
// If the mic is unavailable, the call proceeds without it (send-only disabled).
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
		slog.Warn("opening capture device, proceeding without mic", "sid", c.sid, "err", err)
		mic = nil
	}
	spk, err := call.NewSpeaker()
	if err != nil {
		if mic != nil {
			mic.Close()
		}
		return err
	}

	c.mu.Lock()
	c.enc, c.dec, c.mic, c.spk = enc, dec, mic, spk
	pc := c.pc
	c.mu.Unlock()

	if mic != nil {
		go recoverAndLog(c.sid, "micPump", func() {
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
		})
	}
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

// currentState returns c.state under lock, for broadcastState calls made
// from places (like startVideoShare) that changed something other than the
// lifecycle state and just want to re-broadcast the unchanged one.
func (c *callSession) currentState() callState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// startVideoShare adds the video track and sends a XEP-0166 content-add
// offering it - the capture process (wf-recorder or, for useCamera, ffmpeg
// reading the webcam) only actually starts once the peer's content-accept
// lands (see applyContentAccept), so the video m-line only ever gets
// negotiated with real media about to flow, never idle from the start (see
// call.NewPeerConnection's doc comment for why that matters). No-op if
// already sharing or a content-add is already in flight. Runs fine while
// we're already receiving the peer's own video too - each direction gets its
// own content/mid (see call.PeerConnection.AddVideoTrack, which always
// allocates a fresh transceiver rather than repurposing the recvonly one
// applyContentAdd left in place for the peer's stream), so both sides can be
// sending video on the same call simultaneously.
func (c *callSession) startVideoShare(useCamera bool) error {
	c.mu.Lock()
	if c.sharing || c.pendingShare {
		c.mu.Unlock()
		return nil
	}
	pc, state, remote := c.pc, c.state, c.remoteJID
	c.videoUseCamera = useCamera
	c.mu.Unlock()
	if pc == nil || state != callConnected {
		return fmt.Errorf("call is not connected")
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
	videoContent, ok := firstContentOfKind(contents, "video")
	if !ok {
		return fmt.Errorf("no video content in renegotiation offer")
	}
	slog.Debug("screen share: sending content-add", "sid", c.sid, "to", remote, "content_name", videoContent.Name, "payload_types", len(videoContent.Description.PayloadTypes))

	c.mu.Lock()
	c.pendingShare = true
	c.mu.Unlock()

	if err := c.client.SendContentAdd(context.Background(), remote, c.sid, videoContent); err != nil {
		c.mu.Lock()
		c.pendingShare = false
		c.mu.Unlock()
		return fmt.Errorf("sending content-add: %w", err)
	}
	return nil
}

// applyContentAdd is the responder side of a peer starting a screen share:
// merge the new video content onto what's already established, apply the
// resulting full offer, and answer with content-accept. We don't add our
// own video track here - only the side that called startVideoShare sends;
// this side just receives (pion's CreateAnswer sets recvonly for a
// transceiver with no local track).
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

	// No local video track is added on this side (see comment above), so
	// given the sendonly offer we just applied, pion's CreateAnswer
	// negotiates recvonly for the video m-line on its own.
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

// applyContentAccept is the sharer's side: the peer's answer to our
// content-add completes the renegotiation, at which point it's finally safe
// to actually start capturing and sending frames (see beginScreenShareCapture).
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
	c.mu.Unlock()

	slog.Debug("screen share: content-add negotiation complete, starting capture", "sid", c.sid)
	slog.Debug("screen share: transceiver state after content-accept", "sid", c.sid, "transceivers", pc.DebugTransceivers())
	c.beginScreenShareCapture(pc)
}

// applyContentModify replies to a peer's XEP-0166 content-modify (e.g.
// Conversations trying to upgrade a receive-only video content to
// bidirectional so it can send its own camera back) by echoing the
// content's senders value from before the request, unchanged - see
// xmpp.JingleActionContentModify's doc for why silence here is actively
// wrong (a real peer, observed live, read it as acceptance and proceeded on
// a senders value kage's own PeerConnection never actually adopted, leaving
// the two sides disagreeing about who sends what for the rest of the call).
func (c *callSession) applyContentModify(ctx context.Context, jingle xmpp.JingleIQ) {
	c.mu.Lock()
	remote, remoteContents := c.remoteJID, c.remoteContents
	c.mu.Unlock()

	for _, req := range jingle.Contents {
		// content-modify only names the content (creator/name/senders), no
		// <description/> - matching jingleContentsFromSDP's own assumption
		// that echoed content names are stable identifiers, not something to
		// re-derive from a description that may not even be present here.
		var current xmpp.JingleContent
		var ok bool
		for _, rc := range remoteContents {
			if rc.Name == req.Name {
				current, ok = rc, true
				break
			}
		}
		if !ok {
			continue
		}
		slog.Debug("screen share: declining content-modify, keeping senders unchanged", "sid", c.sid, "content", req.Name, "requested_senders", req.Senders, "kept_senders", current.Senders)
		decline := xmpp.JingleContent{Creator: req.Creator, Name: req.Name, Senders: current.Senders}
		if err := c.client.SendContentModify(ctx, remote, c.sid, decline); err != nil {
			slog.Warn("declining content-modify", "sid", c.sid, "err", err)
		}
	}
}

// defaultCameraDevice is the only camera device kage tries - no UI or config
// exists yet to pick among several, so the common single-webcam case is all
// that's supported for now.
const defaultCameraDevice = "/dev/video0"

// beginScreenShareCapture launches the capture process (wf-recorder for a
// screen share, ffmpeg/v4l2 for a camera - see c.videoUseCamera) and starts
// pumping captured frames onto the now-fully-negotiated video track.
func (c *callSession) beginScreenShareCapture(pc *call.PeerConnection) {
	c.mu.Lock()
	useCamera := c.videoUseCamera
	c.mu.Unlock()

	quality := currentVideoQuality()
	var share call.VideoSource
	var err error
	if useCamera {
		share, err = call.NewCamera(defaultCameraDevice, quality)
	} else {
		share, err = call.NewScreenShare(quality)
	}
	if err != nil {
		slog.Warn("starting video capture", "sid", c.sid, "camera", useCamera, "err", err)
		return
	}
	slog.Debug("screen share: capture started", "sid", c.sid, "camera", useCamera, "video_sender_ssrc", pc.VideoSenderSSRC())

	c.mu.Lock()
	c.videoSource = share
	c.sharing = true
	c.mu.Unlock()

	go recoverAndLog(c.sid, "screenShareCapture", func() {
		if err := share.Run(func(frame []byte, sinceLast time.Duration) error {
			return pc.WriteVideoSample(frame, sinceLast)
		}); err != nil {
			slog.Warn("screen share capture ended", "sid", c.sid, "err", err)
		}
		c.mu.Lock()
		stillOurs := c.videoSource == share
		if stillOurs {
			c.videoSource = nil
			c.sharing = false
		}
		c.mu.Unlock()
		if stillOurs {
			c.broadcastState(c.currentState(), "")
		}
	})

	c.broadcastState(c.currentState(), "")
}

// reopenRemoteVideo re-requests a keyframe for the peer's currently active
// incoming video track, letting playRemoteVideo's viewer==nil branch reopen
// mpv once it arrives - the recovery path for a peer who closed the mpv
// window by accident and wants it back, without tearing down or
// renegotiating the video content itself.
func (c *callSession) reopenRemoteVideo() error {
	c.mu.Lock()
	pc, track := c.pc, c.remoteVideoTrack
	c.mu.Unlock()
	if pc == nil || track == nil {
		return fmt.Errorf("no active incoming video to reopen")
	}
	return pc.SendPLI(track.SSRC())
}

// stopScreenShare tears down the capture process, if one is running.
func (c *callSession) stopScreenShare() {
	c.mu.Lock()
	share := c.videoSource
	c.videoSource = nil
	c.sharing = false
	c.mu.Unlock()
	if share != nil {
		share.Stop()
	}
	c.broadcastState(c.currentState(), "")
}

// playRemoteVideo pipes a peer's shared-screen video track into mpv,
// mirroring playRemote's role for audio - reassembled via pion's H.264
// depacketizer/samplebuilder rather than decoded ourselves; mpv does the
// actual H.264 decode (see call.ScreenViewer). The viewer window is only
// spawned once the first complete frame arrives, so a video track that's
// negotiated but never actually sends anything (the common case: the peer
// isn't sharing) never pops up an empty mpv window.
func (c *callSession) playRemoteVideo(pc *call.PeerConnection, track *webrtc.TrackRemote) {
	slog.Debug("screen share: remote video track registered, waiting for packets", "sid", c.sid, "track_id", track.ID(), "ssrc", track.SSRC())

	c.mu.Lock()
	c.receivingRemoteVideo = true
	c.remoteVideoTrack = track
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.receivingRemoteVideo = false
		c.remoteVideoTrack = nil
		c.mu.Unlock()
	}()

	// Request a keyframe now, not just once we're ready to decode: the
	// sender's encoder was very likely already running before this track
	// existed on our side (see SendPLI's doc), so without this the first
	// samples we assemble reference a keyframe we never received and the
	// viewer stays black indefinitely. A couple of retries cover a PLI lost
	// before ICE/DTLS is fully settled.
	for _, delay := range []time.Duration{0, 2 * time.Second, 5 * time.Second} {
		time.AfterFunc(delay, func() {
			if err := pc.SendPLI(track.SSRC()); err != nil {
				slog.Debug("screen share: sending PLI failed", "sid", c.sid, "err", err)
				return
			}
			slog.Debug("screen share: PLI sent", "sid", c.sid)
		})
	}

	// The very first RTP packet has to be read before we can ask pion what
	// codec it actually negotiated: track.Codec() is empty at the moment
	// OnTrack fires (SetFireOnTrackBeforeFirstRTP - see NewPeerConnection -
	// deliberately fires OnTrack before codec auto-detection completes, to
	// dodge a separate pion crash), and only reads as the real value once
	// codec detection has run, which needs a packet to have arrived first.
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

	// The peer picks the codec, not us - kage's own screen-share always
	// sends H.264, but an arbitrary Jingle peer's video (e.g. a phone
	// placing a video call) may have negotiated VP8 instead. Feeding VP8
	// RTP through an H.264 depacketizer produces no error, just NAL-shaped
	// garbage that never contains anything hasKeyframe recognizes and a
	// viewer that opens and stays black forever - so ask pion what it
	// actually negotiated rather than assuming.
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
		return
	}
	slog.Debug("screen share: first remote video RTP packet received", "sid", c.sid, "seq", firstPacket.SequenceNumber, "payload_type", firstPacket.PayloadType, "codec", mimeType, "bytes", len(firstPacket.Payload))

	sb := samplebuilder.New(50, depacketizer, 90000)
	sb.Push(firstPacket)
	var viewer *call.ScreenViewer
	packets, samples := 1, 0
	sawKeyframe := false
	defer func() {
		if viewer != nil {
			viewer.Close()
		}
	}()
	for {
		for {
			sample := sb.Pop()
			if sample == nil {
				break
			}
			samples++
			if hasKeyframe(sample.Data) && !sawKeyframe {
				sawKeyframe = true
				slog.Debug("screen share: first keyframe assembled", "sid", c.sid, "sample", samples, "bytes", len(sample.Data))
			}
			if samples == 1 || samples%60 == 0 {
				slog.Debug("screen share: assembled sample", "sid", c.sid, "sample", samples, "bytes", len(sample.Data), "saw_keyframe", sawKeyframe)
			}
			if viewer == nil {
				// Only open a fresh viewer once we've actually seen a keyframe
				// since the last one closed - feeding an interframe-only stream
				// into a brand new mpv process never decodes into a visible
				// picture.
				if !sawKeyframe {
					continue
				}
				v, err := call.NewScreenViewer(c.peer+" — screen share", videoCodec)
				if err != nil {
					slog.Warn("starting screen share viewer", "sid", c.sid, "err", err)
					return
				}
				viewer = v
				slog.Debug("screen share: mpv viewer launched", "sid", c.sid)
			}
			if err := viewer.WriteFrame(sample.Data); err != nil {
				// mpv exited (e.g. the peer closed the window by accident) -
				// this doesn't end the call's video: keep draining the track
				// so reopenRemoteVideo (bound to a key in the UI) can request a
				// fresh keyframe and reopen a new viewer without restarting
				// the whole share.
				slog.Debug("screen share: writing to mpv failed, closing viewer", "sid", c.sid, "err", err)
				viewer.Close()
				viewer = nil
				sawKeyframe = false
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

// hasH264IDRNAL reports whether an assembled H.264 access unit (Annex-B,
// one or more concatenated NALs) contains an IDR slice (type 5) - i.e. is a
// keyframe a decoder can actually start from. Used purely for diagnosing
// "video track receives packets but the viewer stays black": that symptom
// is indistinguishable from working RTP unless we can tell whether any of
// what we assembled was ever decodable in the first place (see SendPLI).
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

// end tears the call down exactly once and tells the TUI why. It does not
// send any stanza itself: whichever caller decided the call is over already
// sent (or received) the terminate/reject/retract that says so on the wire.
func (c *callSession) end(ctx context.Context, state, reason string) {
	c.closeOnce.Do(func() {
		close(c.done)
		c.stopRing()

		c.mu.Lock()
		slog.Debug("call ending", "sid", c.sid, "prior_state", c.state.String(), "new_state", state, "reason", reason, "was_sharing", c.sharing)
		c.state = callEnded
		pc, mic, spk, share := c.pc, c.mic, c.spk, c.videoSource
		c.pc, c.mic, c.spk, c.enc, c.dec = nil, nil, nil, nil, nil
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
	muted, quality, sharing := c.muted, c.quality, c.sharing
	sas, fpChanged := c.fingerprintSAS, c.fingerprintChanged
	c.mu.Unlock()
	slog.Debug("call state broadcast", "sid", c.sid, "state", state.String(), "reason", reason, "incoming", c.incoming)
	broadcast(c.srv, evCallState, callStateEvent{
		AccountIdx: c.accountIdx, Peer: c.peer, SID: c.sid, State: state.String(), Reason: reason,
		Muted: muted, Quality: quality, Sharing: sharing,
		SAS: sas, FingerprintChanged: fpChanged,
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
