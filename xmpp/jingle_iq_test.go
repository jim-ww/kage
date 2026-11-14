package xmpp

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"

	"mellium.im/xmpp/jid"
	"mellium.im/xmpp/stanza"
)

// iqStream is the xmlstream.TokenReadEncoder a handler gets: tokens of the
// IQ's payload in, whatever the handler writes back out.
type iqStream struct {
	*xml.Decoder
	*xml.Encoder
}

// serveJingleIQ runs handleJingleIQ on payload, returning what it wrote back
// and the event it emitted, if any.
func serveJingleIQ(t *testing.T, payload string) (reply string, ev Event) {
	t.Helper()
	c := &Client{events: make(chan Event, 1)}
	dec := xml.NewDecoder(strings.NewReader(payload))
	tok, err := dec.Token()
	if err != nil {
		t.Fatalf("reading payload start: %v", err)
	}
	start := tok.(xml.StartElement)
	var out bytes.Buffer
	enc := xml.NewEncoder(&out)
	iq := stanza.IQ{ID: "1", Type: stanza.SetIQ, From: jid.MustParse("peer@example.test/phone")}
	if err := c.handleJingleIQ(iq, iqStream{dec, enc}, &start); err != nil {
		t.Fatalf("handleJingleIQ: %v", err)
	}
	if err := enc.Flush(); err != nil {
		t.Fatal(err)
	}
	select {
	case ev = <-c.events:
	default:
	}
	return out.String(), ev
}

func TestJingleContentModifyDeclinedWithError(t *testing.T) {
	reply, ev := serveJingleIQ(t, `<jingle xmlns="urn:xmpp:jingle:1" action="content-modify" sid="s1">
		<content creator="initiator" name="1" senders="both"/>
	</jingle>`)
	if !strings.Contains(reply, `type="error"`) || !strings.Contains(reply, "feature-not-implemented") {
		t.Errorf("reply = %s, want a feature-not-implemented IQ error", reply)
	}
	if ev != nil {
		t.Errorf("emitted %#v for a declined content-modify", ev)
	}
}

func TestJingleIQAcknowledged(t *testing.T) {
	reply, ev := serveJingleIQ(t, `<jingle xmlns="urn:xmpp:jingle:1" action="content-add" sid="s1">
		<content creator="initiator" name="1"/>
	</jingle>`)
	if !strings.Contains(reply, `type="result"`) {
		t.Errorf("reply = %s, want an IQ result", reply)
	}
	je, ok := ev.(JingleEvent)
	if !ok || je.Jingle.Action != JingleActionContentAdd || je.Jingle.SID != "s1" {
		t.Errorf("event = %#v, want the content-add", ev)
	}
}

func TestJingleSessionInfoMuteDecoded(t *testing.T) {
	_, ev := serveJingleIQ(t, `<jingle xmlns="urn:xmpp:jingle:1" action="session-info" sid="s1">
		<mute xmlns="urn:xmpp:jingle:apps:rtp:info:1" creator="initiator" name="1"/>
	</jingle>`)
	je, ok := ev.(JingleEvent)
	if !ok || je.Jingle.Action != JingleActionSessionInfo {
		t.Fatalf("event = %#v, want the session-info", ev)
	}
	if je.Jingle.Mute == nil || je.Jingle.Mute.Name != "1" || je.Jingle.Unmute != nil {
		t.Errorf("Mute = %#v, Unmute = %#v, want a mute of content 1", je.Jingle.Mute, je.Jingle.Unmute)
	}
}

func TestJingleMuteEncoded(t *testing.T) {
	out, err := xml.Marshal(JingleIQ{Action: JingleActionSessionInfo, SID: "s1", Mute: &RTPInfo{Creator: "initiator", Name: "1"}})
	if err != nil {
		t.Fatal(err)
	}
	want := `<mute xmlns="urn:xmpp:jingle:apps:rtp:info:1" creator="initiator" name="1"></mute>`
	if !strings.Contains(string(out), want) {
		t.Errorf("encoded %s, want it to contain %s", out, want)
	}
	if strings.Contains(string(out), "unmute") || strings.Contains(string(out), "<reason") {
		t.Errorf("encoded %s, want no unset elements", out)
	}
}
