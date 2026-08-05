package xmpp

import (
	"encoding/xml"
	"strings"
	"testing"
)

// TestPubsubDeviceListEventDecode guards the wire-level parsing
// handleStanza's DeviceListChangedEvent dispatch depends on: without this,
// a peer's XEP-0163 PEP push notifying us their OMEMO device list changed
// (e.g. a new device added, or an old one dropped after a wipe) is silently
// ignored, since devicesFor (omemo-go) otherwise only ever refreshes a
// cached device list when it's completely empty or every currently-known
// device has failed - a single still-working stale device masks the rest
// forever. See account.go's DeviceListChangedEvent handling.
func TestPubsubDeviceListEventDecode(t *testing.T) {
	tests := []struct {
		name     string
		xml      string
		wantNode string
	}{
		{
			name: "omemo2 device list push",
			xml: `<message xmlns="jabber:client" from="bob@example.com" to="alice@example.com">
  <event xmlns="http://jabber.org/protocol/pubsub#event">
    <items node="urn:xmpp:omemo:2:devices">
      <item id="current">
        <devices xmlns="urn:xmpp:omemo:2"><device id="1234"/></devices>
      </item>
    </items>
  </event>
</message>`,
			wantNode: omemoDevicesNode,
		},
		{
			name: "legacy omemo device list push",
			xml: `<message xmlns="jabber:client" from="bob@example.com" to="alice@example.com">
  <event xmlns="http://jabber.org/protocol/pubsub#event">
    <items node="eu.siacs.conversations.axolotl.devicelist">
      <item id="current">
        <list xmlns="eu.siacs.conversations.axolotl"><device id="5678"/></list>
      </item>
    </items>
  </event>
</message>`,
			wantNode: omemoV1DevicesNode,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := xml.NewDecoder(strings.NewReader(tt.xml))
			var msg messageBody
			tok, err := d.Token()
			if err != nil {
				t.Fatal(err)
			}
			start := tok.(xml.StartElement)
			if err := d.DecodeElement(&msg, &start); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if msg.PubsubEvent == nil || msg.PubsubEvent.Items == nil {
				t.Fatal("PubsubEvent.Items is nil")
			}
			if got := msg.PubsubEvent.Items.Node; got != tt.wantNode {
				t.Errorf("Items.Node = %q, want %q", got, tt.wantNode)
			}
		})
	}
}
