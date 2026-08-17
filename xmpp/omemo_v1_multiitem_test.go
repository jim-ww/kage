package xmpp

import (
	"encoding/xml"
	"strings"
	"testing"
)

// TestOmemoV1ListElemDecodeKeepsOnlyLastItem is a network-free unit test for
// the exact decode-and-keep-last-item logic FetchOmemoDeviceListV1 runs
// per-item: given two device-list item payloads (as if two items had been
// iterated from a pubsub response), only the second's devices should
// survive, mirroring the loop body in omemo_v1.go.
func TestOmemoV1ListElemDecodeKeepsOnlyLastItem(t *testing.T) {
	items := []string{
		`<list xmlns="eu.siacs.conversations.axolotl"><device id="111"/><device id="222"/></list>`,
		`<list xmlns="eu.siacs.conversations.axolotl"><device id="999"/></list>`,
	}

	var ids []uint32
	for _, raw := range items {
		var list omemoV1ListElem
		if err := xml.NewDecoder(strings.NewReader(raw)).Decode(&list); err != nil {
			t.Fatalf("decode item: %v", err)
		}
		ids = ids[:0]
		for _, d := range list.Devices {
			ids = append(ids, d.ID)
		}
	}

	if len(ids) != 1 || ids[0] != 999 {
		t.Fatalf("got %v, want exactly [999] (only the last item's devices)", ids)
	}
}

// publishDeviceListItemV1 and TestFetchOmemoDeviceListV1MultipleItems live in
// omemo_v1_multiitem_live_test.go (build tag integration) - they need a real
// Prosody instance.
