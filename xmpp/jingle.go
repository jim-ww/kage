package xmpp

// This file implements XEP-0166 (Jingle) session signaling, XEP-0167
// (Jingle RTP Sessions) audio description, XEP-0176 (Jingle ICE-UDP
// Transport), and XEP-0353 (Jingle Message Initiation) for the lightweight
// propose/ringing push that precedes a full Jingle session. It only builds
// and sends/parses the stanzas — it has no call-state machine and no
// awareness of pion/audio.

import (
	"context"
	"encoding/xml"
	"fmt"
	"log/slog"

	"mellium.im/xmlstream"
	"mellium.im/xmpp/jid"
	"mellium.im/xmpp/stanza"
)

// XML namespaces used by this file.
const (
	jingleNS        = "urn:xmpp:jingle:1"
	jingleRTPNS     = "urn:xmpp:jingle:apps:rtp:1"
	jingleICEUDPNS  = "urn:xmpp:jingle:transports:ice-udp:1"
	jingleMessageNS = "urn:xmpp:jingle-message:0"
	jingleDTLSNS    = "urn:xmpp:jingle:apps:dtls:0"
	jingleSSMANS    = "urn:xmpp:jingle:apps:rtp:ssma:0"
)

// Jingle actions (XEP-0166 §7.2). Only the ones this slice needs to send are
// named as constants; others may still round-trip through JingleIQ.Action
// as a plain string when decoding stanzas we don't originate.
const (
	JingleActionSessionInitiate  = "session-initiate"
	JingleActionSessionAccept    = "session-accept"
	JingleActionSessionTerminate = "session-terminate"
	JingleActionTransportInfo    = "transport-info"

	// Transport restart actions (XEP-0166/0176 §6.1, "Transport Restart"):
	// the Jingle equivalent of WebRTC's RTCPeerConnection.restartIce() plus
	// renegotiation, scoped to just the transport rather than the whole
	// session. transport-replace carries the new transport (fresh
	// ufrag/pwd/candidates), transport-accept is the peer's answer to it,
	// transport-reject says the peer can't/won't restart.
	JingleActionTransportReplace = "transport-replace"
	JingleActionTransportAccept  = "transport-accept"
	JingleActionTransportReject  = "transport-reject"

	// Content-add actions (XEP-0166 §7.2.4/§7.2.5): adding a new content
	// (e.g. a video track for screen sharing) to an already-established
	// session, without touching the content(s) already flowing. content-add
	// carries only the new content's description+transport; content-accept
	// is the peer's answer to it, same shape.
	JingleActionContentAdd    = "content-add"
	JingleActionContentAccept = "content-accept"

	// JingleActionContentModify (XEP-0166 §7.2.9) asks to change an existing
	// content's senders (e.g. upgrade a receive-only video content to
	// bidirectional). kage never originates one, only replies to a peer's:
	// we don't have a camera pipeline to become a sender with, so the only
	// correct reply (per the same section) is to echo the content back with
	// its senders value unchanged - an explicit decline, rather than silence
	// the peer might read as acceptance (observed live: Conversations logged
	// "remote has accepted our upgrade to senders=both" after kage silently
	// dropped its content-modify, since we didn't yet have a case for it at
	// all).
	JingleActionContentModify = "content-modify"
)

// JingleIQ is the <jingle/> payload of a Jingle IQ (XEP-0166). SID is the
// session ID both parties use to correlate all stanzas of one call.
type JingleIQ struct {
	Action    string          `xml:"action,attr"`
	SID       string          `xml:"sid,attr"`
	Initiator string          `xml:"initiator,attr,omitempty"`
	Responder string          `xml:"responder,attr,omitempty"`
	Contents  []JingleContent `xml:"content"`
	Reason    *JingleReason   `xml:"reason"`
}

// JingleContent is one <content/> within a Jingle session — for an
// audio-only call there is exactly one, named "audio".
type JingleContent struct {
	Creator     string           `xml:"creator,attr"`
	Name        string           `xml:"name,attr"`
	Senders     string           `xml:"senders,attr,omitempty"`
	Description *RTPDescription  `xml:"urn:xmpp:jingle:apps:rtp:1 description"`
	Transport   *ICEUDPTransport `xml:"urn:xmpp:jingle:transports:ice-udp:1 transport"`
}

// RTPDescription is the XEP-0167 <description/> naming the codec(s) offered
// or accepted for this content.
type RTPDescription struct {
	Media        string           `xml:"media,attr"`
	PayloadTypes []RTPPayloadType `xml:"payload-type"`

	// RTCPMux mirrors SDP's a=rtcp-mux (XEP-0167 §3): RTP and RTCP share one
	// port. WebRTC requires it, so it's always set on what we generate — it's
	// a pointer only so an incoming description that omits it round-trips
	// distinctly.
	RTCPMux *struct{} `xml:"urn:xmpp:jingle:apps:rtp:1 rtcp-mux"`

	// Sources are the XEP-0167/XEP-0339 SSMA <source/> elements naming the
	// SSRC(s) sending media for this content. Needed once a session has more
	// than one content to disambiguate streams; harmless to include even for
	// today's single-content audio calls.
	Sources []JingleSource `xml:"urn:xmpp:jingle:apps:rtp:ssma:0 source"`
}

// JingleSource is one XEP-0339 SSMA <source/>, declaring a single SSRC and
// its parameters (just cname, here — enough to identify the stream; RTX/FEC
// grouping via <ssrc-group/> is out of scope).
type JingleSource struct {
	SSRC       uint32              `xml:"ssrc,attr"`
	Parameters []JingleSourceParam `xml:"urn:xmpp:jingle:apps:rtp:ssma:0 parameter"`
}

// JingleSourceParam is one <parameter/> of a <source/>, e.g. cname.
type JingleSourceParam struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

// RTPPayloadType is one negotiable codec (XEP-0167 <payload-type/>), e.g.
// Opus at 48kHz.
type RTPPayloadType struct {
	ID        int    `xml:"id,attr"`
	Name      string `xml:"name,attr"`
	ClockRate int    `xml:"clockrate,attr,omitempty"`
	Channels  int    `xml:"channels,attr,omitempty"`

	// Parameters are the codec's format parameters, SDP's a=fmtp key=value
	// pairs (XEP-0167 §3) - e.g. Opus's useinbandfec=1.
	Parameters []RTPParameter `xml:"urn:xmpp:jingle:apps:rtp:1 parameter"`
}

// RTPParameter is one XEP-0167 <parameter/>, one key=value pair of an SDP
// a=fmtp line.
type RTPParameter struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

// ICEUDPTransport is the XEP-0176 <transport/> carrying ICE credentials and
// zero or more candidates. A session-initiate/-accept typically carries the
// credentials plus any candidates gathered so far; later candidates trickle
// in via separate transport-info IQs (see (*Client).SendTransportInfo).
type ICEUDPTransport struct {
	Ufrag      string         `xml:"ufrag,attr,omitempty"`
	Pwd        string         `xml:"pwd,attr,omitempty"`
	Candidates []ICECandidate `xml:"candidate"`

	// Fingerprint is XEP-0320's DTLS-SRTP fingerprint. WebRTC refuses to
	// negotiate media without one, so a transport built from a pion SDP
	// always carries it, and one arriving without it can't be turned back
	// into a usable SDP.
	Fingerprint *DTLSFingerprint `xml:"urn:xmpp:jingle:apps:dtls:0 fingerprint"`
}

// DTLSFingerprint is the XEP-0320 <fingerprint/>: the hash of our DTLS
// certificate plus the DTLS role (SDP's a=setup).
type DTLSFingerprint struct {
	Hash  string `xml:"hash,attr"`
	Setup string `xml:"setup,attr,omitempty"`
	Value string `xml:",chardata"`
}

// ICECandidate is one XEP-0176 <candidate/>, mirroring the fields of a
// standard ICE candidate closely enough to translate to/from a
// pion/webrtc.ICECandidate.
type ICECandidate struct {
	Component  int    `xml:"component,attr"`
	Foundation string `xml:"foundation,attr"`
	Generation int    `xml:"generation,attr"`
	ID         string `xml:"id,attr,omitempty"`
	IP         string `xml:"ip,attr"`
	Network    int    `xml:"network,attr"`
	Port       int    `xml:"port,attr"`
	Priority   int    `xml:"priority,attr"`
	Protocol   string `xml:"protocol,attr"`
	Type       string `xml:"type,attr"`

	// RelAddr/RelPort are the reflexive/relayed candidate's base address
	// (SDP's raddr/rport). Absent on host candidates.
	RelAddr string `xml:"rel-addr,attr,omitempty"`
	RelPort int    `xml:"rel-port,attr,omitempty"`
}

// JingleReason is the optional <reason/> on a session-terminate, naming why
// the session ended. Modeled as separate pointer fields for the same reason
// messageBody's chat states are - encoding/xml matches struct fields to
// fixed element names, not a variant tag.
type JingleReason struct {
	Success           *struct{} `xml:"success"`
	Decline           *struct{} `xml:"decline"`
	Cancel            *struct{} `xml:"cancel"`
	ConnectivityError *struct{} `xml:"connectivity-error"`
	Text              string    `xml:"text,omitempty"`
}

// jingleStanza wraps JingleIQ in its IQ envelope for send/decode, same
// embedding shape as messageBody wraps stanza.Message.
type jingleStanza struct {
	stanza.IQ
	Jingle JingleIQ `xml:"urn:xmpp:jingle:1 jingle"`
}

// sendJingleIQ addresses and sends a Jingle IQ. Jingle actions are
// fire-and-forget from this package's point of view - callers wanting to
// know whether the peer acknowledged (or errored) the IQ will need to add
// response tracking themselves; this slice only builds and sends the wire
// format.
func (c *Client) sendJingleIQ(ctx context.Context, to string, jingle JingleIQ) error {
	toJID, err := jid.Parse(to)
	if err != nil {
		return fmt.Errorf("parsing recipient %q: %w", to, err)
	}
	iq := jingleStanza{
		IQ:     stanza.IQ{To: toJID, Type: stanza.SetIQ, ID: randomID()},
		Jingle: jingle,
	}
	if err := c.session.Encode(ctx, iq); err != nil {
		return fmt.Errorf("sending jingle %s: %w", jingle.Action, err)
	}
	return nil
}

// SendSessionInitiate sends a XEP-0166 session-initiate: the full offer,
// naming the codec(s) (XEP-0167) and ICE-UDP transport (XEP-0176) for the
// audio content. jingle.Action and jingle.Initiator are set by this method;
// the caller fills in SID, Contents, etc.
func (c *Client) SendSessionInitiate(ctx context.Context, to string, jingle JingleIQ) error {
	jingle.Action = JingleActionSessionInitiate
	jingle.Initiator = c.JID.String()
	return c.sendJingleIQ(ctx, to, jingle)
}

// SendSessionAccept sends a XEP-0166 session-accept: our answer to an
// incoming session-initiate, naming our chosen codec and transport.
func (c *Client) SendSessionAccept(ctx context.Context, to string, jingle JingleIQ) error {
	jingle.Action = JingleActionSessionAccept
	jingle.Responder = c.JID.String()
	return c.sendJingleIQ(ctx, to, jingle)
}

// SendSessionTerminate ends the Jingle session identified by sid, with an
// optional reason (nil is fine - not every termination needs one).
func (c *Client) SendSessionTerminate(ctx context.Context, to, sid string, reason *JingleReason) error {
	return c.sendJingleIQ(ctx, to, JingleIQ{
		Action: JingleActionSessionTerminate,
		SID:    sid,
		Reason: reason,
	})
}

// SendTransportInfo trickles additional ICE candidates for an
// already-established session (XEP-0176 §4).
func (c *Client) SendTransportInfo(ctx context.Context, to, sid string, content JingleContent) error {
	return c.sendJingleIQ(ctx, to, JingleIQ{
		Action:   JingleActionTransportInfo,
		SID:      sid,
		Contents: []JingleContent{content},
	})
}

// SendTransportReplace sends a XEP-0166/0176 §6.1 transport-replace: the new
// transport (fresh ufrag/pwd/candidates) for an ICE restart, scoped to a
// single content like session-initiate but without repeating the
// description - content should carry only Transport, no Description.
func (c *Client) SendTransportReplace(ctx context.Context, to, sid string, content JingleContent) error {
	return c.sendJingleIQ(ctx, to, JingleIQ{
		Action:   JingleActionTransportReplace,
		SID:      sid,
		Contents: []JingleContent{content},
	})
}

// SendTransportAccept sends a XEP-0166/0176 §6.1 transport-accept: our
// answer to a peer's transport-replace, naming our own (possibly
// also-restarted) transport.
func (c *Client) SendTransportAccept(ctx context.Context, to, sid string, content JingleContent) error {
	return c.sendJingleIQ(ctx, to, JingleIQ{
		Action:   JingleActionTransportAccept,
		SID:      sid,
		Contents: []JingleContent{content},
	})
}

// SendTransportReject sends a XEP-0166/0176 §6.1 transport-reject: we can't
// or won't go along with the peer's proposed transport restart.
func (c *Client) SendTransportReject(ctx context.Context, to, sid string) error {
	return c.sendJingleIQ(ctx, to, JingleIQ{
		Action: JingleActionTransportReject,
		SID:    sid,
	})
}

// SendContentAdd sends a XEP-0166 content-add: a new content (description +
// transport) being added to an already-established session, e.g. a video
// track for screen sharing - the existing content(s) are untouched and not
// repeated here.
func (c *Client) SendContentAdd(ctx context.Context, to, sid string, content JingleContent) error {
	return c.sendJingleIQ(ctx, to, JingleIQ{
		Action:   JingleActionContentAdd,
		SID:      sid,
		Contents: []JingleContent{content},
	})
}

// SendContentAccept sends a XEP-0166 content-accept: our answer to a peer's
// content-add, naming our own description+transport for that content.
func (c *Client) SendContentAccept(ctx context.Context, to, sid string, content JingleContent) error {
	return c.sendJingleIQ(ctx, to, JingleIQ{
		Action:   JingleActionContentAccept,
		SID:      sid,
		Contents: []JingleContent{content},
	})
}

// SendContentModify replies to a peer's content-modify - see
// JingleActionContentModify's doc for why kage only ever echoes the
// content's senders value back unchanged, never actually changing it.
func (c *Client) SendContentModify(ctx context.Context, to, sid string, content JingleContent) error {
	return c.sendJingleIQ(ctx, to, JingleIQ{
		Action:   JingleActionContentModify,
		SID:      sid,
		Contents: []JingleContent{content},
	})
}

// jmiIDElem is the shape shared by every XEP-0353 element that carries
// nothing but the Jingle session ID.
type jmiIDElem struct {
	ID string `xml:"id,attr"`
}

// jmiDescription is XEP-0353's abbreviated echo of the RTP description
// inside a <propose/> - just enough (the media type) for the callee's other
// devices to show what kind of call this is before the real Jingle
// negotiation happens.
type jmiDescription struct {
	Media string `xml:"media,attr"`
}

// jmiProposeElem is the XEP-0353 <propose/> element.
type jmiProposeElem struct {
	ID           string           `xml:"id,attr"`
	Descriptions []jmiDescription `xml:"urn:xmpp:jingle:apps:rtp:1 description"`
}

// jmiMessage is the <message/> wrapper for a single XEP-0353 element. At
// most one field is set per message, same convention as messageBody's chat
// state fields.
type jmiMessage struct {
	stanza.Message
	Propose *jmiProposeElem `xml:"urn:xmpp:jingle-message:0 propose"`
	Ringing *jmiIDElem      `xml:"urn:xmpp:jingle-message:0 ringing"`
	Proceed *jmiIDElem      `xml:"urn:xmpp:jingle-message:0 proceed"`
	Reject  *jmiIDElem      `xml:"urn:xmpp:jingle-message:0 reject"`
	Accept  *jmiIDElem      `xml:"urn:xmpp:jingle-message:0 accept"`
	Retract *jmiIDElem      `xml:"urn:xmpp:jingle-message:0 retract"`
}

// sendJMI addresses and sends a single XEP-0353 element to "to".
func (c *Client) sendJMI(ctx context.Context, to string, build func(*jmiMessage)) error {
	toJID, err := jid.Parse(to)
	if err != nil {
		return fmt.Errorf("parsing recipient %q: %w", to, err)
	}
	msg := jmiMessage{Message: stanza.Message{To: toJID, ID: randomID()}}
	build(&msg)
	return c.session.Encode(ctx, msg)
}

// ProposeCall sends a XEP-0353 <propose/>: the lightweight "ring" push that
// precedes the full Jingle session-initiate IQ exchange, letting all of the
// callee's devices show an incoming-call notification immediately. sid is
// the session ID that the subsequent Jingle IQs (once the callee's chosen
// device proceeds) will use.
func (c *Client) ProposeCall(ctx context.Context, to, sid string) error {
	return c.sendJMI(ctx, to, func(m *jmiMessage) {
		// Video (for screen sharing) is negotiated later via a XEP-0166
		// content-add once the session is already established, not upfront -
		// the propose only needs to list what session-initiate will actually
		// offer immediately (audio), which is all a compliant peer checks
		// against.
		m.Propose = &jmiProposeElem{ID: sid, Descriptions: []jmiDescription{{Media: "audio"}}}
	})
}

// RingingCall sends a XEP-0353 <ringing/>, telling the caller that this
// device has alerted the user to the incoming call named by sid.
func (c *Client) RingingCall(ctx context.Context, to, sid string) error {
	return c.sendJMI(ctx, to, func(m *jmiMessage) { m.Ringing = &jmiIDElem{ID: sid} })
}

// ProceedCall sends a XEP-0353 <proceed/>: this device is answering the
// call named by sid, so the caller should begin the full Jingle
// session-initiate exchange with it.
func (c *Client) ProceedCall(ctx context.Context, to, sid string) error {
	return c.sendJMI(ctx, to, func(m *jmiMessage) { m.Proceed = &jmiIDElem{ID: sid} })
}

// RejectCall sends a XEP-0353 <reject/>, declining the call named by sid.
func (c *Client) RejectCall(ctx context.Context, to, sid string) error {
	return c.sendJMI(ctx, to, func(m *jmiMessage) { m.Reject = &jmiIDElem{ID: sid} })
}

// AcceptCall sends a XEP-0353 <accept/>, informing our own other devices
// that sid was answered elsewhere so they can stop ringing.
func (c *Client) AcceptCall(ctx context.Context, to, sid string) error {
	return c.sendJMI(ctx, to, func(m *jmiMessage) { m.Accept = &jmiIDElem{ID: sid} })
}

// RetractCall sends a XEP-0353 <retract/>, withdrawing our own earlier
// propose for sid (e.g. the caller hung up before anyone answered).
func (c *Client) RetractCall(ctx context.Context, to, sid string) error {
	return c.sendJMI(ctx, to, func(m *jmiMessage) { m.Retract = &jmiIDElem{ID: sid} })
}

// --- receive side ---

// JingleEvent is an incoming XEP-0166 Jingle IQ (session-initiate,
// session-accept, session-terminate, transport-info, ...) already decoded
// off the wire and acknowledged with an empty IQ result. From is the peer's
// *full* JID: unlike chat, Jingle is a resource-to-resource protocol, so
// every subsequent stanza of this session must be addressed back to exactly
// this JID rather than the bare one.
type JingleEvent struct {
	From   string
	Jingle JingleIQ
}

func (JingleEvent) isEvent() {}

// JMIAction names which XEP-0353 element a JingleMessageEvent carried.
type JMIAction string

// JMIAction values, one per XEP-0353 element.
const (
	JMIPropose JMIAction = "propose"
	JMIRinging JMIAction = "ringing"
	JMIProceed JMIAction = "proceed"
	JMIReject  JMIAction = "reject"
	JMIAccept  JMIAction = "accept"
	JMIRetract JMIAction = "retract"
)

// JingleMessageEvent is an incoming XEP-0353 Jingle Message Initiation
// element - the pre-session "ring" exchange. Media is only meaningful for
// JMIPropose.
type JingleMessageEvent struct {
	From   string
	SID    string
	Action JMIAction
	Media  string
}

func (JingleMessageEvent) isEvent() {}

// jingleIQName is the payload the mux matches incoming Jingle IQs on.
var jingleIQName = xml.Name{Space: jingleNS, Local: "jingle"}

// handleJingleIQ decodes a <jingle/> IQ payload, acknowledges it with an
// empty result (XEP-0166 requires the receiver to ack every action IQ before
// acting on it) and surfaces it on the event stream for the call session to
// drive. Registered on the client's mux in newDiscoMux.
func (c *Client) handleJingleIQ(iq stanza.IQ, t xmlstream.TokenReadEncoder, start *xml.StartElement) error {
	var jingle JingleIQ
	// Same decode caveat as handleStanza's message path: the stream is
	// already positioned inside start, so DecodeElement reports the closing
	// tag as an "unexpected end element" even on a fully successful decode.
	_ = xml.NewTokenDecoder(t).DecodeElement(&jingle, start)

	if _, err := xmlstream.Copy(t, iq.Result(nil)); err != nil {
		slog.Warn("acknowledging jingle iq", "action", jingle.Action, "err", err)
	}

	select {
	case c.events <- JingleEvent{From: iq.From.String(), Jingle: jingle}:
	default:
		slog.Warn("dropping jingle iq: event queue full", "action", jingle.Action)
	}
	return nil
}

// jmiEvent maps the one set JMI element on msg (if any) to an event.
func (m messageBody) jmiEvent() (JingleMessageEvent, bool) {
	ev := JingleMessageEvent{From: m.From.String()}
	switch {
	case m.JMIPropose != nil:
		ev.Action, ev.SID = JMIPropose, m.JMIPropose.ID
		if len(m.JMIPropose.Descriptions) > 0 {
			ev.Media = m.JMIPropose.Descriptions[0].Media
		}
	case m.JMIRinging != nil:
		ev.Action, ev.SID = JMIRinging, m.JMIRinging.ID
	case m.JMIProceed != nil:
		ev.Action, ev.SID = JMIProceed, m.JMIProceed.ID
	case m.JMIReject != nil:
		ev.Action, ev.SID = JMIReject, m.JMIReject.ID
	case m.JMIAccept != nil:
		ev.Action, ev.SID = JMIAccept, m.JMIAccept.ID
	case m.JMIRetract != nil:
		ev.Action, ev.SID = JMIRetract, m.JMIRetract.ID
	default:
		return JingleMessageEvent{}, false
	}
	return ev, true
}
