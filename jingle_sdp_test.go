package main

import (
	"strings"
	"testing"

	"github.com/jim-ww/kage/call"
	"github.com/jim-ww/kage/xmpp"
	"github.com/pion/webrtc/v4"
)

// pionOfferSDP is a real offer as produced by call.NewPeerConnection's
// CreateOffer (audio-only, one Opus track, trickle ICE). It's captured here
// rather than generated live so the translation is covered even in sandboxed
// environments where pion can't enumerate network interfaces at all.
const pionOfferSDP = `v=0
o=- 3948323618 1751403418 IN IP4 0.0.0.0
s=-
t=0 0
a=fingerprint:sha-256 F5:3C:1E:9A:44:2B:70:D8:6C:11:A0:5F:93:2E:88:47:6D:CA:31:05:9B:E2:74:AF:60:13:8C:29:B7:4E:D1:52
a=group:BUNDLE 0
m=audio 9 UDP/TLS/RTP/SAVPF 111
c=IN IP4 0.0.0.0
a=setup:actpass
a=mid:0
a=ice-ufrag:BCkjvJhCEcYkAWGP
a=ice-pwd:GVQhZbPTHmzDdWuJDeMdrHwYrEEJDMRF
a=rtcp-mux
a=rtcp-rsize
a=rtpmap:111 opus/48000/2
a=fmtp:111 minptime=10;useinbandfec=1
a=ssrc:1943397459 cname:kage
a=candidate:2718001543 1 udp 2130706431 192.168.1.5 51234 typ host generation 0
a=sendrecv
`

func TestJingleContentsFromSDP(t *testing.T) {
	contents, err := jingleContentsFromSDP(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: pionOfferSDP}, true)
	if err != nil {
		t.Fatalf("translating offer to jingle: %v", err)
	}
	if len(contents) != 1 {
		t.Fatalf("got %d jingle contents, want 1", len(contents))
	}
	c := contents[0]
	if c.Name != "0" || c.Creator != "initiator" {
		t.Errorf("content identified as %q/%q, want initiator/0", c.Creator, c.Name)
	}
	if c.Description == nil || c.Description.RTCPMux == nil {
		t.Fatalf("description lost rtcp-mux: %+v", c.Description)
	}
	if len(c.Description.PayloadTypes) != 1 {
		t.Fatalf("got %d payload types, want 1", len(c.Description.PayloadTypes))
	}
	pt := c.Description.PayloadTypes[0]
	if pt.ID != 111 || !strings.EqualFold(pt.Name, "opus") || pt.ClockRate != 48000 || pt.Channels != 2 {
		t.Errorf("opus payload type decoded as %+v", pt)
	}
	if len(pt.Parameters) != 2 || pt.Parameters[1].Name != "useinbandfec" || pt.Parameters[1].Value != "1" {
		t.Errorf("fmtp parameters decoded as %+v", pt.Parameters)
	}

	if c.Transport.Ufrag != "BCkjvJhCEcYkAWGP" || c.Transport.Pwd != "GVQhZbPTHmzDdWuJDeMdrHwYrEEJDMRF" {
		t.Errorf("ICE credentials decoded as %q/%q", c.Transport.Ufrag, c.Transport.Pwd)
	}
	// The fingerprint is session-level in this offer, not on the m-section:
	// finding it proves the session-level fallback works.
	if c.Transport.Fingerprint == nil || c.Transport.Fingerprint.Hash != "sha-256" || c.Transport.Fingerprint.Setup != "actpass" {
		t.Fatalf("fingerprint decoded as %+v", c.Transport.Fingerprint)
	}
	if len(c.Transport.Candidates) != 1 || c.Transport.Candidates[0].IP != "192.168.1.5" {
		t.Fatalf("candidates decoded as %+v", c.Transport.Candidates)
	}
}

// TestJingleSDPRoundTrip translates an SDP offer to Jingle and back, then
// re-reads the result: everything a WebRTC negotiation depends on has to
// survive both directions unchanged.
func TestJingleSDPRoundTrip(t *testing.T) {
	contents, err := jingleContentsFromSDP(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: pionOfferSDP}, true)
	if err != nil {
		t.Fatalf("translating offer to jingle: %v", err)
	}

	rebuilt, err := sdpFromJingleContents(contents, webrtc.SDPTypeOffer, true)
	if err != nil {
		t.Fatalf("translating jingle back to sdp: %v", err)
	}
	for _, want := range []string{
		"m=audio 9 UDP/TLS/RTP/SAVPF 111",
		"a=rtcp-mux",
		"a=ice-ufrag:BCkjvJhCEcYkAWGP",
		"a=setup:actpass",
		"a=rtpmap:111 opus/48000/2",
		"a=fmtp:111 minptime=10;useinbandfec=1",
		"a=group:BUNDLE 0",
		"a=mid:0",
		"a=candidate:2718001543 1 UDP 2130706431 192.168.1.5 51234 typ host generation 0",
	} {
		if !strings.Contains(rebuilt.SDP, want) {
			t.Errorf("rebuilt sdp is missing %q:\n%s", want, rebuilt.SDP)
		}
	}

	again, err := jingleContentsFromSDP(rebuilt, true)
	if err != nil {
		t.Fatalf("re-reading rebuilt sdp: %v", err)
	}
	if got, want := again[0].Transport.Fingerprint.Value, contents[0].Transport.Fingerprint.Value; got != want {
		t.Errorf("fingerprint changed across the round trip: %q vs %q", got, want)
	}
	if len(again[0].Description.PayloadTypes) != len(contents[0].Description.PayloadTypes) {
		t.Errorf("payload types changed across the round trip: %+v", again[0].Description.PayloadTypes)
	}
}

// TestJingleSDPAgainstPion feeds the reconstructed SDP to a real pion peer
// connection - the only way to know WebRTC actually accepts what we built,
// rather than just that it looks right. Skips where pion can't open a
// network (sandboxes without netlink), same as the live-server tests skip
// without devtest/prosody.
func TestJingleSDPAgainstPion(t *testing.T) {
	callee, err := call.NewPeerConnection()
	if err != nil {
		t.Skipf("no usable pion peer connection here: %v", err)
	}
	defer callee.Close()
	if _, err := callee.CreateOffer(); err != nil {
		t.Skipf("no usable pion peer connection here: %v", err)
	}

	contents, err := jingleContentsFromSDP(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: pionOfferSDP}, true)
	if err != nil {
		t.Fatalf("translating offer to jingle: %v", err)
	}
	rebuilt, err := sdpFromJingleContents(contents, webrtc.SDPTypeOffer, true)
	if err != nil {
		t.Fatalf("translating jingle back to sdp: %v", err)
	}

	answerer, err := call.NewPeerConnection()
	if err != nil {
		t.Fatalf("creating answerer: %v", err)
	}
	defer answerer.Close()

	if err := answerer.SetRemoteDescription(rebuilt); err != nil {
		t.Fatalf("pion rejected the rebuilt offer: %v\n%s", err, rebuilt.SDP)
	}
	answer, err := answerer.CreateAnswer()
	if err != nil {
		t.Fatalf("creating answer: %v", err)
	}
	answerContents, err := jingleContentsFromSDP(answer, true)
	if err != nil {
		t.Fatalf("translating answer to jingle: %v", err)
	}
	if _, err := sdpFromJingleContents(answerContents, webrtc.SDPTypeAnswer, true); err != nil {
		t.Fatalf("translating jingle answer back to sdp: %v", err)
	}
}

// TestSSRCRoundTrip confirms the a=ssrc:<id> cname:<value> line pion emits
// on its generated offer survives translation to Jingle <source/> and back
// out to SDP unchanged.
func TestSSRCRoundTrip(t *testing.T) {
	contents, err := jingleContentsFromSDP(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: pionOfferSDP}, true)
	if err != nil {
		t.Fatalf("translating offer to jingle: %v", err)
	}
	if len(contents) != 1 {
		t.Fatalf("got %d jingle contents, want 1", len(contents))
	}
	sources := contents[0].Description.Sources
	if len(sources) != 1 || sources[0].SSRC != 1943397459 {
		t.Fatalf("sources decoded as %+v, want ssrc 1943397459", sources)
	}
	if len(sources[0].Parameters) != 1 || sources[0].Parameters[0].Name != "cname" || sources[0].Parameters[0].Value != "kage" {
		t.Fatalf("source parameters decoded as %+v, want cname=kage", sources[0].Parameters)
	}

	rebuilt, err := sdpFromJingleContents(contents, webrtc.SDPTypeOffer, true)
	if err != nil {
		t.Fatalf("translating jingle back to sdp: %v", err)
	}
	if !strings.Contains(rebuilt.SDP, "a=ssrc:1943397459 cname:kage") {
		t.Errorf("rebuilt sdp is missing the ssrc line:\n%s", rebuilt.SDP)
	}

	again, err := jingleContentsFromSDP(rebuilt, true)
	if err != nil {
		t.Fatalf("re-reading rebuilt sdp: %v", err)
	}
	if len(again[0].Description.Sources) != 1 || again[0].Description.Sources[0].SSRC != 1943397459 {
		t.Errorf("ssrc changed across the round trip: %+v", again[0].Description.Sources)
	}
}

// pionTwoContentSDP is a synthetic offer with two m-lines (audio + a fake
// VP8 video section) - nothing in this app produces video today, but this
// proves jingleContentsFromSDP/sdpFromJingleContents handle more than one
// content without another rewrite once a real video content exists.
const pionTwoContentSDP = `v=0
o=- 3948323618 1751403418 IN IP4 0.0.0.0
s=-
t=0 0
a=fingerprint:sha-256 F5:3C:1E:9A:44:2B:70:D8:6C:11:A0:5F:93:2E:88:47:6D:CA:31:05:9B:E2:74:AF:60:13:8C:29:B7:4E:D1:52
a=group:BUNDLE 0 1
m=audio 9 UDP/TLS/RTP/SAVPF 111
c=IN IP4 0.0.0.0
a=setup:actpass
a=mid:0
a=ice-ufrag:BCkjvJhCEcYkAWGP
a=ice-pwd:GVQhZbPTHmzDdWuJDeMdrHwYrEEJDMRF
a=rtcp-mux
a=rtpmap:111 opus/48000/2
a=fmtp:111 minptime=10;useinbandfec=1
a=ssrc:1943397459 cname:kage
a=sendrecv
m=video 9 UDP/TLS/RTP/SAVPF 96
c=IN IP4 0.0.0.0
a=setup:actpass
a=mid:1
a=ice-ufrag:BCkjvJhCEcYkAWGP
a=ice-pwd:GVQhZbPTHmzDdWuJDeMdrHwYrEEJDMRF
a=rtcp-mux
a=rtpmap:96 VP8/90000
a=ssrc:2718001543 cname:kage
a=sendrecv
`

func TestJingleSDPMultiContentRoundTrip(t *testing.T) {
	contents, err := jingleContentsFromSDP(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: pionTwoContentSDP}, true)
	if err != nil {
		t.Fatalf("translating two-content offer to jingle: %v", err)
	}
	if len(contents) != 2 {
		t.Fatalf("got %d jingle contents, want 2", len(contents))
	}
	if contents[0].Description.Media != "audio" || contents[1].Description.Media != "video" {
		t.Fatalf("content media kinds are %q/%q, want audio/video", contents[0].Description.Media, contents[1].Description.Media)
	}
	if contents[1].Description.PayloadTypes[0].Name != "VP8" {
		t.Fatalf("video payload type decoded as %+v", contents[1].Description.PayloadTypes)
	}
	if len(contents[1].Description.Sources) != 1 || contents[1].Description.Sources[0].SSRC != 2718001543 {
		t.Fatalf("video sources decoded as %+v", contents[1].Description.Sources)
	}

	rebuilt, err := sdpFromJingleContents(contents, webrtc.SDPTypeOffer, true)
	if err != nil {
		t.Fatalf("translating two-content jingle back to sdp: %v", err)
	}
	for _, want := range []string{
		"a=group:BUNDLE 0 1",
		"m=audio 9 UDP/TLS/RTP/SAVPF 111",
		"m=video 9 UDP/TLS/RTP/SAVPF 96",
		"a=rtpmap:96 VP8/90000",
		"a=ssrc:1943397459 cname:kage",
		"a=ssrc:2718001543 cname:kage",
	} {
		if !strings.Contains(rebuilt.SDP, want) {
			t.Errorf("rebuilt multi-content sdp is missing %q:\n%s", want, rebuilt.SDP)
		}
	}

	again, err := jingleContentsFromSDP(rebuilt, true)
	if err != nil {
		t.Fatalf("re-reading rebuilt multi-content sdp: %v", err)
	}
	if len(again) != 2 {
		t.Errorf("got %d contents after re-reading rebuilt sdp, want 2", len(again))
	}
}

// TestJingleSDPSingleContentUnaffected confirms today's audio-only path
// still produces exactly one content and one m-line after generalizing to
// support multiple - the regression case for the multi-content plumbing.
func TestJingleSDPSingleContentUnaffected(t *testing.T) {
	contents, err := jingleContentsFromSDP(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: pionOfferSDP}, true)
	if err != nil {
		t.Fatalf("translating offer to jingle: %v", err)
	}
	if len(contents) != 1 {
		t.Fatalf("got %d jingle contents, want 1", len(contents))
	}

	rebuilt, err := sdpFromJingleContents(contents, webrtc.SDPTypeOffer, true)
	if err != nil {
		t.Fatalf("translating jingle back to sdp: %v", err)
	}
	if strings.Count(rebuilt.SDP, "\nm=") != 1 {
		t.Errorf("rebuilt single-content sdp has more than one m-line:\n%s", rebuilt.SDP)
	}
	if !strings.Contains(rebuilt.SDP, "a=group:BUNDLE 0") || strings.Contains(rebuilt.SDP, "a=group:BUNDLE 0 ") {
		t.Errorf("rebuilt single-content bundle group is wrong:\n%s", rebuilt.SDP)
	}
}

// TestICECandidateRoundTrip checks the candidate translation both ways,
// including the reflexive candidate's related address (which XEP-0176 calls
// rel-addr/rel-port and SDP calls raddr/rport).
func TestICECandidateRoundTrip(t *testing.T) {
	const line = "1829696788 1 udp 2122260223 192.168.1.5 51234 typ srflx raddr 10.0.0.2 rport 51235 generation 0"

	cand, err := jingleCandidateFromSDPLine(line)
	if err != nil {
		t.Fatalf("parsing candidate: %v", err)
	}
	if cand.Foundation != "1829696788" || cand.Component != 1 || cand.Protocol != "udp" ||
		cand.Priority != 2122260223 || cand.IP != "192.168.1.5" || cand.Port != 51234 ||
		cand.Type != "srflx" || cand.RelAddr != "10.0.0.2" || cand.RelPort != 51235 {
		t.Fatalf("candidate decoded wrong: %+v", cand)
	}

	init := iceCandidateInit(cand, "audio")
	if init.SDPMid == nil || *init.SDPMid != "audio" {
		t.Fatalf("candidate init has wrong mid: %v", init.SDPMid)
	}
	want := "candidate:1829696788 1 UDP 2122260223 192.168.1.5 51234 typ srflx raddr 10.0.0.2 rport 51235 generation 0"
	if init.Candidate != want {
		t.Fatalf("candidate re-rendered as %q, want %q", init.Candidate, want)
	}
}

// TestSendersDirectionRoleAware guards against a regression where a
// unidirectional content's XEP-0166 "senders" attribute was computed
// without regard to which Jingle role (initiator/responder) the local peer
// actually holds - "sendonly" locally was always written out as
// senders="initiator" even when the local peer sending it was the
// responder, which would tell a spec-compliant peer the wrong party is
// sending (this is exactly what screen-share content-add hits: the sharer
// isn't always the session's initiator).
func TestSendersDirectionRoleAware(t *testing.T) {
	cases := []struct {
		direction   string
		isInitiator bool
		wantSenders string
	}{
		{"sendonly", true, "initiator"},
		{"sendonly", false, "responder"},
		{"recvonly", true, "responder"},
		{"recvonly", false, "initiator"},
		{"sendrecv", true, "both"},
		{"sendrecv", false, "both"},
	}
	for _, tc := range cases {
		if got := directionToSenders(tc.direction, tc.isInitiator); got != tc.wantSenders {
			t.Errorf("directionToSenders(%q, isInitiator=%v) = %q, want %q",
				tc.direction, tc.isInitiator, got, tc.wantSenders)
		}
		// sendersToDirection must invert directionToSenders for the same role.
		if got := sendersToDirection(tc.wantSenders, tc.isInitiator); got != tc.direction {
			t.Errorf("sendersToDirection(%q, isInitiator=%v) = %q, want %q",
				tc.wantSenders, tc.isInitiator, got, tc.direction)
		}
	}
}

// TestSDPFromJingleContentsSingleSSRCPerMid guards against a regression
// where a peer sending RTX/FEC (e.g. a phone camera's video track, which
// almost always advertises a repair SSRC alongside the primary one) caused
// incoming video calls to be rejected outright. We don't parse XEP-0339
// <ssrc-group/> (see JingleSource's doc), so multiple <source/> elements on
// one content used to all become ungrouped a=ssrc lines under the same mid;
// pion's Plan-B heuristic (trackDetailsFromSDP) treats an unlinked second
// SSRC on one mid as a second track and refuses to answer at all
// ("Expected UnifiedPlan, but RemoteDescription is PlanB"). Only the first
// (primary) source should ever reach the rebuilt SDP.
func TestSDPFromJingleContentsSingleSSRCPerMid(t *testing.T) {
	contents := []xmpp.JingleContent{{
		Creator: "initiator",
		Name:    "video",
		Senders: "initiator",
		Description: &xmpp.RTPDescription{
			Media: "video",
			PayloadTypes: []xmpp.RTPPayloadType{
				{ID: 96, Name: "H264", ClockRate: 90000},
			},
			Sources: []xmpp.JingleSource{
				{SSRC: 111, Parameters: []xmpp.JingleSourceParam{{Name: "cname", Value: "peer"}}},
				{SSRC: 222, Parameters: []xmpp.JingleSourceParam{{Name: "cname", Value: "peer"}}}, // RTX repair SSRC
			},
		},
		Transport: &xmpp.ICEUDPTransport{
			Ufrag: "ufrag",
			Pwd:   "pwd",
			Fingerprint: &xmpp.DTLSFingerprint{
				Hash: "sha-256", Setup: "actpass", Value: "AA:BB",
			},
		},
	}}

	sdpDesc, err := sdpFromJingleContents(contents, webrtc.SDPTypeOffer, true)
	if err != nil {
		t.Fatalf("building sdp: %v", err)
	}
	if n := strings.Count(sdpDesc.SDP, "a=ssrc:"); n != 1 {
		t.Fatalf("rebuilt sdp has %d a=ssrc lines, want 1 (multiple unlinked SSRCs on one mid trips pion's Plan-B check):\n%s", n, sdpDesc.SDP)
	}
	if !strings.Contains(sdpDesc.SDP, "a=ssrc:111 ") {
		t.Fatalf("rebuilt sdp dropped the primary ssrc:\n%s", sdpDesc.SDP)
	}
	if strings.Contains(sdpDesc.SDP, "a=ssrc:222 ") {
		t.Fatalf("rebuilt sdp kept the repair ssrc:\n%s", sdpDesc.SDP)
	}
}

// TestJingleContentsFromSDPSingleSSRCPerContent is
// TestSDPFromJingleContentsSingleSSRCPerMid's counterpart for our own
// outgoing offers/answers: pion auto-negotiates RTX for a video track (it
// did for kage's own screen-share content-add live, unprompted) and so its
// generated SDP can carry two a=ssrc lines under one m= section - a primary
// and a repair SSRC, both sharing the same msid/cname, telling them apart
// only by an a=ssrc-group:FID line we don't parse. Emitting the repair SSRC
// as an ordinary second <source/> left at least one real peer (Conversations)
// confused about which SSRC was the actual video and reporting zero frames
// received. jingleContentsFromSDP must keep only the first SSRC per content.
func TestJingleContentsFromSDPSingleSSRCPerContent(t *testing.T) {
	const offerWithRTX = `v=0
o=- 1 1 IN IP4 0.0.0.0
s=-
t=0 0
a=fingerprint:sha-256 F5:3C:1E:9A:44:2B:70:D8:6C:11:A0:5F:93:2E:88:47:6D:CA:31:05:9B:E2:74:AF:60:13:8C:29:B7:4E:D1:52
a=group:BUNDLE 0
m=video 9 UDP/TLS/RTP/SAVPF 96 97
c=IN IP4 0.0.0.0
a=setup:actpass
a=mid:0
a=ice-ufrag:BCkjvJhCEcYkAWGP
a=ice-pwd:GVQhZbPTHmzDdWuJDeMdrHwYrEEJDMRF
a=rtcp-mux
a=rtpmap:96 H264/90000
a=fmtp:96 level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f
a=rtpmap:97 rtx/90000
a=fmtp:97 apt=96
a=ssrc-group:FID 2399846864 3594954464
a=ssrc:2399846864 cname:kage
a=ssrc:2399846864 msid:kage video
a=ssrc:3594954464 cname:kage
a=ssrc:3594954464 msid:kage video
a=sendonly
`
	contents, err := jingleContentsFromSDP(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerWithRTX}, true)
	if err != nil {
		t.Fatalf("translating offer to jingle: %v", err)
	}
	if len(contents) != 1 {
		t.Fatalf("got %d contents, want 1", len(contents))
	}
	sources := contents[0].Description.Sources
	if len(sources) != 1 {
		t.Fatalf("got %d sources, want 1 (RTX ssrc must be dropped): %+v", len(sources), sources)
	}
	if sources[0].SSRC != 2399846864 {
		t.Fatalf("kept ssrc %d, want the primary 2399846864", sources[0].SSRC)
	}
}
