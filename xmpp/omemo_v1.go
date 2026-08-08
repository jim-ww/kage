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
	"mellium.im/xmpp/jid"
	"mellium.im/xmpp/pubsub"
	"mellium.im/xmpp/stanza"

	omemolib "github.com/jim-ww/omemo-go"
)

// Legacy OMEMO (pre-XEP-0384 standardization, eu.siacs.conversations.axolotl
// namespace, "OMEMO v1"): device list + bundle exchange via PEP, and the
// <encrypted/> element carried inside a normal <message/>. This mirrors
// omemo.go's OMEMO 2 wire format, with the differences legacy clients
// (older Conversations/Gajim/ChatSecure/Dino) actually speak - verified
// against a real captured bundle and github.com/janimo/textsecure/axolotl,
// a real pure-Go implementation of the underlying libsignal wire protocol,
// rather than assumed from the OMEMO 2 shape:
//
//   - a <list/> device-list wrapper instead of <devices/>, differently-named
//     bundle child elements;
//   - every public key on the wire (identityKey, signedPreKeyPublic,
//     preKeyPublic in the bundle) carries a leading 0x05 "DJB type" byte,
//     making it 33 bytes rather than the raw 32 omemolib deals in - see
//     serializeLegacyKey/parseLegacyKey;
//   - the <key/> element carries no ik/ek/spkid/pkid attributes at all
//     (unlike OMEMO 2's <key kex="true">): its base64 content is a complete,
//     self-describing PreKeyWhisperMessage or WhisperMessage blob (see
//     xochimilco/legacysignal), bundling that metadata itself. Only "rid"
//     and a "prekey" boolean survive as separate wire attributes;
//   - an explicit <iv/> element, since legacy's payload cipher (AES-GCM)
//     doesn't derive its IV from key material the way OMEMO 2's does - see
//     xochimilco.EncryptPayloadV1.
const (
	omemoV1NS               = "eu.siacs.conversations.axolotl"
	omemoV1DevicesNode      = "eu.siacs.conversations.axolotl.devicelist"
	omemoV1BundleNodePrefix = "eu.siacs.conversations.axolotl.bundles:"
	omemoV1ItemID           = "current"

	// omemoV1KeyType is the "DJB type" byte libsignal prefixes every
	// Curve25519 public key with on the wire - confirmed against a real
	// captured bundle (identityKey values are 44 base64 characters with no
	// padding, which only decodes evenly to 33 bytes).
	omemoV1KeyType = 0x05
)

// serializeLegacyKey prefixes a raw 32-byte Curve25519 public key with
// omemoV1KeyType, as every public key field in this wire protocol requires.
func serializeLegacyKey(pub []byte) []byte {
	return append([]byte{omemoV1KeyType}, pub...)
}

// parseLegacyKey strips and validates the omemoV1KeyType prefix, recovering
// the raw 32-byte Curve25519 public key omemolib deals in.
func parseLegacyKey(b []byte) ([]byte, error) {
	if len(b) != 33 {
		return nil, fmt.Errorf("serialized public key MUST be of 33 bytes, got %d", len(b))
	}
	if b[0] != omemoV1KeyType {
		return nil, fmt.Errorf("unsupported public key type byte 0x%02x", b[0])
	}
	return b[1:], nil
}

// ── Device list ──────────────────────────────────────────────────────────

type omemoV1DeviceElem struct {
	ID uint32 `xml:"id,attr"`
}

type omemoV1ListElem struct {
	XMLName xml.Name            `xml:"eu.siacs.conversations.axolotl list"`
	Devices []omemoV1DeviceElem `xml:"eu.siacs.conversations.axolotl device"`
}

// PublishOmemoDeviceListV1 publishes list to our own legacy PEP device-list
// node.
func (c *Client) PublishOmemoDeviceListV1(ctx context.Context, list omemolib.DeviceList) error {
	children := make([]xml.TokenReader, len(list.Devices))
	for i, id := range list.Devices {
		children[i] = xmlstream.Wrap(nil, xml.StartElement{
			Name: xml.Name{Space: omemoV1NS, Local: "device"},
			Attr: []xml.Attr{{Name: xml.Name{Local: "id"}, Value: strconv.FormatUint(uint64(id), 10)}},
		})
	}
	elem := xmlstream.Wrap(
		xmlstream.MultiReader(children...),
		xml.StartElement{Name: xml.Name{Space: omemoV1NS, Local: "list"}},
	)
	if _, err := pubsub.Publish(ctx, c.session, omemoV1DevicesNode, omemoV1ItemID, elem); err != nil {
		return fmt.Errorf("publishing legacy omemo device list: %w", err)
	}
	c.makeNodeOpen(ctx, omemoV1DevicesNode)
	return nil
}

// FetchOmemoDeviceListV1 fetches peerJID's published legacy device list.
func (c *Client) FetchOmemoDeviceListV1(ctx context.Context, peerJID string) (omemolib.DeviceList, error) {
	peer, err := jid.Parse(peerJID)
	if err != nil {
		return omemolib.DeviceList{}, fmt.Errorf("parsing peer jid %q: %w", peerJID, err)
	}

	iter := pubsub.FetchIQ(ctx, stanza.IQ{To: peer}, c.session, pubsub.Query{Node: omemoV1DevicesNode})
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
		var list omemoV1ListElem
		if err := xml.NewTokenDecoder(r).Decode(&list); err != nil {
			slog.Warn("FetchOmemoDeviceListV1: failed to decode device-list item", "peer", peerJID, "err", err)
			continue
		}
		ids = ids[:0]
		for _, d := range list.Devices {
			ids = append(ids, omemolib.DeviceID(d.ID))
		}
	}
	if err := iter.Err(); err != nil {
		if strings.Contains(err.Error(), "item-not-found") || strings.Contains(err.Error(), "Node not found") {
			return omemolib.DeviceList{JID: peerJID, Devices: ids}, nil
		}
		return omemolib.DeviceList{}, fmt.Errorf("fetching legacy omemo device list for %s: %w", peerJID, err)
	}
	return omemolib.DeviceList{JID: peerJID, Devices: ids}, nil
}

// ── Bundle ───────────────────────────────────────────────────────────────

type omemoV1BundleElem struct {
	XMLName      xml.Name `xml:"eu.siacs.conversations.axolotl bundle"`
	SignedPreKey struct {
		ID     uint32 `xml:"signedPreKeyId,attr"`
		Public string `xml:",chardata"`
	} `xml:"eu.siacs.conversations.axolotl signedPreKeyPublic"`
	SignedPreKeySig string `xml:"eu.siacs.conversations.axolotl signedPreKeySignature"`
	IdentityKey     string `xml:"eu.siacs.conversations.axolotl identityKey"`
	PreKeys         []struct {
		ID     uint32 `xml:"preKeyId,attr"`
		Public string `xml:",chardata"`
	} `xml:"eu.siacs.conversations.axolotl prekeys>preKeyPublic"`
}

// PublishOmemoBundleV1 publishes bundle to our own legacy PEP bundle node
// for its device ID.
func (c *Client) PublishOmemoBundleV1(ctx context.Context, bundle omemolib.Bundle) error {
	pkElems := make([]xml.TokenReader, len(bundle.PreKeys))
	for i, pk := range bundle.PreKeys {
		pkElems[i] = xmlstream.Wrap(
			xmlstream.Token(xml.CharData(base64.StdEncoding.EncodeToString(serializeLegacyKey(pk.Public)))),
			xml.StartElement{
				Name: xml.Name{Space: omemoV1NS, Local: "preKeyPublic"},
				Attr: []xml.Attr{{Name: xml.Name{Local: "preKeyId"}, Value: strconv.FormatUint(uint64(pk.ID), 10)}},
			},
		)
	}

	elem := xmlstream.Wrap(
		xmlstream.MultiReader(
			xmlstream.Wrap(
				xmlstream.Token(xml.CharData(base64.StdEncoding.EncodeToString(serializeLegacyKey(bundle.SignedPreKey.Public)))),
				xml.StartElement{
					Name: xml.Name{Space: omemoV1NS, Local: "signedPreKeyPublic"},
					Attr: []xml.Attr{{Name: xml.Name{Local: "signedPreKeyId"}, Value: strconv.FormatUint(uint64(bundle.SignedPreKey.ID), 10)}},
				},
			),
			xmlstream.Wrap(
				xmlstream.Token(xml.CharData(base64.StdEncoding.EncodeToString(bundle.SignedPreKey.Signature))),
				xml.StartElement{Name: xml.Name{Space: omemoV1NS, Local: "signedPreKeySignature"}},
			),
			xmlstream.Wrap(
				xmlstream.Token(xml.CharData(base64.StdEncoding.EncodeToString(serializeLegacyKey(bundle.IdentityKey)))),
				xml.StartElement{Name: xml.Name{Space: omemoV1NS, Local: "identityKey"}},
			),
			xmlstream.Wrap(
				xmlstream.MultiReader(pkElems...),
				xml.StartElement{Name: xml.Name{Space: omemoV1NS, Local: "prekeys"}},
			),
		),
		xml.StartElement{Name: xml.Name{Space: omemoV1NS, Local: "bundle"}},
	)

	node := omemoV1BundleNodePrefix + strconv.FormatUint(uint64(bundle.Device.ID), 10)
	if _, err := pubsub.Publish(ctx, c.session, node, omemoV1ItemID, elem); err != nil {
		return fmt.Errorf("publishing legacy omemo bundle: %w", err)
	}
	c.makeNodeOpen(ctx, node)
	return nil
}

// FetchOmemoBundleV1 fetches dev's published legacy bundle.
func (c *Client) FetchOmemoBundleV1(ctx context.Context, dev omemolib.Device) (omemolib.Bundle, error) {
	peer, err := jid.Parse(dev.JID)
	if err != nil {
		return omemolib.Bundle{}, fmt.Errorf("parsing peer jid %q: %w", dev.JID, err)
	}

	node := omemoV1BundleNodePrefix + strconv.FormatUint(uint64(dev.ID), 10)
	iter := pubsub.FetchIQ(ctx, stanza.IQ{To: peer}, c.session, pubsub.Query{Node: node})
	defer iter.Close()

	if !iter.Next() {
		if err := iter.Err(); err != nil {
			return omemolib.Bundle{}, fmt.Errorf("fetching legacy omemo bundle for %s/%d: %w", dev.JID, dev.ID, err)
		}
		return omemolib.Bundle{}, fmt.Errorf("no legacy omemo bundle published for %s/%d", dev.JID, dev.ID)
	}
	_, r := iter.Item()
	var b omemoV1BundleElem
	if err := xml.NewTokenDecoder(r).Decode(&b); err != nil {
		return omemolib.Bundle{}, fmt.Errorf("decoding legacy omemo bundle from %s: %w", dev.JID, err)
	}

	ikSerialized, err := base64.StdEncoding.DecodeString(b.IdentityKey)
	if err != nil {
		return omemolib.Bundle{}, fmt.Errorf("decoding identity key: %w", err)
	}
	ik, err := parseLegacyKey(ikSerialized)
	if err != nil {
		return omemolib.Bundle{}, fmt.Errorf("parsing identity key: %w", err)
	}
	spkPubSerialized, err := base64.StdEncoding.DecodeString(b.SignedPreKey.Public)
	if err != nil {
		return omemolib.Bundle{}, fmt.Errorf("decoding signed prekey: %w", err)
	}
	spkPub, err := parseLegacyKey(spkPubSerialized)
	if err != nil {
		return omemolib.Bundle{}, fmt.Errorf("parsing signed prekey: %w", err)
	}
	spkSig, err := base64.StdEncoding.DecodeString(b.SignedPreKeySig)
	if err != nil {
		return omemolib.Bundle{}, fmt.Errorf("decoding signed prekey signature: %w", err)
	}

	preKeys := make([]omemolib.PreKey, 0, len(b.PreKeys))
	for _, pk := range b.PreKeys {
		serialized, err := base64.StdEncoding.DecodeString(pk.Public)
		if err != nil {
			continue
		}
		pub, err := parseLegacyKey(serialized)
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

// omemoV1KeyElem carries a recipient device's wrapped key. Unlike OMEMO 2's
// <key kex="true" ik="..." ek="..." spkid="..." pkid="...">, this wire
// protocol's <key> has no separate key-exchange attributes at all: when
// PreKey is true, Data is a complete, self-describing PreKeyWhisperMessage
// blob (see xochimilco/legacysignal.PreKeyMessage) that already bundles the
// sender's identity key, X3DH ephemeral key, and which of the recipient's
// signed/one-time prekeys were used.
type omemoV1KeyElem struct {
	RID    uint32 `xml:"rid,attr"`
	PreKey bool   `xml:"prekey,attr,omitempty"`
	Data   string `xml:",chardata"`
}

type omemoV1HeaderElem struct {
	SID  uint32           `xml:"sid,attr"`
	Keys []omemoV1KeyElem `xml:"eu.siacs.conversations.axolotl key"`
	IV   string           `xml:"eu.siacs.conversations.axolotl iv,omitempty"`
}

// omemoEncryptedElemV1 is our wire encoding of an omemolib.EncryptedMessage
// under the legacy protocol - the eu.siacs.conversations.axolotl
// counterpart to omemoEncryptedElem.
type omemoEncryptedElemV1 struct {
	XMLName xml.Name          `xml:"eu.siacs.conversations.axolotl encrypted"`
	Header  omemoV1HeaderElem `xml:"eu.siacs.conversations.axolotl header"`
	Payload string            `xml:"eu.siacs.conversations.axolotl payload,omitempty"`
}

// EncodeOmemoMessageV1 converts msg into its legacy wire element for
// embedding in an outgoing <message/> stanza. k.Data is already the
// complete wire blob (PreKeyWhisperMessage or WhisperMessage, produced by
// internal/signal.Session.Encrypt) for either case - k.KeyExchange being
// non-nil only tells us which one, for the "prekey" attribute.
func EncodeOmemoMessageV1(msg *omemolib.EncryptedMessage) *omemoEncryptedElemV1 {
	elem := &omemoEncryptedElemV1{
		Header: omemoV1HeaderElem{SID: uint32(msg.Sender.ID)},
	}
	if msg.Payload != nil {
		elem.Payload = base64.StdEncoding.EncodeToString(msg.Payload)
	}
	if msg.IV != nil {
		elem.Header.IV = base64.StdEncoding.EncodeToString(msg.IV)
	}
	for _, k := range msg.Keys {
		elem.Header.Keys = append(elem.Header.Keys, omemoV1KeyElem{
			RID:    uint32(k.Device),
			PreKey: k.KeyExchange != nil,
			Data:   base64.StdEncoding.EncodeToString(k.Data),
		})
	}
	return elem
}

// DecodeOmemoMessageV1 converts a received legacy wire element back into an
// omemolib.EncryptedMessage for Manager.DecryptMessage. For a prekey
// message, KeyExchange is set to a non-nil empty marker only - the actual
// key-exchange parameters live inside Data itself and are extracted by
// internal/signal.PeekLegacyPreKeyIDs/NewPassiveSessionFromPreKeyBlob.
func DecodeOmemoMessageV1(elem *omemoEncryptedElemV1, senderJID string) (*omemolib.EncryptedMessage, error) {
	msg := &omemolib.EncryptedMessage{
		Sender: omemolib.Device{JID: senderJID, ID: omemolib.DeviceID(elem.Header.SID)},
	}
	if elem.Payload != "" {
		p, err := base64.StdEncoding.DecodeString(elem.Payload)
		if err != nil {
			return nil, fmt.Errorf("decoding legacy omemo payload: %w", err)
		}
		msg.Payload = p
	}
	if elem.Header.IV != "" {
		iv, err := base64.StdEncoding.DecodeString(elem.Header.IV)
		if err != nil {
			return nil, fmt.Errorf("decoding legacy omemo iv: %w", err)
		}
		msg.IV = iv
	}
	for _, k := range elem.Header.Keys {
		data, err := base64.StdEncoding.DecodeString(k.Data)
		if err != nil {
			return nil, fmt.Errorf("decoding legacy omemo key data: %w", err)
		}
		rk := omemolib.RecipientKey{Device: omemolib.DeviceID(k.RID), Data: data}
		if k.PreKey {
			rk.KeyExchange = &omemolib.KeyExchange{}
		}
		msg.Keys = append(msg.Keys, rk)
	}
	return msg, nil
}

// omemoTransportV1 adapts a *Client to omemolib.Transport for the legacy
// protocol.
type omemoTransportV1 struct{ c *Client }

// OmemoTransportV1 returns c as an omemolib.Transport speaking legacy OMEMO,
// for use with omemolib.NewManager(..., omemolib.ProtocolV1, ...).
func (c *Client) OmemoTransportV1() omemolib.Transport { return omemoTransportV1{c: c} }

func (t omemoTransportV1) FetchDeviceList(ctx context.Context, jid string) (omemolib.DeviceList, error) {
	return t.c.FetchOmemoDeviceListV1(ctx, jid)
}

func (t omemoTransportV1) PublishDeviceList(ctx context.Context, list omemolib.DeviceList) error {
	return t.c.PublishOmemoDeviceListV1(ctx, list)
}

func (t omemoTransportV1) FetchBundle(ctx context.Context, dev omemolib.Device) (omemolib.Bundle, error) {
	return t.c.FetchOmemoBundleV1(ctx, dev)
}

func (t omemoTransportV1) PublishBundle(ctx context.Context, bundle omemolib.Bundle) error {
	return t.c.PublishOmemoBundleV1(ctx, bundle)
}
