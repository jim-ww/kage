package xmpp

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestMAMResultDecode(t *testing.T) {
	raw := `<message xmlns="jabber:client" from="mam.example.com" to="me@example.com/res" id="aeb213">
  <result xmlns="urn:xmpp:mam:2" queryid="f27" id="28482-98726-73623">
    <forwarded xmlns="urn:xmpp:forward:0">
      <delay xmlns="urn:xmpp:delay" stamp="2010-07-10T23:08:25Z"/>
      <message xmlns="jabber:client" from="juliet@capulet.lit/balcony" to="romeo@montague.lit" id="hgn27" type="chat">
        <body>Wherefore art thou, Romeo?</body>
      </message>
    </forwarded>
  </result>
</message>`

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
	if msg.MAMResult == nil {
		t.Fatal("MAMResult is nil")
	}
	if msg.MAMResult.ID != "28482-98726-73623" {
		t.Errorf("archive id = %q", msg.MAMResult.ID)
	}
	if msg.MAMResult.Forwarded.Delay.Stamp != "2010-07-10T23:08:25Z" {
		t.Errorf("delay stamp = %q", msg.MAMResult.Forwarded.Delay.Stamp)
	}
	inner := msg.MAMResult.Forwarded.Message
	if inner.From.String() != "juliet@capulet.lit/balcony" {
		t.Errorf("inner from = %q", inner.From.String())
	}
	if inner.Body != "Wherefore art thou, Romeo?" {
		t.Errorf("inner body = %q", inner.Body)
	}
	if inner.ID != "hgn27" {
		t.Errorf("inner id = %q", inner.ID)
	}
}
