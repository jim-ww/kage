// Jingle <-> SDP translation. This is deliberately in package main rather
// than in call/ or xmpp/: it's the only code that has to know both pion's
// SDP shapes and the XEP-0166/0167/0176/0320 XML structs, and neither of
// those packages may import the other (call stays decoupled from xmpp, same
// rule ui/crypto follow).
//
// Only what an audio-only, rtcp-mux'd, trickle-ICE WebRTC call needs is
// translated today, but the content/SSRC plumbing is generalized to handle
// multiple m-lines (e.g. a future video content) so a second content doesn't
// require rewriting this file again. jingleContentsFromSDP/
// sdpFromJingleContents are reused as-is for ICE restart's transport-replace/
// -accept (see callsession.go's attemptICERestart/applyTransportReplace) -
// same offer/answer shapes, just with fresh ICE credentials.
package main

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"

	"github.com/jim-ww/kage/xmpp"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v4"
)

// jingleContentName is the SDP mid / Jingle content name used when we're the
// one generating the offer. When answering we echo the peer's instead.
const jingleContentName = "audio"

// jingleContentsFromSDP translates a local pion SessionDescription into the
// XEP-0166 <content/> list carried by a session-initiate/session-accept.
//
// Candidates present in desc at the time of the call are included, but ours
// are normally still being gathered - the rest trickle out separately as
// transport-info (see jingleTransportInfoContent).
func jingleContentsFromSDP(desc webrtc.SessionDescription, isInitiator bool) ([]xmpp.JingleContent, error) {
	var parsed sdp.SessionDescription
	if err := parsed.UnmarshalString(desc.SDP); err != nil {
		return nil, fmt.Errorf("parsing local sdp: %w", err)
	}

	var contents []xmpp.JingleContent
	for _, media := range parsed.MediaDescriptions {
		name := attrOr(media, &parsed, "mid", jingleContentName)
		direction := getSDPDirection(media)
		senders := directionToSenders(direction, isInitiator)
		content := xmpp.JingleContent{
			Creator: "initiator",
			Name:    name,
			Senders: senders,
			Description: &xmpp.RTPDescription{
				Media:        media.MediaName.Media,
				PayloadTypes: payloadTypesFromSDP(media),
				Sources:      sourcesFromSDP(media),
			},
			Transport: &xmpp.ICEUDPTransport{
				Ufrag: attrOr(media, &parsed, "ice-ufrag", ""),
				Pwd:   attrOr(media, &parsed, "ice-pwd", ""),
			},
		}
		if _, ok := media.Attribute("rtcp-mux"); ok {
			content.Description.RTCPMux = &struct{}{}
		}
		if fp := attrOr(media, &parsed, "fingerprint", ""); fp != "" {
			hash, value, _ := strings.Cut(fp, " ")
			content.Transport.Fingerprint = &xmpp.DTLSFingerprint{
				Hash:  hash,
				Setup: attrOr(media, &parsed, "setup", "actpass"),
				Value: value,
			}
		}
		for _, a := range media.Attributes {
			if a.Key != "candidate" {
				continue
			}
			cand, err := jingleCandidateFromSDPLine(a.Value)
			if err != nil {
				continue // a candidate we can't model is better skipped than fatal
			}
			content.Transport.Candidates = append(content.Transport.Candidates, cand)
		}
		contents = append(contents, content)
	}
	if len(contents) == 0 {
		return nil, fmt.Errorf("no media sections in local sdp")
	}
	return contents, nil
}

// sourcesFromSDP reads the `a=ssrc:<id> cname:<value>` lines pion generates
// for a media section's local track into a single XEP-0167/XEP-0339
// <source/> element. Only cname is carried today - it's the one parameter
// every consumer of this needs (stream identity), and pion itself doesn't
// emit msid/label/mslabel on its generated offers.
//
// Only the first (primary) SSRC is kept, even if pion auto-negotiated RTX
// for the m= section and so declared a second one there: without full
// XEP-0339 <ssrc-group/> support (out of scope - see JingleSource's doc) to
// tell a peer the second SSRC is a repair stream for the first, sending it
// as an ordinary, ungrouped second <source/> leaves the peer either
// confused about which SSRC is the actual media (observed live: a peer
// declared both under the same msid and then received zero frames) or
// tripping the same Plan-B misdetection sdpFromJingleContents already
// guards against on the receiving end - this is that fix's counterpart for
// our own outgoing offers/answers.
func sourcesFromSDP(media *sdp.MediaDescription) []xmpp.JingleSource {
	var src *xmpp.JingleSource
	for _, a := range media.Attributes {
		if a.Key != "ssrc" {
			continue
		}
		idStr, rest, ok := strings.Cut(a.Value, " ")
		if !ok {
			continue
		}
		id, err := strconv.ParseUint(idStr, 10, 32)
		if err != nil {
			continue
		}
		ssrc := uint32(id)
		if src == nil {
			src = &xmpp.JingleSource{SSRC: ssrc}
		} else if src.SSRC != ssrc {
			continue // a second, presumably RTX, SSRC - not carried, see doc above
		}
		attrName, attrValue, ok := strings.Cut(rest, ":")
		if ok {
			src.Parameters = append(src.Parameters, xmpp.JingleSourceParam{Name: attrName, Value: attrValue})
		}
	}
	if src == nil {
		return nil
	}
	return []xmpp.JingleSource{*src}
}

// payloadTypesFromSDP reads the rtpmap/fmtp lines of one media section into
// XEP-0167 <payload-type/> elements, in the offer's preference order.
func payloadTypesFromSDP(media *sdp.MediaDescription) []xmpp.RTPPayloadType {
	rtpmaps := map[string]string{}
	fmtps := map[string]string{}
	for _, a := range media.Attributes {
		id, rest, ok := strings.Cut(a.Value, " ")
		if !ok {
			continue
		}
		switch a.Key {
		case "rtpmap":
			rtpmaps[id] = rest
		case "fmtp":
			fmtps[id] = rest
		}
	}

	var out []xmpp.RTPPayloadType
	for _, format := range media.MediaName.Formats {
		id, err := strconv.Atoi(format)
		if err != nil {
			continue
		}
		pt := xmpp.RTPPayloadType{ID: id}
		parts := strings.Split(rtpmaps[format], "/")
		if len(parts) > 0 {
			pt.Name = parts[0]
		}
		if len(parts) > 1 {
			pt.ClockRate, _ = strconv.Atoi(parts[1])
		}
		if len(parts) > 2 {
			pt.Channels, _ = strconv.Atoi(parts[2])
		}
		if pt.Name == "" {
			continue // a payload type with no rtpmap is nothing we can describe
		}
		for _, param := range strings.Split(fmtps[format], ";") {
			param = strings.TrimSpace(param)
			if param == "" {
				continue
			}
			k, v, _ := strings.Cut(param, "=")
			pt.Parameters = append(pt.Parameters, xmpp.RTPParameter{Name: k, Value: v})
		}
		out = append(out, pt)
	}
	return out
}

// sdpFromJingleContents rebuilds a full SDP offer/answer from an incoming
// stanza's contents (session-initiate/-accept, content-add/-accept,
// transport-replace/-accept), so it can be handed to
// PeerConnection.SetRemoteDescription. Because contents always came from the
// remote peer, isInitiator here means "is the remote peer the session's
// Jingle initiator" - the opposite of the local callSession's own role (see
// callSession.incoming) - so the reconstructed a=sendonly/recvonly reflects
// what the remote peer is actually doing, not us. This is the inverse of
// jingleContentsFromSDP, which always describes our own SDP and so always
// takes our own role. One MediaDescription is emitted per content that
// carries a Description+Transport (today that's always exactly the one
// audio content; the loop is content-agnostic so a second content, e.g.
// video, needs no changes here).
func sdpFromJingleContents(contents []xmpp.JingleContent, typ webrtc.SDPType, isInitiator bool) (webrtc.SessionDescription, error) {
	described := contentsWithMedia(contents)
	if len(described) == 0 {
		return webrtc.SessionDescription{}, fmt.Errorf("jingle %s has no content with a description and transport", typ)
	}

	var mids []string
	var mediaDescs []*sdp.MediaDescription
	for _, content := range described {
		if content.Transport.Fingerprint == nil {
			return webrtc.SessionDescription{}, fmt.Errorf("jingle %s carries no DTLS fingerprint", typ)
		}

		mid := content.Name
		if mid == "" {
			mid = jingleContentName
		}
		mids = append(mids, mid)

		var formats []string
		direction := sendersToDirection(content.Senders, isInitiator)
		attrs := []sdp.Attribute{
			{Key: "mid", Value: mid},
			{Key: "ice-ufrag", Value: content.Transport.Ufrag},
			{Key: "ice-pwd", Value: content.Transport.Pwd},
			{Key: "ice-options", Value: "trickle"},
			{Key: "fingerprint", Value: content.Transport.Fingerprint.Hash + " " + strings.TrimSpace(content.Transport.Fingerprint.Value)},
			{Key: "setup", Value: setupOrDefault(content.Transport.Fingerprint.Setup, typ)},
			{Key: direction},
			{Key: "rtcp-mux"},
		}
		for _, pt := range content.Description.PayloadTypes {
			id := strconv.Itoa(pt.ID)
			formats = append(formats, id)

			rtpmap := id + " " + pt.Name
			if pt.ClockRate > 0 {
				rtpmap += "/" + strconv.Itoa(pt.ClockRate)
				if pt.Channels > 0 {
					rtpmap += "/" + strconv.Itoa(pt.Channels)
				}
			}
			attrs = append(attrs, sdp.Attribute{Key: "rtpmap", Value: rtpmap})

			if len(pt.Parameters) > 0 {
				params := make([]string, 0, len(pt.Parameters))
				for _, p := range pt.Parameters {
					params = append(params, p.Name+"="+p.Value)
				}
				attrs = append(attrs, sdp.Attribute{Key: "fmtp", Value: id + " " + strings.Join(params, ";")})
			}
		}
		if len(formats) == 0 {
			return webrtc.SessionDescription{}, fmt.Errorf("jingle %s content %q offers no payload types", typ, mid)
		}
		// Only the first source: a peer sending RTX/FEC lists those as
		// additional <source/> elements alongside the primary one (grouped
		// via <ssrc-group/> in full XEP-0339, which we don't parse - see
		// JingleSource's doc). Emitting every one of them as an ungrouped
		// a=ssrc here would leave pion's Plan-B heuristic seeing multiple
		// unlinked tracks under one mid (it specifically special-cases
		// a=ssrc-group:FID to rule that out - see pion's trackDetailsFromSDP)
		// and it then refuses to answer at all ("Expected UnifiedPlan, but
		// RemoteDescription is PlanB"). We don't do RTX/FEC ourselves, so
		// the primary source - conventionally listed first - is all we
		// actually need here.
		if srcs := content.Description.Sources; len(srcs) > 0 {
			for _, p := range srcs[0].Parameters {
				attrs = append(attrs, sdp.Attribute{
					Key:   "ssrc",
					Value: fmt.Sprintf("%d %s:%s", srcs[0].SSRC, p.Name, p.Value),
				})
			}
		}
		for _, c := range content.Transport.Candidates {
			attrs = append(attrs, sdp.Attribute{Key: "candidate", Value: sdpCandidateLine(c)})
		}

		mediaDescs = append(mediaDescs, &sdp.MediaDescription{
			MediaName: sdp.MediaName{
				Media:   content.Description.Media,
				Port:    sdp.RangedPort{Value: 9},
				Protos:  []string{"UDP", "TLS", "RTP", "SAVPF"},
				Formats: formats,
			},
			ConnectionInformation: &sdp.ConnectionInformation{
				NetworkType: "IN",
				AddressType: "IP4",
				Address:     &sdp.Address{Address: "0.0.0.0"},
			},
			Attributes: attrs,
		})
	}

	desc := sdp.SessionDescription{
		Version: 0,
		Origin: sdp.Origin{
			Username: "-", SessionID: rand.Uint64(), SessionVersion: 2,
			NetworkType: "IN", AddressType: "IP4", UnicastAddress: "127.0.0.1",
		},
		SessionName:      "-",
		TimeDescriptions: []sdp.TimeDescription{{Timing: sdp.Timing{StartTime: 0, StopTime: 0}}},
		Attributes: []sdp.Attribute{
			{Key: "group", Value: "BUNDLE " + strings.Join(mids, " ")},
			{Key: "msid-semantic", Value: "WMS *"},
		},
		MediaDescriptions: mediaDescs,
	}

	raw, err := desc.Marshal()
	if err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("marshaling sdp from jingle %s: %w", typ, err)
	}
	return webrtc.SessionDescription{Type: typ, SDP: string(raw)}, nil
}

// setupOrDefault fills in the DTLS role for a peer that omitted a=setup:
// an offer defaults to actpass, an answer to active (RFC 5763 §5).
func setupOrDefault(setup string, typ webrtc.SDPType) string {
	if setup != "" {
		return setup
	}
	if typ == webrtc.SDPTypeOffer {
		return "actpass"
	}
	return "active"
}

// contentsWithMedia returns every content that carries both a Description
// and a Transport - i.e. every content actually usable for SDP translation,
// regardless of media kind. Multiple contents (audio + a future video) are
// returned in the order they appear.
func contentsWithMedia(contents []xmpp.JingleContent) []xmpp.JingleContent {
	var out []xmpp.JingleContent
	for _, c := range contents {
		if c.Description != nil && c.Transport != nil {
			out = append(out, c)
		}
	}
	return out
}

// firstContentOfKind picks the first fully-described content of the given
// media kind (e.g. "audio") out of a Jingle's content list. Everywhere in
// today's audio-only call this is the only content there is; it stays
// separate from contentsWithMedia for the places that specifically need
// "the audio one" once a second content (e.g. video) exists.
func firstContentOfKind(contents []xmpp.JingleContent, mediaKind string) (xmpp.JingleContent, bool) {
	for _, c := range contents {
		if c.Description != nil && c.Description.Media == mediaKind && c.Transport != nil {
			return c, true
		}
	}
	return xmpp.JingleContent{}, false
}

// jingleTransportInfoContent wraps one locally-gathered ICE candidate in the
// <content/> shape a XEP-0176 transport-info IQ expects. ufrag/pwd are
// repeated on every transport-info so the peer can tell candidates of the
// current ICE generation from a stale one.
func jingleTransportInfoContent(name, ufrag, pwd string, candidate xmpp.ICECandidate) xmpp.JingleContent {
	return xmpp.JingleContent{
		Creator: "initiator",
		Name:    name,
		Transport: &xmpp.ICEUDPTransport{
			Ufrag:      ufrag,
			Pwd:        pwd,
			Candidates: []xmpp.ICECandidate{candidate},
		},
	}
}

// jingleCandidateFromICE translates a pion candidate into its XEP-0176 form.
func jingleCandidateFromICE(c *webrtc.ICECandidate) xmpp.ICECandidate {
	return xmpp.ICECandidate{
		Component:  int(c.Component),
		Foundation: c.Foundation,
		Generation: 0,
		ID:         c.Foundation + "-" + strconv.Itoa(int(c.Port)),
		IP:         c.Address,
		Network:    0,
		Port:       int(c.Port),
		Priority:   int(c.Priority),
		Protocol:   c.Protocol.String(),
		Type:       c.Typ.String(),
		RelAddr:    c.RelatedAddress,
		RelPort:    int(c.RelatedPort),
	}
}

// jingleCandidateFromSDPLine parses an SDP a=candidate value (the part after
// "candidate:") into its XEP-0176 form.
func jingleCandidateFromSDPLine(line string) (xmpp.ICECandidate, error) {
	fields := strings.Fields(line)
	if len(fields) < 8 || fields[6] != "typ" {
		return xmpp.ICECandidate{}, fmt.Errorf("unparsable candidate %q", line)
	}
	cand := xmpp.ICECandidate{
		Foundation: fields[0],
		IP:         fields[4],
		Protocol:   strings.ToLower(fields[2]),
		Type:       fields[7],
		ID:         fields[0] + "-" + fields[5],
	}
	cand.Component, _ = strconv.Atoi(fields[1])
	cand.Priority, _ = strconv.Atoi(fields[3])
	cand.Port, _ = strconv.Atoi(fields[5])
	for i := 8; i+1 < len(fields); i += 2 {
		switch fields[i] {
		case "raddr":
			cand.RelAddr = fields[i+1]
		case "rport":
			cand.RelPort, _ = strconv.Atoi(fields[i+1])
		case "generation":
			cand.Generation, _ = strconv.Atoi(fields[i+1])
		}
	}
	return cand, nil
}

// sdpCandidateLine renders a XEP-0176 candidate as an SDP a=candidate value.
func sdpCandidateLine(c xmpp.ICECandidate) string {
	component := c.Component
	if component == 0 {
		component = 1
	}
	line := fmt.Sprintf("%s %d %s %d %s %d typ %s",
		c.Foundation, component, strings.ToUpper(c.Protocol), c.Priority, c.IP, c.Port, c.Type)
	if c.RelAddr != "" {
		line += fmt.Sprintf(" raddr %s rport %d", c.RelAddr, c.RelPort)
	}
	return line + fmt.Sprintf(" generation %d", c.Generation)
}

// iceCandidateInit turns a XEP-0176 candidate into the form
// PeerConnection.AddICECandidate takes, tied to the m-line named by mid.
func iceCandidateInit(c xmpp.ICECandidate, mid string) webrtc.ICECandidateInit {
	idx := uint16(0)
	return webrtc.ICECandidateInit{
		Candidate:     "candidate:" + sdpCandidateLine(c),
		SDPMid:        &mid,
		SDPMLineIndex: &idx,
	}
}

// attrOr reads an attribute from a media section, falling back to the
// session level (where some peers put ice-ufrag/pwd/fingerprint) and then to
// def.
func attrOr(media *sdp.MediaDescription, session *sdp.SessionDescription, key, def string) string {
	if v, ok := media.Attribute(key); ok {
		return v
	}
	if v, ok := session.Attribute(key); ok {
		return v
	}
	return def
}

// getSDPDirection reads the direction attribute from a media section.
// Returns "sendrecv" by default if no direction attribute is present.
func getSDPDirection(media *sdp.MediaDescription) string {
	for _, dir := range []string{"sendrecv", "sendonly", "recvonly", "inactive"} {
		if _, ok := media.Attribute(dir); ok {
			return dir
		}
	}
	return "sendrecv"
}

// directionToSenders maps a *local* SDP direction attribute to its XEP-0166
// senders equivalent, which is expressed in terms of the Jingle session's
// initiator/responder roles rather than "us"/"them" - isInitiator says which
// role this local peer holds (see callSession.incoming). This is the inverse
// of sendersToDirection.
func directionToSenders(direction string, isInitiator bool) string {
	self, other := "responder", "initiator"
	if isInitiator {
		self, other = "initiator", "responder"
	}
	switch direction {
	case "sendrecv":
		return "both"
	case "sendonly":
		return self
	case "recvonly":
		return other
	case "inactive":
		return "none"
	default:
		return "both"
	}
}

// sendersToDirection maps a XEP-0166 senders attribute (in terms of the
// session's initiator/responder roles) to the local SDP direction attribute
// for this peer - isInitiator says which role this local peer holds (see
// callSession.incoming). "both" maps to sendrecv and "none" to inactive
// regardless of role; "initiator"/"responder" resolve to sendonly if that
// role is ours, recvonly otherwise. When the attribute is empty (older
// clients), we default to sendrecv.
func sendersToDirection(senders string, isInitiator bool) string {
	switch senders {
	case "both":
		return "sendrecv"
	case "none":
		return "inactive"
	case "initiator":
		if isInitiator {
			return "sendonly"
		}
		return "recvonly"
	case "responder":
		if isInitiator {
			return "recvonly"
		}
		return "sendonly"
	default:
		// Empty or unknown: default to sendrecv for compatibility
		return "sendrecv"
	}
}
