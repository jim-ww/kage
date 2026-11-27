package xmpp

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"

	"mellium.im/xmlstream"
	"mellium.im/xmpp/bookmarks"
	"mellium.im/xmpp/jid"
	"mellium.im/xmpp/pubsub"
	"mellium.im/xmpp/stanza"
)

// Bookmark is a room in our bookmarks (XEP-0402), which every client of the
// account shares.
type Bookmark struct {
	Room     string // bare JID
	Name     string
	Nick     string
	Password string
	Autojoin bool
}

// BookmarksChangedEvent is a PEP push: our bookmarks changed, to re-fetch.
type BookmarksChangedEvent struct{}

func (BookmarksChangedEvent) isEvent() {}

// Bookmarks fetches our bookmarks. A server with no bookmarks node yet has
// none, not an error.
func (c *Client) Bookmarks(ctx context.Context) ([]Bookmark, error) {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	iter := bookmarks.Fetch(ctx, c.session)
	defer iter.Close()
	var out []Bookmark
	for iter.Next() {
		b := iter.Bookmark()
		out = append(out, Bookmark{
			Room:     b.JID.Bare().String(),
			Name:     b.Name,
			Nick:     b.Nick,
			Password: b.Password,
			Autojoin: b.Autojoin,
		})
	}
	if err := iter.Err(); err != nil {
		var se stanza.Error
		if errors.As(err, &se) && se.Condition == stanza.ItemNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("fetching bookmarks: %w", err)
	}
	return out, nil
}

// PublishBookmark adds or updates b, its node private to us (XEP-0402 §3.2).
func (c *Client) PublishBookmark(ctx context.Context, b Bookmark) error {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	room, err := jid.Parse(b.Room)
	if err != nil {
		return fmt.Errorf("parsing room %q: %w", b.Room, err)
	}
	item := bookmarks.Channel{JID: room.Bare(), Name: b.Name, Nick: b.Nick, Password: b.Password, Autojoin: b.Autojoin}
	publish := xmlstream.Wrap(
		xmlstream.Wrap(item.TokenReader(), xml.StartElement{
			Name: xml.Name{Local: "item"},
			Attr: []xml.Attr{{Name: xml.Name{Local: "id"}, Value: room.Bare().String()}},
		}),
		xml.StartElement{Name: xml.Name{Local: "publish"}, Attr: []xml.Attr{{Name: xml.Name{Local: "node"}, Value: bookmarks.NS}}},
	)
	options := xmlstream.Wrap(
		xmlstream.Wrap(
			xmlstream.MultiReader(
				formField("FORM_TYPE", "http://jabber.org/protocol/pubsub#publish-options", "hidden"),
				formField("pubsub#persist_items", "true", ""),
				formField("pubsub#max_items", "max", ""),
				formField("pubsub#send_last_published_item", "never", ""),
				formField("pubsub#access_model", "whitelist", ""),
			),
			xml.StartElement{
				Name: xml.Name{Space: "jabber:x:data", Local: "x"},
				Attr: []xml.Attr{{Name: xml.Name{Local: "type"}, Value: "submit"}},
			},
		),
		xml.StartElement{Name: xml.Name{Local: "publish-options"}},
	)
	return c.session.UnmarshalIQElement(ctx, xmlstream.Wrap(
		xmlstream.MultiReader(publish, options),
		xml.StartElement{Name: xml.Name{Space: pubsub.NS, Local: "pubsub"}},
	), stanza.IQ{Type: stanza.SetIQ}, nil)
}

// RemoveBookmark retracts room's bookmark.
func (c *Client) RemoveBookmark(ctx context.Context, room string) error {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	j, err := jid.Parse(room)
	if err != nil {
		return fmt.Errorf("parsing room %q: %w", room, err)
	}
	return bookmarks.Delete(ctx, c.session, j.Bare())
}

// formField is one data form field (XEP-0004) with a single value.
func formField(name, value, typ string) xml.TokenReader {
	attrs := []xml.Attr{{Name: xml.Name{Local: "var"}, Value: name}}
	if typ != "" {
		attrs = append(attrs, xml.Attr{Name: xml.Name{Local: "type"}, Value: typ})
	}
	return xmlstream.Wrap(
		xmlstream.Wrap(xmlstream.Token(xml.CharData(value)), xml.StartElement{Name: xml.Name{Local: "value"}}),
		xml.StartElement{Name: xml.Name{Local: "field"}, Attr: attrs},
	)
}
