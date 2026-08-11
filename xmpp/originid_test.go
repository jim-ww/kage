package xmpp

import (
	"encoding/xml"
	"strings"
	"testing"
)

// decodeMessageBody is a small helper shared by the tests below: decode a
// raw <message/> stanza into a messageBody, the same way handleStanza does.
func decodeMessageBody(t *testing.T, raw string) messageBody {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(raw))
	var msg messageBody
	tok, err := d.Token()
	if err != nil {
		t.Fatal(err)
	}
	start := tok.(xml.StartElement)
	if err := d.DecodeElement(&msg, &start); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return msg
}

// TestSelfIDPrefersOriginID verifies XEP-0359: when a message carries an
// <origin-id>, selfID() (what we use to target it with a later
// reply/correction/retraction) prefers that over the bare stanza id
// attribute, since the id attribute isn't guaranteed to survive
// forwarding/archiving unmodified while origin-id is.
func TestSelfIDPrefersOriginID(t *testing.T) {
	raw := `<message xmlns="jabber:client" from="juliet@capulet.lit/balcony" to="romeo@montague.lit" id="stanza-id-only-locally-unique" type="chat">
  <body>Wherefore art thou, Romeo?</body>
  <origin-id xmlns="urn:xmpp:sid:0" id="origin-id-globally-stable"/>
</message>`

	msg := decodeMessageBody(t, raw)
	if got, want := msg.selfID(), "origin-id-globally-stable"; got != want {
		t.Errorf("selfID() = %q, want %q", got, want)
	}
}

// TestSelfIDFallsBackToStanzaID verifies a message with no <origin-id>
// (most servers/older clients) still resolves selfID() to the plain
// stanza id attribute, same as before XEP-0359 support was added.
func TestSelfIDFallsBackToStanzaID(t *testing.T) {
	raw := `<message xmlns="jabber:client" from="juliet@capulet.lit/balcony" to="romeo@montague.lit" id="plain-stanza-id" type="chat">
  <body>Wherefore art thou, Romeo?</body>
</message>`

	msg := decodeMessageBody(t, raw)
	if got, want := msg.selfID(), "plain-stanza-id"; got != want {
		t.Errorf("selfID() = %q, want %q", got, want)
	}
}
