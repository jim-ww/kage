package xmpp

import (
	"context"
	"encoding/xml"
	"strconv"
	"strings"
	"testing"

	"mellium.im/xmlstream"
	"mellium.im/xmpp/pubsub"
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

// publishDeviceListItemV1 publishes a raw legacy device-list item under an
// explicit pubsub item ID, bypassing PublishOmemoDeviceListV1's fixed
// omemoV1ItemID - this is what lets the test leave more than one item
// behind on the node, the way a foreign publisher (a different client
// implementation using its own random item IDs, or a server that doesn't
// collapse to a true singleton) could in the wild.
func publishDeviceListItemV1(ctx context.Context, c *Client, itemID string, deviceIDs []uint32) error {
	children := make([]xml.TokenReader, len(deviceIDs))
	for i, id := range deviceIDs {
		children[i] = xmlstream.Wrap(nil, xml.StartElement{
			Name: xml.Name{Space: omemoV1NS, Local: "device"},
			Attr: []xml.Attr{{Name: xml.Name{Local: "id"}, Value: strconv.FormatUint(uint64(id), 10)}},
		})
	}
	elem := xmlstream.Wrap(
		xmlstream.MultiReader(children...),
		xml.StartElement{Name: xml.Name{Space: omemoV1NS, Local: "list"}},
	)
	_, err := pubsub.Publish(ctx, c.session, omemoV1DevicesNode, itemID, elem)
	return err
}

// TestFetchOmemoDeviceListV1MultipleItems checks what happens when two items
// end up published to the same legacy-OMEMO devicelist node under different
// item IDs (PublishOmemoDeviceListV1 itself never does this - it always
// reuses omemoV1ItemID - but a foreign publisher speaking the same PEP node
// could). Prosody itself collapses this down to one item regardless (true
// PEP singleton behavior), so this doesn't reproduce a live union bug against
// our own devtest server - it's a regression test for FetchOmemoDeviceListV1
// keeping only the last-iterated item rather than merging every item's
// devices together, in case a differently-behaved PEP service ever does hand
// back more than one item.
func TestFetchOmemoDeviceListV1MultipleItems(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()

	alice, err := Dial(ctx, "alice@localhost", "alicepw", tlsConfig)
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}
	defer alice.Close()
	bob, err := Dial(ctx, "bob@localhost", "bobpw", tlsConfig)
	if err != nil {
		t.Fatalf("dial bob: %v", err)
	}
	defer bob.Close()

	if err := publishDeviceListItemV1(ctx, alice, "stale-item", []uint32{111, 222}); err != nil {
		t.Fatalf("publish stale item: %v", err)
	}
	if err := publishDeviceListItemV1(ctx, alice, "current-item", []uint32{999}); err != nil {
		t.Fatalf("publish current item: %v", err)
	}

	got, err := bob.FetchOmemoDeviceListV1(ctx, "alice@localhost")
	if err != nil {
		t.Fatalf("FetchOmemoDeviceListV1: %v", err)
	}
	t.Logf("fetched devices after two-item publish: %v", got.Devices)

	// The only correct outcome per PEP singleton-node semantics is exactly
	// the most recently published item's device: [999]. Today's
	// merge-everything code will instead return [111 222 999] if the server
	// actually kept both items around.
	if len(got.Devices) != 1 || got.Devices[0] != 999 {
		t.Errorf("FetchOmemoDeviceListV1 returned %v, want exactly [999] (latest item only) - "+
			"this reproduces the union bug if the server retained both items", got.Devices)
	}
}
