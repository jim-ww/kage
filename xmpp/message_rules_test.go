package xmpp

import (
	"bytes"
	"encoding/xml"
	"slices"
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

// TestReceiptsNotForCarbons checks a receipt request is answered on a
// message delivered to us, and on no carbon of one: a received carbon's
// device answers it, and a sent carbon is ours (XEP-0280 §10.4).
func TestReceiptsNotForCarbons(t *testing.T) {
	direct := `<message xmlns="jabber:client" from="bob@example.test/pc" to="alice@example.test/laptop" type="chat" id="m2">
		<body>hi alice</body><request xmlns="urn:xmpp:receipts"/>
	</message>`
	if reply, _ := serveMessage(t, direct); !strings.Contains(reply, `<received xmlns="urn:xmpp:receipts" id="m2">`) {
		t.Errorf("no receipt for a message to us: %q", reply)
	}

	received := `<message xmlns="jabber:client" from="alice@example.test" to="alice@example.test/laptop">
		<received xmlns="urn:xmpp:carbons:2"><forwarded xmlns="urn:xmpp:forward:0">
			` + direct + `
		</forwarded></received>
	</message>`
	for name, payload := range map[string]string{"received": received, "sent": sentCarbon("alice@example.test")} {
		if reply, _ := serveMessage(t, payload); strings.Contains(reply, "receipts") {
			t.Errorf("receipt sent for a %s carbon: %q", name, reply)
		}
	}
}

// TestMessageFeaturesAdvertised pins the message extensions kage handles
// coming in, each of whose XEPs has an entity supporting it advertise so
// (0184 §6, 0308 §2, 0424 §2, 0444 §2.1, 0461 §2, 0066 §4; 0428 §2.1 makes
// it optional): clients that check before sending won't otherwise.
func TestMessageFeaturesAdvertised(t *testing.T) {
	for _, want := range []string{
		"http://jabber.org/protocol/chatstates",
		"urn:xmpp:receipts",
		"urn:xmpp:message-correct:0",
		"urn:xmpp:message-retract:1",
		"urn:xmpp:reactions:0",
		"urn:xmpp:reply:0",
		"urn:xmpp:fallback:0",
		"jabber:x:oob",
	} {
		if !slices.Contains(discoFeatures, want) {
			t.Errorf("discoFeatures is missing %q", want)
		}
	}
}
