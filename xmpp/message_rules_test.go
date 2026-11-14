package xmpp

import (
	"bytes"
	"encoding/xml"
	"strings"
	"sync"
	"testing"

	"mellium.im/xmpp/jid"
)

// serveMessage runs handleStanza on an incoming <message/> as alice's
// client would, returning what it wrote back and the events it emitted.
func serveMessage(t *testing.T, payload string) (reply string, events []Event) {
	t.Helper()
	c := &Client{JID: jid.MustParse("alice@example.test/laptop")}
	c.evCond = sync.NewCond(&c.evMu)
	dec := xml.NewDecoder(strings.NewReader(payload))
	tok, err := dec.Token()
	if err != nil {
		t.Fatalf("reading payload start: %v", err)
	}
	start := tok.(xml.StartElement)
	var out bytes.Buffer
	enc := xml.NewEncoder(&out)
	c.handleStanza(iqStream{dec, enc}, &start)
	if err := enc.Flush(); err != nil {
		t.Fatal(err)
	}
	return out.String(), c.evBuf
}

func messages(events []Event) []MessageEvent {
	var out []MessageEvent
	for _, ev := range events {
		if me, ok := ev.(MessageEvent); ok {
			out = append(out, me)
		}
	}
	return out
}

// sentCarbon is a carbon of a message alice sent bob from another device,
// forwarded by wrapperFrom.
func sentCarbon(wrapperFrom string) string {
	from := ""
	if wrapperFrom != "" {
		from = ` from="` + wrapperFrom + `"`
	}
	return `<message xmlns="jabber:client"` + from + ` to="alice@example.test/laptop">
		<sent xmlns="urn:xmpp:carbons:2"><forwarded xmlns="urn:xmpp:forward:0">
			<message xmlns="jabber:client" from="alice@example.test/phone" to="bob@example.test" type="chat" id="m1">
				<body>hi bob</body>
				<request xmlns="urn:xmpp:receipts"/>
			</message>
		</forwarded></sent>
	</message>`
}

// TestCarbonsOnlyFromOwnAccount checks a carbon is taken in only from our
// own bare JID (or the server, sending with no from): from anyone else it
// would let them put words in someone's mouth (XEP-0280 §11).
func TestCarbonsOnlyFromOwnAccount(t *testing.T) {
	for _, from := range []string{"alice@example.test", ""} {
		if _, events := serveMessage(t, sentCarbon(from)); len(messages(events)) != 1 {
			t.Errorf("carbon from %q: %d messages, want it taken in", from, len(messages(events)))
		}
	}
	for _, from := range []string{"mallory@example.test", "mallory@example.test/x", "alice@example.test/phone", "example.test"} {
		if _, events := serveMessage(t, sentCarbon(from)); len(messages(events)) != 0 {
			t.Errorf("carbon from %q taken in: %+v", from, messages(events))
		}
	}
}
