package xmpp

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"

	"mellium.im/xmlstream"
	"mellium.im/xmpp/jid"
	"mellium.im/xmpp/stanza"
)

// Legacy bookmarks (XEP-0048) live in private XML storage (XEP-0049),
// read and written whole.
const (
	privateNS         = "jabber:iq:private"
	legacyBookmarksNS = "storage:bookmarks"
)

// legacyElem is an element of the legacy storage, kept as it came so
// writing the storage back loses nothing another client put there.
type legacyElem struct {
	XMLName  xml.Name
	Attrs    []xml.Attr   `xml:",any,attr"`
	Text     string       `xml:",chardata"`
	Children []legacyElem `xml:",any"`
}

// child is the text of e's child named name.
func (e legacyElem) child(name string) string {
	for _, c := range e.Children {
		if c.XMLName.Local == name {
			return c.Text
		}
	}
	return ""
}

func (e legacyElem) attr(name string) string {
	for _, a := range e.Attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

type legacyStorage struct {
	XMLName  xml.Name     `xml:"storage:bookmarks storage"`
	Elements []legacyElem `xml:",any"`
}

// conferenceRoom is the bare room JID of a conference element, "" for any
// other element or a bad JID.
func (e legacyElem) conferenceRoom() string {
	if e.XMLName.Local != "conference" {
		return ""
	}
	j, err := jid.Parse(e.attr("jid"))
	if err != nil {
		return ""
	}
	return j.Bare().String()
}

func (c *Client) legacyStorage(ctx context.Context) (legacyStorage, error) {
	query := xmlstream.Wrap(
		xmlstream.Wrap(nil, xml.StartElement{Name: xml.Name{Space: legacyBookmarksNS, Local: "storage"}}),
		xml.StartElement{Name: xml.Name{Space: privateNS, Local: "query"}},
	)
	var resp struct {
		Storage legacyStorage `xml:"storage:bookmarks storage"`
	}
	err := c.session.UnmarshalIQElement(ctx, query, stanza.IQ{Type: stanza.GetIQ}, &resp)
	return resp.Storage, err
}

// LegacyBookmarks fetches the rooms in our legacy bookmarks. A server
// without private storage has none, not an error.
func (c *Client) LegacyBookmarks(ctx context.Context) ([]Bookmark, error) {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	storage, err := c.legacyStorage(ctx)
	if err != nil {
		var se stanza.Error
		if errors.As(err, &se) && (se.Condition == stanza.ItemNotFound || se.Condition == stanza.ServiceUnavailable || se.Condition == stanza.FeatureNotImplemented) {
			return nil, nil
		}
		return nil, fmt.Errorf("fetching legacy bookmarks: %w", err)
	}
	return legacyBookmarks(storage), nil
}

func legacyBookmarks(storage legacyStorage) []Bookmark {
	var out []Bookmark
	for _, e := range storage.Elements {
		room := e.conferenceRoom()
		if room == "" {
			continue
		}
		autojoin := strings.TrimSpace(e.attr("autojoin"))
		out = append(out, Bookmark{
			Room:     room,
			Name:     e.attr("name"),
			Nick:     strings.TrimSpace(e.child("nick")),
			Password: e.child("password"),
			Autojoin: autojoin == "true" || autojoin == "1",
		})
	}
	return out
}

// RemoveLegacyBookmark drops room from our legacy bookmarks, leaving the
// rest as they were. Nothing is written when it isn't there.
func (c *Client) RemoveLegacyBookmark(ctx context.Context, room string) error {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	storage, err := c.legacyStorage(ctx)
	if err != nil {
		var se stanza.Error
		if errors.As(err, &se) {
			return nil
		}
		return fmt.Errorf("fetching legacy bookmarks: %w", err)
	}
	kept := storage.Elements[:0]
	for _, e := range storage.Elements {
		if e.conferenceRoom() != room {
			kept = append(kept, e)
		}
	}
	if len(kept) == len(storage.Elements) {
		return nil
	}
	storage.Elements = kept
	raw, err := xml.Marshal(storage)
	if err != nil {
		return err
	}
	query := xmlstream.Wrap(xml.NewDecoder(bytes.NewReader(raw)), xml.StartElement{Name: xml.Name{Space: privateNS, Local: "query"}})
	return c.session.UnmarshalIQElement(ctx, query, stanza.IQ{Type: stanza.SetIQ}, nil)
}
