package xmpp

import (
	"encoding/xml"
	"strings"
	"testing"
)

// conversationsVideoDescription is a video <description/> in the shape
// libwebrtc-based clients (Conversations) send: per-codec RTCP feedback,
// header extensions, and an RTX stream tied to the primary by an FID group.
const conversationsVideoDescription = `<description xmlns="urn:xmpp:jingle:apps:rtp:1" media="video">
	<payload-type id="96" name="VP8" clockrate="90000">
		<rtcp-fb xmlns="urn:xmpp:jingle:apps:rtp:rtcp-fb:0" type="nack"/>
		<rtcp-fb xmlns="urn:xmpp:jingle:apps:rtp:rtcp-fb:0" type="nack" subtype="pli"/>
		<rtcp-fb xmlns="urn:xmpp:jingle:apps:rtp:rtcp-fb:0" type="transport-cc"/>
	</payload-type>
	<payload-type id="97" name="rtx" clockrate="90000">
		<parameter name="apt" value="96"/>
	</payload-type>
	<rtcp-fb xmlns="urn:xmpp:jingle:apps:rtp:rtcp-fb:0" type="ccm" subtype="fir"/>
	<rtp-hdrext xmlns="urn:xmpp:jingle:apps:rtp:rtp-hdrext:0" id="3" uri="http://www.ietf.org/id/draft-holmer-rmcat-transport-wide-cc-extensions-01"/>
	<rtp-hdrext xmlns="urn:xmpp:jingle:apps:rtp:rtp-hdrext:0" id="5" uri="urn:3gpp:video-orientation" senders="initiator"/>
	<extmap-allow-mixed xmlns="urn:xmpp:jingle:apps:rtp:rtp-hdrext:0"/>
	<source xmlns="urn:xmpp:jingle:apps:rtp:ssma:0" ssrc="111"><parameter name="cname" value="peer"/></source>
	<source xmlns="urn:xmpp:jingle:apps:rtp:ssma:0" ssrc="222"><parameter name="cname" value="peer"/></source>
	<ssrc-group xmlns="urn:xmpp:jingle:apps:rtp:ssma:0" semantics="FID">
		<source ssrc="111"/>
		<source ssrc="222"/>
	</ssrc-group>
	<rtcp-mux/>
</description>`

// TestRTPDescriptionFeedbackAndExtensions checks XEP-0293 feedback,
// XEP-0294 header extensions and XEP-0339 source groups decode, and encode
// back in their own namespaces.
func TestRTPDescriptionFeedbackAndExtensions(t *testing.T) {
	var d RTPDescription
	if err := xml.Unmarshal([]byte(conversationsVideoDescription), &d); err != nil {
		t.Fatal(err)
	}
	if fb := d.PayloadTypes[0].Feedback; len(fb) != 3 || fb[1] != (RTCPFeedback{Type: "nack", Subtype: "pli"}) {
		t.Errorf("VP8 feedback decoded as %+v", fb)
	}
	if len(d.Feedback) != 1 || d.Feedback[0] != (RTCPFeedback{Type: "ccm", Subtype: "fir"}) {
		t.Errorf("description-level feedback decoded as %+v", d.Feedback)
	}
	if len(d.HeaderExtensions) != 2 || d.HeaderExtensions[0].ID != 3 || d.HeaderExtensions[1].Senders != "initiator" {
		t.Errorf("header extensions decoded as %+v", d.HeaderExtensions)
	}
	if d.ExtmapAllowMixed == nil {
		t.Error("extmap-allow-mixed lost")
	}
	if len(d.SourceGroups) != 1 || d.SourceGroups[0].Semantics != "FID" || len(d.SourceGroups[0].Sources) != 2 || d.SourceGroups[0].Sources[1].SSRC != 222 {
		t.Errorf("source groups decoded as %+v", d.SourceGroups)
	}

	out, err := xml.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<rtcp-fb xmlns="urn:xmpp:jingle:apps:rtp:rtcp-fb:0" type="nack" subtype="pli">`,
		`<rtp-hdrext xmlns="urn:xmpp:jingle:apps:rtp:rtp-hdrext:0" id="5" uri="urn:3gpp:video-orientation" senders="initiator">`,
		`<extmap-allow-mixed xmlns="urn:xmpp:jingle:apps:rtp:rtp-hdrext:0">`,
		`<ssrc-group xmlns="urn:xmpp:jingle:apps:rtp:ssma:0" semantics="FID"><source xmlns="urn:xmpp:jingle:apps:rtp:ssma:0" ssrc="111">`,
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("encoded description lacks %s:\n%s", want, out)
		}
	}
}
