package xmpp

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"log/slog"

	"mellium.im/xmlstream"
	"mellium.im/xmpp/form"
	"mellium.im/xmpp/jid"
	"mellium.im/xmpp/pubsub"
	"mellium.im/xmpp/stanza"
)

// XEP-0373 (OpenPGP Public Keys via PEP): a single metadata node lists which
// key(s) an account has published, and each key gets its own node holding
// the raw (base64-encoded) OpenPGP transferable public key.
const (
	pepOpenPGPNS     = "urn:xmpp:openpgp:0"
	pepMetadataNode  = "urn:xmpp:openpgp:0:public-keys"
	pepKeyNodePrefix = "urn:xmpp:openpgp:0:public-keys:"
	pepPublishItemID = "current"
)

// PublishOpenPGPKey publishes fingerprint's raw key data (as returned by
// gpg.Export — not armored) to our own PEP nodes: the key itself, and the
// metadata node advertising its fingerprint so contacts can discover it.
func (c *Client) PublishOpenPGPKey(ctx context.Context, fingerprint string, keyData []byte) error {
	keyElem := xmlstream.Wrap(
		xmlstream.Wrap(
			xmlstream.Token(xml.CharData(base64.StdEncoding.EncodeToString(keyData))),
			xml.StartElement{Name: xml.Name{Space: pepOpenPGPNS, Local: "data"}},
		),
		xml.StartElement{Name: xml.Name{Space: pepOpenPGPNS, Local: "pubkey"}},
	)
	keyNode := pepKeyNodePrefix + fingerprint
	if _, err := pubsub.Publish(ctx, c.session, keyNode, pepPublishItemID, keyElem); err != nil {
		return fmt.Errorf("publishing key node: %w", err)
	}
	c.makeNodeOpen(ctx, keyNode)

	metaElem := xmlstream.Wrap(
		xmlstream.Wrap(nil, xml.StartElement{
			Name: xml.Name{Space: pepOpenPGPNS, Local: "pubkey-metadata"},
			Attr: []xml.Attr{{Name: xml.Name{Local: "v4-fingerprint"}, Value: fingerprint}},
		}),
		xml.StartElement{Name: xml.Name{Space: pepOpenPGPNS, Local: "public-keys-list"}},
	)
	if _, err := pubsub.Publish(ctx, c.session, pepMetadataNode, pepPublishItemID, metaElem); err != nil {
		return fmt.Errorf("publishing metadata node: %w", err)
	}
	c.makeNodeOpen(ctx, pepMetadataNode)
	return nil
}

// makeNodeOpen reconfigures a PEP node's access model to "open" — the whole
// point of publishing an OpenPGP/OMEMO key or bundle here is for anyone
// (mutual contact or not) to discover it, so the access-controlled default
// most PEP node types auto-create with (e.g. "presence", visible only to
// subscribed contacts, or worse "whitelist", visible only to us) defeats the
// purpose. Best-effort: some servers may not honor node reconfiguration, in
// which case the node keeps whatever default access model it was
// auto-created with — logged rather than silently swallowed, since a server
// that rejects this leaves the node invisible to contacts with no other
// symptom on our end (the publish itself still succeeds).
func (c *Client) makeNodeOpen(ctx context.Context, node string) {
	cfg := form.New(
		form.Hidden("FORM_TYPE", form.Value("http://jabber.org/protocol/pubsub#node_config")),
		form.List("pubsub#access_model", form.Value("open")),
	)
	if err := pubsub.SetConfig(ctx, c.session, node, cfg); err != nil {
		slog.Warn("makeNodeOpen: reconfiguring to open access failed", "node", node, "err", err)
		return
	}
	slog.Debug("makeNodeOpen: reconfigured to open access", "node", node)
}

type pubkeyMetadataList struct {
	XMLName xml.Name         `xml:"urn:xmpp:openpgp:0 public-keys-list"`
	Keys    []pubkeyMetadata `xml:"urn:xmpp:openpgp:0 pubkey-metadata"`
}

type pubkeyMetadata struct {
	Fingerprint string `xml:"v4-fingerprint,attr"`
}

type pubkeyElem struct {
	XMLName xml.Name `xml:"urn:xmpp:openpgp:0 pubkey"`
	Data    string   `xml:"urn:xmpp:openpgp:0 data"`
}

// FetchOpenPGPFingerprints queries peerJID's PEP metadata node for the
// fingerprints of the OpenPGP keys they've published, most-recently-added
// first (per the node's own item order).
func (c *Client) FetchOpenPGPFingerprints(ctx context.Context, peerJID string) ([]string, error) {
	peer, err := jid.Parse(peerJID)
	if err != nil {
		return nil, fmt.Errorf("parsing peer jid %q: %w", peerJID, err)
	}

	iter := pubsub.FetchIQ(ctx, stanza.IQ{To: peer}, c.session, pubsub.Query{Node: pepMetadataNode})
	defer iter.Close()

	var fprs []string
	for iter.Next() {
		_, r := iter.Item()
		var meta pubkeyMetadataList
		if err := xml.NewTokenDecoder(r).Decode(&meta); err != nil {
			continue
		}
		for _, k := range meta.Keys {
			if k.Fingerprint != "" {
				fprs = append(fprs, k.Fingerprint)
			}
		}
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("fetching openpgp metadata for %s: %w", peerJID, err)
	}
	return fprs, nil
}

// FetchOpenPGPKey fetches the raw (decoded, not armored) OpenPGP public key
// data for the given fingerprint from peerJID's PEP key node.
func (c *Client) FetchOpenPGPKey(ctx context.Context, peerJID, fingerprint string) ([]byte, error) {
	peer, err := jid.Parse(peerJID)
	if err != nil {
		return nil, fmt.Errorf("parsing peer jid %q: %w", peerJID, err)
	}

	iter := pubsub.FetchIQ(ctx, stanza.IQ{To: peer}, c.session, pubsub.Query{Node: pepKeyNodePrefix + fingerprint})
	defer iter.Close()

	if !iter.Next() {
		if err := iter.Err(); err != nil {
			return nil, fmt.Errorf("fetching openpgp key %s for %s: %w", fingerprint, peerJID, err)
		}
		return nil, fmt.Errorf("no openpgp key %s published by %s", fingerprint, peerJID)
	}
	_, r := iter.Item()
	var key pubkeyElem
	if err := xml.NewTokenDecoder(r).Decode(&key); err != nil {
		return nil, fmt.Errorf("decoding openpgp key from %s: %w", peerJID, err)
	}
	data, err := base64.StdEncoding.DecodeString(key.Data)
	if err != nil {
		return nil, fmt.Errorf("decoding base64 key data from %s: %w", peerJID, err)
	}
	return data, nil
}
