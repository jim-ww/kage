package xmpp

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
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

// ── Device list ──────────────────────────────────────────────────────────

type omemoDeviceElem struct {
	ID uint32 `xml:"id,attr"`
}

type omemoDevicesElem struct {
	XMLName xml.Name          `xml:"urn:xmpp:omemo:2 devices"`
	Devices []omemoDeviceElem `xml:"urn:xmpp:omemo:2 device"`
}

// PublishOmemoDeviceList publishes list to our own PEP device-list node.
func (c *Client) PublishOmemoDeviceList(ctx context.Context, list omemolib.DeviceList) error {
	children := make([]xml.TokenReader, len(list.Devices))
	for i, id := range list.Devices {
		children[i] = xmlstream.Wrap(nil, xml.StartElement{
			Name: xml.Name{Space: omemoNS, Local: "device"},
			Attr: []xml.Attr{{Name: xml.Name{Local: "id"}, Value: strconv.FormatUint(uint64(id), 10)}},
		})
	}
	elem := xmlstream.Wrap(
		xmlstream.MultiReader(children...),
		xml.StartElement{Name: xml.Name{Space: omemoNS, Local: "devices"}},
	)
	if _, err := pubsub.Publish(ctx, c.session, omemoDevicesNode, omemoItemID, elem); err != nil {
		return fmt.Errorf("publishing omemo device list: %w", err)
	}
	c.makeNodeOpen(ctx, omemoDevicesNode)
	return nil
}

// FetchOmemoDeviceList fetches peerJID's published device list.
func (c *Client) FetchOmemoDeviceList(ctx context.Context, peerJID string) (omemolib.DeviceList, error) {
	peer, err := jid.Parse(peerJID)
	if err != nil {
		return omemolib.DeviceList{}, fmt.Errorf("parsing peer jid %q: %w", peerJID, err)
	}

	iter := pubsub.FetchIQ(ctx, stanza.IQ{To: peer}, c.session, pubsub.Query{Node: omemoDevicesNode})
	defer iter.Close()

	var ids []omemolib.DeviceID
	for iter.Next() {
		_, r := iter.Item()
		var devices omemoDevicesElem
		if err := xml.NewTokenDecoder(r).Decode(&devices); err != nil {
			if c.Debugf != nil {
				c.Debugf("FetchOmemoDeviceList: %s: failed to decode device-list item: %v", peerJID, err)
			}
			continue
		}
		for _, d := range devices.Devices {
			ids = append(ids, omemolib.DeviceID(d.ID))
		}
	}
	if err := iter.Err(); err != nil {
		// Treat "item-not-found" / "Node not found" as empty list (first-time
		// setup, no device-list node published yet). Anything else (timeout,
		// service-unavailable, not-authorized, ...) is a real fetch failure
		// and must propagate - otherwise it gets cached as "peer has zero
		// devices" and every future send silently fails with ErrNoRecipients.
		if strings.Contains(err.Error(), "item-not-found") || strings.Contains(err.Error(), "Node not found") {
			return omemolib.DeviceList{JID: peerJID, Devices: ids}, nil
		}
		return omemolib.DeviceList{}, fmt.Errorf("fetching omemo device list for %s: %w", peerJID, err)
	}
	if c.Debugf != nil {
		c.Debugf("FetchOmemoDeviceList: %s has %d device(s): %v", peerJID, len(ids), ids)
	}
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

	node := omemoBundleNodePrefix + strconv.FormatUint(uint64(bundle.Device.ID), 10)
	if _, err := pubsub.Publish(ctx, c.session, node, omemoItemID, elem); err != nil {
		return fmt.Errorf("publishing omemo bundle: %w", err)
	}
	c.makeNodeOpen(ctx, node)
	return nil
}

// FetchOmemoBundle fetches dev's published bundle.
func (c *Client) FetchOmemoBundle(ctx context.Context, dev omemolib.Device) (omemolib.Bundle, error) {
	peer, err := jid.Parse(dev.JID)
	if err != nil {
		return omemolib.Bundle{}, fmt.Errorf("parsing peer jid %q: %w", dev.JID, err)
	}

	node := omemoBundleNodePrefix + strconv.FormatUint(uint64(dev.ID), 10)
	iter := pubsub.FetchIQ(ctx, stanza.IQ{To: peer}, c.session, pubsub.Query{Node: node})
	defer iter.Close()

	if !iter.Next() {
		if err := iter.Err(); err != nil {
			return omemolib.Bundle{}, fmt.Errorf("fetching omemo bundle for %s/%d: %w", dev.JID, dev.ID, err)
		}
		return omemolib.Bundle{}, fmt.Errorf("no omemo bundle published for %s/%d", dev.JID, dev.ID)
	}
	_, r := iter.Item()
	var b omemoBundleElem
	if err := xml.NewTokenDecoder(r).Decode(&b); err != nil {
		return omemolib.Bundle{}, fmt.Errorf("decoding omemo bundle from %s: %w", dev.JID, err)
	}

	ik, err := base64.StdEncoding.DecodeString(b.IdentityKey)
	if err != nil {
		return omemolib.Bundle{}, fmt.Errorf("decoding identity key: %w", err)
	}
	spkPub, err := base64.StdEncoding.DecodeString(b.SignedPreKey.Public)
	if err != nil {
		return omemolib.Bundle{}, fmt.Errorf("decoding signed prekey: %w", err)
	}
	spkSig, err := base64.StdEncoding.DecodeString(b.SignedPreKeySig)
	if err != nil {
		return omemolib.Bundle{}, fmt.Errorf("decoding signed prekey signature: %w", err)
	}

	preKeys := make([]omemolib.PreKey, 0, len(b.PreKeys))
	for _, pk := range b.PreKeys {
		pub, err := base64.StdEncoding.DecodeString(pk.Public)
		if err != nil {
			continue
		}
		preKeys = append(preKeys, omemolib.PreKey{ID: pk.ID, Public: pub})
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

// omemoEncryptedElem is our wire encoding of an omemolib.EncryptedMessage.
type omemoEncryptedElem struct {
	XMLName xml.Name        `xml:"urn:xmpp:omemo:2 encrypted"`
	Header  omemoHeaderElem `xml:"urn:xmpp:omemo:2 header"`
	Payload string          `xml:"urn:xmpp:omemo:2 payload,omitempty"`
}

// EncodeOmemoMessage converts msg into its wire element for embedding in an
// outgoing <message/> stanza.
func EncodeOmemoMessage(msg *omemolib.EncryptedMessage) *omemoEncryptedElem {
	elem := &omemoEncryptedElem{
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
func DecodeOmemoMessage(elem *omemoEncryptedElem, senderJID string) (*omemolib.EncryptedMessage, error) {
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
