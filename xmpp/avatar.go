package xmpp

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"

	"mellium.im/xmlstream"
	"mellium.im/xmpp/jid"
	"mellium.im/xmpp/pubsub"
	"mellium.im/xmpp/stanza"
)

// XEP-0084 (User Avatar): a contact publishes their avatar as two PEP
// nodes — metadata, listing what's available and the SHA-1 of each, and
// data, holding the base64 bytes under an item id equal to that same SHA-1.
// The id being the content hash is what makes caching trivial: hash what we
// already have, and if it matches the advertised id there is nothing to
// fetch.
const (
	avatarMetadataNode = "urn:xmpp:avatar:metadata"
	avatarDataNode     = "urn:xmpp:avatar:data"
	// The namespaces are spelled the same as the node names, but are a
	// separate thing — the node is where an item lives, the namespace is
	// what the item's payload is.
	avatarMetadataNS = "urn:xmpp:avatar:metadata"
	avatarDataNS     = "urn:xmpp:avatar:data"
	// avatarEmptyItemID is the item id the "no avatar" metadata is
	// published under. Any id would do; a fixed one keeps a contact's
	// removal from accumulating items.
	avatarEmptyItemID = "current"
)

// AvatarMaxBytes caps how large an avatar we're willing to pull down. A
// contact controls this number entirely, and the whole thing is base64 in a
// single stanza, so it needs a bound that isn't "whatever they sent".
const AvatarMaxBytes = 1 << 20

// AvatarInfo describes one published avatar: its SHA-1 (which is also the
// data node's item id), its media type, and its size.
type AvatarInfo struct {
	ID    string
	Type  string
	Bytes int
}

// metadataElem is the payload of the avatar metadata node. A contact may
// advertise several renditions; those with a url attribute live over HTTP
// rather than in the data node, and are ignored — we only fetch what PEP
// itself will hand us.
type metadataElem struct {
	Info []struct {
		ID     string `xml:"id,attr"`
		Type   string `xml:"type,attr"`
		Bytes  int    `xml:"bytes,attr"`
		URL    string `xml:"url,attr"`
		Width  int    `xml:"width,attr"`
		Height int    `xml:"height,attr"`
	} `xml:"urn:xmpp:avatar:metadata info"`
}

type avatarDataElem struct {
	Data string `xml:",chardata"`
}

// FetchAvatarMetadata reports which avatar peerJID currently publishes.
// Returns ok == false when they publish none — either no node at all, or a
// metadata item with no <info/>, which is how XEP-0084 says "I removed my
// avatar" — so a caller can tell that apart from an error.
func (c *Client) FetchAvatarMetadata(ctx context.Context, peerJID string) (info AvatarInfo, ok bool, err error) {
	peer, err := jid.Parse(peerJID)
	if err != nil {
		return AvatarInfo{}, false, fmt.Errorf("parsing peer jid %q: %w", peerJID, err)
	}

	iter := pubsub.FetchIQ(ctx, stanza.IQ{To: peer}, c.session, pubsub.Query{Node: avatarMetadataNode})
	defer iter.Close()

	if !iter.Next() {
		if err := iter.Err(); err != nil {
			// An item-not-found/not-subscribed error here is the normal
			// case for a contact with no avatar, not something worth
			// surfacing — the caller logs at debug and moves on.
			return AvatarInfo{}, false, fmt.Errorf("fetching avatar metadata for %s: %w", peerJID, err)
		}
		return AvatarInfo{}, false, nil
	}
	_, r := iter.Item()
	var meta metadataElem
	if err := xml.NewTokenDecoder(r).Decode(&meta); err != nil {
		return AvatarInfo{}, false, fmt.Errorf("decoding avatar metadata from %s: %w", peerJID, err)
	}
	for _, in := range meta.Info {
		if in.URL != "" || in.ID == "" {
			continue // published over HTTP, not in the data node
		}
		return AvatarInfo{ID: in.ID, Type: in.Type, Bytes: in.Bytes}, true, nil
	}
	return AvatarInfo{}, false, nil
}

// FetchAvatarData pulls the image bytes peerJID published under id — the
// SHA-1 that FetchAvatarMetadata reported. The returned bytes are not
// verified against that hash; callers that care (see the daemon's avatar
// cache) check it themselves, since it's also what they key the cache on.
func (c *Client) FetchAvatarData(ctx context.Context, peerJID, id string) ([]byte, error) {
	peer, err := jid.Parse(peerJID)
	if err != nil {
		return nil, fmt.Errorf("parsing peer jid %q: %w", peerJID, err)
	}

	iter := pubsub.FetchIQ(ctx, stanza.IQ{To: peer}, c.session, pubsub.Query{Node: avatarDataNode, Item: id})
	defer iter.Close()

	if !iter.Next() {
		if err := iter.Err(); err != nil {
			return nil, fmt.Errorf("fetching avatar data %s from %s: %w", id, peerJID, err)
		}
		return nil, fmt.Errorf("no avatar data %s published by %s", id, peerJID)
	}
	_, r := iter.Item()
	var payload avatarDataElem
	if err := xml.NewTokenDecoder(r).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decoding avatar data from %s: %w", peerJID, err)
	}
	// Servers and clients wrap base64 at various widths; the decoder wants
	// it unbroken.
	encoded := strings.Join(strings.Fields(payload.Data), "")
	if decodedLen := base64.StdEncoding.DecodedLen(len(encoded)); decodedLen > AvatarMaxBytes {
		return nil, fmt.Errorf("avatar from %s is %d bytes, over the %d limit", peerJID, decodedLen, AvatarMaxBytes)
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decoding base64 avatar data from %s: %w", peerJID, err)
	}
	if len(data) > AvatarMaxBytes {
		return nil, fmt.Errorf("avatar from %s is %d bytes, over the %d limit", peerJID, len(data), AvatarMaxBytes)
	}
	return data, nil
}

// PublishAvatar publishes data as this account's avatar: the bytes go to
// the data node under an item id equal to their SHA-1, and the metadata
// node advertises that same id, so a contact can tell from metadata alone
// whether what they already cached is current.
//
// Both nodes are reconfigured to open access, for the same reason
// PublishOpenPGPKey does it: an avatar nobody can fetch is pointless, and
// PEP's default access model is not "anyone".
func (c *Client) PublishAvatar(ctx context.Context, data []byte, mediaType string, width, height int) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("avatar is empty")
	}
	if len(data) > AvatarMaxBytes {
		return "", fmt.Errorf("avatar is %d bytes, over the %d limit", len(data), AvatarMaxBytes)
	}
	id := avatarID(data)

	dataElem := xmlstream.Wrap(
		xmlstream.Token(xml.CharData(base64.StdEncoding.EncodeToString(data))),
		xml.StartElement{Name: xml.Name{Space: avatarDataNS, Local: "data"}},
	)
	if _, err := pubsub.Publish(ctx, c.session, avatarDataNode, id, dataElem); err != nil {
		return "", fmt.Errorf("publishing avatar data: %w", err)
	}
	c.makeNodeOpen(ctx, avatarDataNode)

	attrs := []xml.Attr{
		{Name: xml.Name{Local: "id"}, Value: id},
		{Name: xml.Name{Local: "bytes"}, Value: strconv.Itoa(len(data))},
		{Name: xml.Name{Local: "type"}, Value: mediaType},
	}
	if width > 0 && height > 0 {
		attrs = append(attrs,
			xml.Attr{Name: xml.Name{Local: "width"}, Value: strconv.Itoa(width)},
			xml.Attr{Name: xml.Name{Local: "height"}, Value: strconv.Itoa(height)},
		)
	}
	metaElem := xmlstream.Wrap(
		xmlstream.Wrap(nil, xml.StartElement{
			Name: xml.Name{Space: avatarMetadataNS, Local: "info"},
			Attr: attrs,
		}),
		xml.StartElement{Name: xml.Name{Space: avatarMetadataNS, Local: "metadata"}},
	)
	// The metadata item carries the same id as the data item: XEP-0084 has
	// contacts compare that id against what they cached, and an "id" that
	// varied per publish would make every contact re-download.
	if _, err := pubsub.Publish(ctx, c.session, avatarMetadataNode, id, metaElem); err != nil {
		return "", fmt.Errorf("publishing avatar metadata: %w", err)
	}
	c.makeNodeOpen(ctx, avatarMetadataNode)
	return id, nil
}

// avatarID is the item id an avatar is published under: the SHA-1 of its
// bytes, per XEP-0084. Contacts decide whether to re-download by comparing
// this against what they cached, so it must be derived from the content and
// nothing else.
func avatarID(data []byte) string {
	sum := sha1.Sum(data)
	return hex.EncodeToString(sum[:])
}

// DeleteAvatar stops advertising an avatar, by publishing an empty
// <metadata/> — which is how XEP-0084 spells "I have no avatar". The data
// node is left alone deliberately: contacts key their cache on the id the
// metadata advertises, so with nothing advertised the old bytes are
// unreachable, and retracting them is an extra round trip that can fail
// separately and leave the two nodes disagreeing.
func (c *Client) DeleteAvatar(ctx context.Context) error {
	empty := xmlstream.Wrap(nil, xml.StartElement{
		Name: xml.Name{Space: avatarMetadataNS, Local: "metadata"},
	})
	if _, err := pubsub.Publish(ctx, c.session, avatarMetadataNode, avatarEmptyItemID, empty); err != nil {
		return fmt.Errorf("publishing empty avatar metadata: %w", err)
	}
	return nil
}
