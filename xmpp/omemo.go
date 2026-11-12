package xmpp

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"mellium.im/xmlstream"
	"mellium.im/xmpp"
	"mellium.im/xmpp/jid"
	"mellium.im/xmpp/pubsub"
	"mellium.im/xmpp/stanza"

	omemolib "github.com/jim-ww/omemo-go"
)

// XEP-0384 (OMEMO 2): device list + bundle exchange via PEP, and the
// <encrypted/> element carried inside a normal <message/>. The double
// ratchet/X3DH crypto itself lives in crypto/omemo (github.com/jim-ww/omemo-go);
// this file only does wire format — building/parsing stanzas and PEP items,
// mirroring pep.go's XEP-0373 pattern.
const (
	omemoNS               = "urn:xmpp:omemo:2"
	omemoDevicesNode      = "urn:xmpp:omemo:2:devices"
	omemoBundleNodePrefix = "urn:xmpp:omemo:2:bundles:"
	omemoItemID           = "current"
)

// omemoWire is what tells the two OMEMO protocols' PEP nodes apart.
type omemoWire struct {
	ns, devicesNode, listElem, bundleNodePrefix, itemID, name string
}

var (
	omemoWireV2 = omemoWire{omemoNS, omemoDevicesNode, "devices", omemoBundleNodePrefix, omemoItemID, "omemo"}
	omemoWireV1 = omemoWire{omemoV1NS, omemoV1DevicesNode, "list", omemoV1BundleNodePrefix, omemoV1ItemID, "legacy omemo"}
)

func (w omemoWire) bundleNode(id omemolib.DeviceID) string {
	return w.bundleNodePrefix + strconv.FormatUint(uint64(id), 10)
}

// ── Device list ──────────────────────────────────────────────────────────

// omemoDeviceListElem decodes either protocol's device list - <devices/>
// or <list/> - by its <device/> children alone.
type omemoDeviceListElem struct {
	Devices []struct {
		ID uint32 `xml:"id,attr"`
	} `xml:"device"`
}

// PublishOmemoDeviceList publishes list to our own PEP device-list node.
func (c *Client) PublishOmemoDeviceList(ctx context.Context, list omemolib.DeviceList) error {
	return c.publishOmemoDeviceList(ctx, omemoWireV2, list)
}

// FetchOmemoDeviceList fetches peerJID's published device list.
func (c *Client) FetchOmemoDeviceList(ctx context.Context, peerJID string) (omemolib.DeviceList, error) {
	return c.fetchOmemoDeviceList(ctx, omemoWireV2, peerJID)
}

func (c *Client) publishOmemoDeviceList(ctx context.Context, w omemoWire, list omemolib.DeviceList) error {
	children := make([]xml.TokenReader, len(list.Devices))
	for i, id := range list.Devices {
		children[i] = xmlstream.Wrap(nil, xml.StartElement{
			Name: xml.Name{Space: w.ns, Local: "device"},
			Attr: []xml.Attr{{Name: xml.Name{Local: "id"}, Value: strconv.FormatUint(uint64(id), 10)}},
		})
	}
	elem := xmlstream.Wrap(
		xmlstream.MultiReader(children...),
		xml.StartElement{Name: xml.Name{Space: w.ns, Local: w.listElem}},
	)
	if _, err := pubsub.Publish(ctx, c.session, w.devicesNode, w.itemID, elem); err != nil {
		return fmt.Errorf("publishing %s device list: %w", w.name, err)
	}
	c.makeNodeOpen(ctx, w.devicesNode)
	return nil
}

func (c *Client) fetchOmemoDeviceList(ctx context.Context, w omemoWire, peerJID string) (omemolib.DeviceList, error) {
	peer, err := jid.Parse(peerJID)
	if err != nil {
		return omemolib.DeviceList{}, fmt.Errorf("parsing peer jid %q: %w", peerJID, err)
	}

	iter := pubsub.FetchIQ(ctx, stanza.IQ{To: peer}, c.session, pubsub.Query{Node: w.devicesNode})
	defer iter.Close()

	// This is a PEP node: XEP-0163 mandates a singleton item (the service is
	// supposed to enforce max_items=1), so there is exactly one *current*
	// device list, ever. A well-behaved service like Prosody only ever
	// returns one item regardless of how many item IDs were published under,
	// but a service that doesn't enforce that (or has leftover items from
	// before a max_items config change) could return several - keep only the
	// last one iterated rather than merging every item's devices together,
	// or a single stale leftover item reintroduces long-dead device IDs
	// forever.
	var ids []omemolib.DeviceID
	for iter.Next() {
		_, r := iter.Item()
		var list omemoDeviceListElem
		if err := xml.NewTokenDecoder(r).Decode(&list); err != nil {
			slog.Warn("fetching omemo device list: failed to decode item", "protocol", w.name, "peer", peerJID, "err", err)
			continue
		}
		ids = ids[:0]
		for _, d := range list.Devices {
			ids = append(ids, omemolib.DeviceID(d.ID))
		}
	}
	if err := iter.Err(); err != nil {
		// Treat "item-not-found" / "Node not found" as empty list (first-time
		// setup, no device-list node published yet). Anything else (timeout,
		// service-unavailable, not-authorized, ...) is a real fetch failure
		// and must propagate - otherwise it gets cached as "peer has zero
		// devices" and every future send silently fails with ErrNoRecipients.
		if !strings.Contains(err.Error(), "item-not-found") && !strings.Contains(err.Error(), "Node not found") {
			return omemolib.DeviceList{}, fmt.Errorf("fetching %s device list for %s: %w", w.name, peerJID, err)
		}
	}
	slog.Debug("fetched omemo device list", "protocol", w.name, "peer", peerJID, "count", len(ids), "devices", ids)
	return omemolib.DeviceList{JID: peerJID, Devices: ids}, nil
}

// ── Bundle ───────────────────────────────────────────────────────────────

type omemoBundleElem struct {
	XMLName      xml.Name `xml:"urn:xmpp:omemo:2 bundle"`
	IdentityKey  string   `xml:"urn:xmpp:omemo:2 ik"`
	SignedPreKey struct {
		ID     uint32 `xml:"id,attr"`
		Public string `xml:",chardata"`
	} `xml:"urn:xmpp:omemo:2 spk"`
	SignedPreKeySig string `xml:"urn:xmpp:omemo:2 spks"`
	PreKeys         []struct {
		ID     uint32 `xml:"id,attr"`
		Public string `xml:",chardata"`
	} `xml:"urn:xmpp:omemo:2 prekeys>pk"`
}

// PublishOmemoBundle publishes bundle to our own PEP bundle node for its
// device ID.
func (c *Client) PublishOmemoBundle(ctx context.Context, bundle omemolib.Bundle) error {
	pkElems := make([]xml.TokenReader, len(bundle.PreKeys))
	for i, pk := range bundle.PreKeys {
		pkElems[i] = xmlstream.Wrap(
			xmlstream.Token(xml.CharData(base64.StdEncoding.EncodeToString(pk.Public))),
			xml.StartElement{
				Name: xml.Name{Space: omemoNS, Local: "pk"},
				Attr: []xml.Attr{{Name: xml.Name{Local: "id"}, Value: strconv.FormatUint(uint64(pk.ID), 10)}},
			},
		)
	}

	elem := xmlstream.Wrap(
		xmlstream.MultiReader(
			xmlstream.Wrap(
				xmlstream.Token(xml.CharData(base64.StdEncoding.EncodeToString(bundle.IdentityKey))),
				xml.StartElement{Name: xml.Name{Space: omemoNS, Local: "ik"}},
			),
			xmlstream.Wrap(
				xmlstream.Token(xml.CharData(base64.StdEncoding.EncodeToString(bundle.SignedPreKey.Public))),
				xml.StartElement{
					Name: xml.Name{Space: omemoNS, Local: "spk"},
					Attr: []xml.Attr{{Name: xml.Name{Local: "id"}, Value: strconv.FormatUint(uint64(bundle.SignedPreKey.ID), 10)}},
				},
			),
			xmlstream.Wrap(
				xmlstream.Token(xml.CharData(base64.StdEncoding.EncodeToString(bundle.SignedPreKey.Signature))),
				xml.StartElement{Name: xml.Name{Space: omemoNS, Local: "spks"}},
			),
			xmlstream.Wrap(
				xmlstream.MultiReader(pkElems...),
				xml.StartElement{Name: xml.Name{Space: omemoNS, Local: "prekeys"}},
			),
		),
		xml.StartElement{Name: xml.Name{Space: omemoNS, Local: "bundle"}},
	)

	node := omemoWireV2.bundleNode(bundle.Device.ID)
	if _, err := pubsub.Publish(ctx, c.session, node, omemoItemID, elem); err != nil {
		return fmt.Errorf("publishing omemo bundle: %w", err)
	}
	c.makeNodeOpen(ctx, node)
	return nil
}

// FetchOmemoBundle fetches dev's published bundle.
func (c *Client) FetchOmemoBundle(ctx context.Context, dev omemolib.Device) (omemolib.Bundle, error) {
	var b omemoBundleElem
	if err := c.fetchOmemoBundle(ctx, omemoWireV2, dev, &b); err != nil {
		return omemolib.Bundle{}, err
	}
	return bundleFromElem(dev, b, base64.StdEncoding.DecodeString)
}

// fetchOmemoBundle decodes dev's published bundle into b.
func (c *Client) fetchOmemoBundle(ctx context.Context, w omemoWire, dev omemolib.Device, b any) error {
	peer, err := jid.Parse(dev.JID)
	if err != nil {
		return fmt.Errorf("parsing peer jid %q: %w", dev.JID, err)
	}
	iter := pubsub.FetchIQ(ctx, stanza.IQ{To: peer}, c.session, pubsub.Query{Node: w.bundleNode(dev.ID)})
	defer iter.Close()
	if !iter.Next() {
		if err := iter.Err(); err != nil {
			return fmt.Errorf("fetching %s bundle for %s/%d: %w", w.name, dev.JID, dev.ID, err)
		}
		return fmt.Errorf("no %s bundle published for %s/%d", w.name, dev.JID, dev.ID)
	}
	_, r := iter.Item()
	if err := xml.NewTokenDecoder(r).Decode(b); err != nil {
		return fmt.Errorf("decoding %s bundle from %s: %w", w.name, dev.JID, err)
	}
	return nil
}

// bundleFromElem reads dev's bundle out of b, its public keys decoded by
// key.
func bundleFromElem(dev omemolib.Device, b omemoBundleElem, key func(string) ([]byte, error)) (omemolib.Bundle, error) {
	ik, err := key(b.IdentityKey)
	if err != nil {
		return omemolib.Bundle{}, fmt.Errorf("decoding identity key: %w", err)
	}
	spkPub, err := key(b.SignedPreKey.Public)
	if err != nil {
		return omemolib.Bundle{}, fmt.Errorf("decoding signed prekey: %w", err)
	}
	spkSig, err := base64.StdEncoding.DecodeString(b.SignedPreKeySig)
	if err != nil {
		return omemolib.Bundle{}, fmt.Errorf("decoding signed prekey signature: %w", err)
	}
	preKeys := make([]omemolib.PreKey, 0, len(b.PreKeys))
	for _, pk := range b.PreKeys {
		if pub, err := key(pk.Public); err == nil {
			preKeys = append(preKeys, omemolib.PreKey{ID: pk.ID, Public: pub})
		}
	}
	return omemolib.Bundle{
		Device:      dev,
		IdentityKey: ik,
		SignedPreKey: omemolib.SignedPreKey{
			ID:        b.SignedPreKey.ID,
			Public:    spkPub,
			Signature: spkSig,
		},
		PreKeys: preKeys,
	}, nil
}

// ── <encrypted/> message element ─────────────────────────────────────────

type omemoKeyElem struct {
	RID          uint32 `xml:"rid,attr"`
	Kex          bool   `xml:"kex,attr,omitempty"`
	IdentityKey  string `xml:"ik,attr,omitempty"`
	EphemeralKey string `xml:"ek,attr,omitempty"`
	SPKID        uint32 `xml:"spkid,attr,omitempty"`
	PKID         uint32 `xml:"pkid,attr,omitempty"`
	Data         string `xml:",chardata"`
}

type omemoHeaderElem struct {
	SID  uint32         `xml:"sid,attr"`
	Keys []omemoKeyElem `xml:"urn:xmpp:omemo:2 key"`
}

// OmemoEncryptedElem is our wire encoding of an omemolib.EncryptedMessage.
type OmemoEncryptedElem struct {
	XMLName xml.Name        `xml:"urn:xmpp:omemo:2 encrypted"`
	Header  omemoHeaderElem `xml:"urn:xmpp:omemo:2 header"`
	Payload string          `xml:"urn:xmpp:omemo:2 payload,omitempty"`
}

// EncodeOmemoMessage converts msg into its wire element for embedding in an
// outgoing <message/> stanza.
func EncodeOmemoMessage(msg *omemolib.EncryptedMessage) *OmemoEncryptedElem {
	elem := &OmemoEncryptedElem{
		Header: omemoHeaderElem{SID: uint32(msg.Sender.ID)},
	}
	if msg.Payload != nil {
		elem.Payload = base64.StdEncoding.EncodeToString(msg.Payload)
	}
	for _, k := range msg.Keys {
		ke := omemoKeyElem{
			RID:  uint32(k.Device),
			Data: base64.StdEncoding.EncodeToString(k.Data),
		}
		if k.KeyExchange != nil {
			ke.Kex = true
			ke.IdentityKey = base64.StdEncoding.EncodeToString(k.KeyExchange.IdentityKey)
			ke.EphemeralKey = base64.StdEncoding.EncodeToString(k.KeyExchange.EphemeralKey)
			ke.SPKID = k.KeyExchange.SignedPreKeyID
			ke.PKID = k.KeyExchange.PreKeyID
		}
		elem.Header.Keys = append(elem.Header.Keys, ke)
	}
	return elem
}

// DecodeOmemoMessage converts a received wire element (sender is the bare/
// full JID the enclosing <message/> came from) back into an
// omemolib.EncryptedMessage for Manager.DecryptMessage.
func DecodeOmemoMessage(elem *OmemoEncryptedElem, senderJID string) (*omemolib.EncryptedMessage, error) {
	msg := &omemolib.EncryptedMessage{
		Sender: omemolib.Device{JID: senderJID, ID: omemolib.DeviceID(elem.Header.SID)},
	}
	if elem.Payload != "" {
		p, err := base64.StdEncoding.DecodeString(elem.Payload)
		if err != nil {
			return nil, fmt.Errorf("decoding omemo payload: %w", err)
		}
		msg.Payload = p
	}
	for _, k := range elem.Header.Keys {
		data, err := base64.StdEncoding.DecodeString(k.Data)
		if err != nil {
			return nil, fmt.Errorf("decoding omemo key data: %w", err)
		}
		rk := omemolib.RecipientKey{Device: omemolib.DeviceID(k.RID), Data: data}
		if k.Kex {
			ik, err := base64.StdEncoding.DecodeString(k.IdentityKey)
			if err != nil {
				return nil, fmt.Errorf("decoding omemo kex identity key: %w", err)
			}
			ek, err := base64.StdEncoding.DecodeString(k.EphemeralKey)
			if err != nil {
				return nil, fmt.Errorf("decoding omemo kex ephemeral key: %w", err)
			}
			rk.KeyExchange = &omemolib.KeyExchange{
				IdentityKey:    ik,
				EphemeralKey:   ek,
				SignedPreKeyID: k.SPKID,
				PreKeyID:       k.PKID,
			}
		}
		msg.Keys = append(msg.Keys, rk)
	}
	return msg, nil
}

// omemoTransport adapts a *Client to omemolib.Transport.
type omemoTransport struct{ c *Client }

// OmemoTransport returns c as an omemolib.Transport, for use with
// omemolib.NewManager.
func (c *Client) OmemoTransport() omemolib.Transport { return omemoTransport{c: c} }

func (t omemoTransport) FetchDeviceList(ctx context.Context, jid string) (omemolib.DeviceList, error) {
	return t.c.FetchOmemoDeviceList(ctx, jid)
}

func (t omemoTransport) PublishDeviceList(ctx context.Context, list omemolib.DeviceList) error {
	return t.c.PublishOmemoDeviceList(ctx, list)
}

func (t omemoTransport) FetchBundle(ctx context.Context, dev omemolib.Device) (omemolib.Bundle, error) {
	return t.c.FetchOmemoBundle(ctx, dev)
}

func (t omemoTransport) PublishBundle(ctx context.Context, bundle omemolib.Bundle) error {
	return t.c.PublishOmemoBundle(ctx, bundle)
}

var _ = xmpp.Session{} // keep mellium.im/xmpp import used if the above ever trims down

// DeleteOmemoBundle deletes our own bundle node for deviceID — used when
// an OMEMO device ID is rotated away from, so the abandoned ID stops
// advertising a bundle peers could still build a session against.
func (c *Client) DeleteOmemoBundle(ctx context.Context, deviceID omemolib.DeviceID) error {
	return c.deleteNode(ctx, omemoWireV2.bundleNode(deviceID))
}
