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

// TestMAMFinCompleteDecode pins the shape decodeMAMFin has to cope with: the
// whole response <iq>, since that is what Session.SendIQ returns. Reading
// complete off that element directly (rather than the nested <fin>) silently
// yields false for every page any server ever sends, which left MAM paging
// running past the end of the archive into the empty-page recovery path on
// every single sync.
func TestMAMFinCompleteDecode(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want bool
	}{
		{
			name: "complete",
			raw: `<iq xmlns="jabber:client" type="result" id="q1" from="me@example.com" to="me@example.com/res">
  <fin xmlns="urn:xmpp:mam:2" complete="true">
    <set xmlns="http://jabber.org/protocol/rsm"><first index="0">a1</first><last>a9</last></set>
  </fin>
</iq>`,
			want: true,
		},
		{
			name: "more pages to come",
			raw: `<iq xmlns="jabber:client" type="result" id="q2" from="me@example.com" to="me@example.com/res">
  <fin xmlns="urn:xmpp:mam:2">
    <set xmlns="http://jabber.org/protocol/rsm"><first index="0">b1</first><last>b9</last></set>
  </fin>
</iq>`,
			want: false,
		},
		{
			name: "empty archive is still complete",
			raw:  `<iq xmlns="jabber:client" type="result" id="q3"><fin xmlns="urn:xmpp:mam:2" complete="true"><set xmlns="http://jabber.org/protocol/rsm"/></fin></iq>`,
			want: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeMAMFin(xml.NewDecoder(strings.NewReader(tc.raw)))
			if err != nil {
				t.Fatalf("decodeMAMFin: %v", err)
			}
			if got != tc.want {
				t.Errorf("complete = %v, want %v", got, tc.want)
			}
		})
	}
}
