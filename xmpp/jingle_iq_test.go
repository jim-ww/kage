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
